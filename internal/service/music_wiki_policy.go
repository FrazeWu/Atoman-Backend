package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	musicEditWarWindow     = 24 * time.Hour
	musicEditWarReversions = 3
)

type MusicEntryState struct {
	EntityType      string
	EntityID        uuid.UUID
	LifecycleStatus string
	EditStatus      string
	OwnerID         *uuid.UUID
	AlbumID         *uuid.UUID
}

func LoadMusicEntryState(tx *gorm.DB, entityType string, entityID uuid.UUID, lock bool) (MusicEntryState, error) {
	return loadMusicEntryState(tx, entityType, entityID, lock)
}

func UpdateMusicEntryEditStatus(tx *gorm.DB, entityType string, entityID uuid.UUID, status string) error {
	return updateMusicEntryEditStatus(tx, entityType, entityID, status)
}

// ValidateMusicEntryEdit enforces the Wiki lifecycle and edit-state policy for every write path.
func ValidateMusicEntryEdit(tx *gorm.DB, entityType string, entityID, editorID uuid.UUID, fields ...string) error {
	state, err := loadMusicEntryState(tx, entityType, entityID, true)
	if err != nil {
		return err
	}
	var editor model.User
	isAdmin := tx.Select("role").First(&editor, "uuid = ?", editorID).Error == nil && authctx.RoleAtLeast(editor.Role, authctx.RoleAdmin)

	switch state.LifecycleStatus {
	case model.MusicLifecycleActive:
	case model.MusicLifecycleDraft:
		if !isAdmin && (state.OwnerID == nil || *state.OwnerID != editorID) {
			return apperr.Forbidden("music.edit_forbidden", "Only the draft creator can edit this entry")
		}
	default:
		return apperr.Forbidden("music.entry_not_editable", "This music entry is not editable")
	}
	if state.EditStatus != model.MusicEditDevelopment {
		return apperr.Conflict("music.entry_edit_closed", "This music entry must be reopened before editing")
	}

	if entityType == "song" && state.AlbumID != nil {
		album, albumErr := loadMusicEntryState(tx, "album", *state.AlbumID, false)
		if albumErr != nil {
			return albumErr
		}
		if album.EditStatus == model.MusicEditClosed && !lyricsOnlyMusicEdit(fields) {
			return apperr.Conflict("music.album_edit_closed", "The album must be reopened before editing song metadata")
		}
	}
	return nil
}

func lyricsOnlyMusicEdit(fields []string) bool {
	if len(fields) == 0 {
		return false
	}
	for _, field := range fields {
		switch strings.TrimSpace(field) {
		case "lyrics", "lyrics_content", "lyrics_translation", "lyrics_timing", "lyrics_annotation":
		default:
			return false
		}
	}
	return true
}

func loadMusicEntryState(tx *gorm.DB, entityType string, entityID uuid.UUID, lock bool) (MusicEntryState, error) {
	query := tx
	if lock && supportsRowLock(tx) {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	state := MusicEntryState{EntityType: entityType, EntityID: entityID}
	switch entityType {
	case "artist":
		var artist model.Artist
		if err := query.Select("id", "lifecycle_status", "edit_status", "created_by").First(&artist, "id = ?", entityID).Error; err != nil {
			return MusicEntryState{}, musicEntryStateError(err, entityType)
		}
		state.LifecycleStatus, state.EditStatus, state.OwnerID = artist.LifecycleStatus, artist.EditStatus, artist.CreatedBy
	case "album":
		var album model.Album
		if err := query.Select("id", "lifecycle_status", "edit_status", "uploaded_by").First(&album, "id = ?", entityID).Error; err != nil {
			return MusicEntryState{}, musicEntryStateError(err, entityType)
		}
		state.LifecycleStatus, state.EditStatus, state.OwnerID = album.LifecycleStatus, album.EditStatus, album.UploadedBy
	case "song":
		var song model.Song
		if err := query.Select("id", "lifecycle_status", "edit_status", "uploaded_by", "album_id").First(&song, "id = ?", entityID).Error; err != nil {
			return MusicEntryState{}, musicEntryStateError(err, entityType)
		}
		state.LifecycleStatus, state.EditStatus, state.OwnerID, state.AlbumID = song.LifecycleStatus, song.EditStatus, song.UploadedBy, song.AlbumID
	default:
		return MusicEntryState{}, apperr.BadRequest("music.invalid_entity_type", "Unsupported music entry type")
	}
	if state.LifecycleStatus == "" {
		state.LifecycleStatus = model.MusicLifecycleActive
	}
	if state.EditStatus == "" {
		state.EditStatus = model.MusicEditDevelopment
	}
	return state, nil
}

func musicEntryStateError(err error, entityType string) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.NotFound("music."+entityType+"_not_found", "Music entry not found")
	}
	return err
}

// HandleMusicRevisionApplied invalidates stale close requests and detects edit wars in the same transaction.
func HandleMusicRevisionApplied(tx *gorm.DB, revision *model.Revision) error {
	if revision == nil || revision.ID == uuid.Nil {
		return nil
	}
	if err := SupersedeMusicCloseRequests(tx, revision.ContentType, revision.ContentID, revision.ID); err != nil {
		return err
	}
	if err := RecordMusicRevisionContribution(tx, revision); err != nil {
		return err
	}
	if revision.EditType == "creation" || !isWikiMusicEntity(revision.ContentType) {
		return nil
	}
	var editor model.User
	if err := tx.Select("role").First(&editor, "uuid = ?", revision.EditorID).Error; err == nil && authctx.RoleAtLeast(editor.Role, authctx.RoleAdmin) {
		return nil
	}
	locked, field, err := musicRevisionEditWar(tx, revision)
	if err != nil || !locked {
		return err
	}
	state, err := loadMusicEntryState(tx, revision.ContentType, revision.ContentID, true)
	if err != nil {
		return err
	}
	if state.EditStatus != model.MusicEditDevelopment {
		return nil
	}
	if err := updateMusicEntryEditStatus(tx, revision.ContentType, revision.ContentID, model.MusicEditLocked); err != nil {
		return err
	}
	if tx.Migrator().HasTable(&model.MusicEntryStateEvent{}) {
		reason := fmt.Sprintf("Automatic edit-war lock after repeated changes to %s", field)
		event := model.MusicEntryStateEvent{
			EntityType: revision.ContentType, EntityID: revision.ContentID,
			FromStatus: state.EditStatus, ToStatus: model.MusicEditLocked, Trigger: "automatic",
			RevisionID: &revision.ID, Reason: reason,
		}
		if err := tx.Create(&event).Error; err != nil {
			return err
		}
	}
	return nil
}

func SupersedeAllMusicCloseRequests(tx *gorm.DB, entityType string, entityID uuid.UUID) error {
	if !tx.Migrator().HasTable(&model.MusicEntryStateRequest{}) {
		return nil
	}
	return tx.Model(&model.MusicEntryStateRequest{}).
		Where("entity_type = ? AND entity_id = ? AND action = ? AND status = ?", entityType, entityID, model.MusicStateActionClose, model.MusicStateRequestPending).
		Update("status", model.MusicStateRequestSuperseded).Error
}

// SupersedeMusicCloseRequests marks a version-bound close request stale after content changes.
func SupersedeMusicCloseRequests(tx *gorm.DB, entityType string, entityID, currentRevisionID uuid.UUID) error {
	if !tx.Migrator().HasTable(&model.MusicEntryStateRequest{}) {
		return nil
	}
	return tx.Model(&model.MusicEntryStateRequest{}).
		Where("entity_type = ? AND entity_id = ? AND action = ? AND status = ? AND (base_revision_id IS NULL OR base_revision_id <> ?)",
			entityType, entityID, model.MusicStateActionClose, model.MusicStateRequestPending, currentRevisionID).
		Update("status", model.MusicStateRequestSuperseded).Error
}

func musicRevisionEditWar(tx *gorm.DB, current *model.Revision) (bool, string, error) {
	var revisions []model.Revision
	if err := tx.Preload("Editor").Where("content_type = ? AND content_id = ? AND status = ?", current.ContentType, current.ContentID, "approved").
		Order("version_number DESC").Limit(100).Find(&revisions).Error; err != nil {
		return false, "", err
	}
	sort.Slice(revisions, func(i, j int) bool { return revisions[i].VersionNumber < revisions[j].VersionNumber })
	windowStart := current.CreatedAt.Add(-musicEditWarWindow)
	type reversionStats struct {
		count   int
		editors map[uuid.UUID]struct{}
	}
	stats := map[string]*reversionStats{}
	flattened := make([]map[string]string, len(revisions))
	for i := range revisions {
		flat, err := flattenRevisionSnapshot(revisions[i].ContentSnapshot)
		if err != nil {
			return false, "", err
		}
		flattened[i] = flat
		if i == 0 || revisions[i].CreatedAt.Before(windowStart) || revisions[i].Editor == nil || authctx.RoleAtLeast(revisions[i].Editor.Role, authctx.RoleAdmin) {
			continue
		}
		for path, value := range flat {
			if flattened[i-1][path] == value || !revisionHistoryContainsValue(flattened[:i-1], path, value) {
				continue
			}
			item := stats[path]
			if item == nil {
				item = &reversionStats{editors: map[uuid.UUID]struct{}{}}
				stats[path] = item
			}
			item.count++
			item.editors[revisions[i].EditorID] = struct{}{}
		}
	}
	for path, item := range stats {
		if item.count >= musicEditWarReversions && len(item.editors) >= 2 {
			return true, path, nil
		}
	}
	return false, "", nil
}

func flattenRevisionSnapshot(raw []byte) (map[string]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	result := map[string]string{}
	flattenRevisionValue(result, "", value)
	return result, nil
}

func flattenRevisionValue(result map[string]string, path string, value any) {
	if object, ok := value.(map[string]any); ok {
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			next := key
			if path != "" {
				next = path + "." + key
			}
			flattenRevisionValue(result, next, object[key])
		}
		return
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		result[path] = fmt.Sprintf("%v", value)
		return
	}
	result[path] = string(encoded)
}

func revisionHistoryContainsValue(history []map[string]string, path, value string) bool {
	for _, snapshot := range history {
		if snapshot[path] == value {
			return true
		}
	}
	return false
}

func updateMusicEntryEditStatus(tx *gorm.DB, entityType string, entityID uuid.UUID, status string) error {
	var entity any
	switch entityType {
	case "artist":
		entity = &model.Artist{}
	case "album":
		entity = &model.Album{}
	case "song":
		entity = &model.Song{}
	default:
		return apperr.BadRequest("music.invalid_entity_type", "Unsupported music entry type")
	}
	return tx.Model(entity).Where("id = ?", entityID).Update("edit_status", status).Error
}

func isWikiMusicEntity(entityType string) bool {
	return entityType == "artist" || entityType == "album" || entityType == "song"
}
