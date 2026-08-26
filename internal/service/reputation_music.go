package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const MusicContributionRuleV1 = "music-contribution-v1"

type musicContributionScore struct {
	Family string
	Type   string
	Points int
}

// RecordMusicRevisionContribution records the effective business result of an
// approved music revision. It is a no-op before the reputation schema exists.
func RecordMusicRevisionContribution(tx *gorm.DB, revision *model.Revision) error {
	if tx == nil || revision == nil || revision.ID == uuid.Nil || !tx.Migrator().HasTable(&model.MusicContributionEvent{}) {
		return nil
	}
	if revision.EditType == "revert" {
		if revision.PreviousRevisionID == nil {
			return nil
		}
		return revokeMusicContributionSource(tx, "revision", *revision.PreviousRevisionID, "revision reverted")
	}
	if !isReputationMusicEntity(revision.ContentType) {
		return nil
	}
	if isAutomatedMusicOperation(revision.EditSummary) {
		return nil
	}

	score, metadata, err := scoreMusicRevision(tx, revision)
	if err != nil || score.Points == 0 {
		return err
	}
	return recordMusicContributionEvent(tx, musicContributionEventInput{
		ActorID: revision.EditorID, TargetType: revision.ContentType, TargetID: revision.ContentID,
		OccurredAt: revision.CreatedAt, Score: score, SourceKind: "revision", SourceID: revision.ID,
		Metadata: metadata,
	})
}

// RecordMusicLyricsContribution records one non-revert lyric version.
func RecordMusicLyricsContribution(tx *gorm.DB, version *model.MusicSongLyricVersion, wasNew, hadTranslation, hadTiming bool) error {
	if tx == nil || version == nil || version.ID == uuid.Nil || !tx.Migrator().HasTable(&model.MusicContributionEvent{}) {
		return nil
	}
	if normalizedContributionTarget(version.Target) == "restore" {
		var previous model.MusicSongLyricVersion
		if err := tx.Where("song_id = ? AND version < ?", version.SongID, version.Version).
			Order("version DESC").First(&previous).Error; err == nil {
			return revokeMusicContributionSource(tx, "lyrics", previous.ID, "lyrics reverted")
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return nil
	}
	if isAutomatedMusicOperation(version.EditSummary) {
		return nil
	}

	target := normalizedContributionTarget(version.Target)
	score := musicContributionScore{Family: "lyrics.original", Type: "lyrics.edit", Points: 1}
	switch target {
	case "translation":
		score.Family = "lyrics.translation"
		if !hadTranslation && strings.TrimSpace(version.Translation) != "" {
			score.Type, score.Points = "lyrics.translation.add", 6
		}
	case "timing":
		score.Family = "lyrics.timing"
		if !hadTiming && version.Format == "lrc" {
			score.Type, score.Points = "lyrics.timing.add", 4
		}
	case "import", "all", "original":
		if wasNew && strings.TrimSpace(version.Content) != "" {
			score.Type, score.Points = "lyrics.original.add", 6
		}
	}
	if wasNew {
		var song model.Song
		if err := tx.Select("album_id", "created_at").First(&song, "id = ?", version.SongID).Error; err != nil {
			return err
		}
		if song.AlbumID != nil && !song.CreatedAt.IsZero() && version.CreatedAt.Sub(song.CreatedAt) <= 5*time.Minute {
			return nil
		}
	}
	metadata, err := json.Marshal(map[string]any{"version": version.Version, "target": target})
	if err != nil {
		return err
	}
	return recordMusicContributionEvent(tx, musicContributionEventInput{
		ActorID: version.CreatedBy, TargetType: "song", TargetID: version.SongID,
		OccurredAt: version.CreatedAt, Score: score, SourceKind: "lyrics", SourceID: version.ID,
		Metadata: metadata,
	})
}

type musicContributionEventInput struct {
	ActorID    uuid.UUID
	TargetType string
	TargetID   uuid.UUID
	OccurredAt time.Time
	Score      musicContributionScore
	SourceKind string
	SourceID   uuid.UUID
	Metadata   []byte
}

func recordMusicContributionEvent(tx *gorm.DB, input musicContributionEventInput) error {
	occurredAt := input.OccurredAt.UTC()
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	naturalDate := time.Date(occurredAt.Year(), occurredAt.Month(), occurredAt.Day(), 0, 0, 0, 0, time.UTC)
	mergeKey := fmt.Sprintf("%s:%s:%s:%s:%s", input.ActorID, input.TargetType, input.TargetID, input.Score.Family, naturalDate.Format("2006-01-02"))
	event := model.MusicContributionEvent{
		ActorUserID: input.ActorID, OperationFamily: input.Score.Family, OperationType: input.Score.Type,
		TargetType: input.TargetType, TargetID: input.TargetID, NaturalDate: naturalDate,
		Points: input.Score.Points, Status: model.ContributionEvidenceActive, EffectiveAt: occurredAt,
		MergeKey: mergeKey, SourceKind: input.SourceKind, SourceID: input.SourceID,
		RuleVersion: MusicContributionRuleV1, Metadata: input.Metadata,
	}
	var existing model.MusicContributionEvent
	err := tx.Where("source_kind = ? AND source_id = ?", input.SourceKind, input.SourceID).First(&existing).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		if err := tx.Create(&event).Error; err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		return refreshMusicContributionEvidence(tx, existing.MergeKey)
	}
	return refreshMusicContributionEvidence(tx, mergeKey)
}

func revokeMusicContributionSource(tx *gorm.DB, sourceKind string, sourceID uuid.UUID, reason string) error {
	var event model.MusicContributionEvent
	if err := tx.Where("source_kind = ? AND source_id = ?", sourceKind, sourceID).First(&event).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := tx.Model(&event).Updates(map[string]any{
		"status": model.ContributionEvidenceRevoked, "revoked_at": now, "revocation_reason": reason,
	}).Error; err != nil {
		return err
	}
	return refreshMusicContributionEvidence(tx, event.MergeKey)
}

func refreshMusicContributionEvidence(tx *gorm.DB, mergeKey string) error {
	var events []model.MusicContributionEvent
	if err := tx.Where("merge_key = ? AND status = ?", mergeKey, model.ContributionEvidenceActive).
		Order("points DESC, effective_at ASC").Find(&events).Error; err != nil {
		return err
	}
	var evidence model.MusicContributionEvidence
	err := tx.Where("merge_key = ?", mergeKey).First(&evidence).Error
	if len(events) == 0 {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		return tx.Model(&evidence).Updates(map[string]any{
			"points": 0, "status": model.ContributionEvidenceRevoked, "revoked_at": now,
			"revocation_reason": "all merged events revoked",
		}).Error
	}
	winner := events[0]
	values := map[string]any{
		"actor_user_id": winner.ActorUserID, "operation_family": winner.OperationFamily,
		"operation_type": winner.OperationType, "target_type": winner.TargetType, "target_id": winner.TargetID,
		"natural_date": winner.NaturalDate, "points": winner.Points, "status": model.ContributionEvidenceActive,
		"effective_at": winner.EffectiveAt, "revoked_at": nil, "revocation_reason": "",
		"source_revision_id": nil, "source_lyric_id": nil, "rule_version": winner.RuleVersion,
		"metadata": winner.Metadata,
	}
	if winner.SourceKind == "revision" {
		values["source_revision_id"] = winner.SourceID
	} else if winner.SourceKind == "lyrics" {
		values["source_lyric_id"] = winner.SourceID
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		evidence = model.MusicContributionEvidence{
			ActorUserID: winner.ActorUserID, OperationFamily: winner.OperationFamily, OperationType: winner.OperationType,
			TargetType: winner.TargetType, TargetID: winner.TargetID, NaturalDate: winner.NaturalDate,
			Points: winner.Points, Status: model.ContributionEvidenceActive, EffectiveAt: winner.EffectiveAt,
			MergeKey: mergeKey, RuleVersion: winner.RuleVersion, Metadata: winner.Metadata,
		}
		if winner.SourceKind == "revision" {
			evidence.SourceRevisionID = &winner.SourceID
		} else if winner.SourceKind == "lyrics" {
			evidence.SourceLyricID = &winner.SourceID
		}
		return tx.Create(&evidence).Error
	}
	if err != nil {
		return err
	}
	return tx.Model(&evidence).Updates(values).Error
}

func scoreMusicRevision(tx *gorm.DB, revision *model.Revision) (musicContributionScore, []byte, error) {
	current, err := decodeJSONMap(revision.ContentSnapshot)
	if err != nil {
		return musicContributionScore{}, nil, err
	}
	if revision.EditType == "creation" {
		score := scoreMusicCreation(revision.ContentType, current)
		metadata, err := json.Marshal(map[string]any{"version": revision.VersionNumber, "edit_type": revision.EditType})
		if err != nil {
			return musicContributionScore{}, nil, err
		}
		return score, metadata, nil
	}
	if revision.EditType == "audio_replace" {
		return musicContributionScore{Family: "song.audio", Type: "song.audio.replace", Points: 2}, nil, nil
	}
	if revision.PreviousRevisionID == nil {
		return musicContributionScore{}, nil, nil
	}
	var previous model.Revision
	if err := tx.First(&previous, "id = ?", *revision.PreviousRevisionID).Error; err != nil {
		return musicContributionScore{}, nil, err
	}
	before, err := decodeJSONMap(previous.ContentSnapshot)
	if err != nil {
		return musicContributionScore{}, nil, err
	}
	score, fields := scoreMusicChanges(revision.ContentType, before, current)
	metadata, err := json.Marshal(map[string]any{"version": revision.VersionNumber, "changed_fields": fields})
	if err != nil {
		return musicContributionScore{}, nil, err
	}
	return score, metadata, nil
}

func scoreMusicCreation(contentType string, snapshot map[string]any) musicContributionScore {
	switch contentType {
	case "artist":
		if nonEmpty(snapshot["name"]) && nonEmpty(snapshot["artist_form"]) && artistHasIdentity(snapshot) {
			return musicContributionScore{Family: "artist.create", Type: "artist.create", Points: 6}
		}
	case "album":
		album := nestedMap(snapshot, "album")
		if nonEmpty(album["title"]) && nonEmpty(album["album_type"]) && sliceLen(snapshot["artist_credits"]) > 0 && sliceLen(snapshot["songs"]) > 0 {
			return musicContributionScore{Family: "album.create", Type: "album.create", Points: 10}
		}
	case "song":
		if nonEmpty(snapshot["title"]) && nonEmpty(snapshot["release_type"]) && !nonEmpty(snapshot["album_id"]) && sliceLen(snapshot["artist_credits"]) > 0 {
			return musicContributionScore{Family: "song.create", Type: "song.create", Points: 6}
		}
	}
	return musicContributionScore{}
}

func scoreMusicChanges(contentType string, before, after map[string]any) (musicContributionScore, []string) {
	fields := changedTopLevelFields(before, after)
	if len(fields) == 0 {
		return musicContributionScore{}, nil
	}
	switch contentType {
	case "artist":
		if changed(before, after, "members") {
			return musicContributionScore{Family: "artist.members", Type: "artist.members.edit", Points: 2}, fields
		}
		if changed(before, after, "bio") {
			return musicContributionScore{Family: "artist.metadata", Type: "artist.bio.edit", Points: 2}, fields
		}
		if onlyIgnoredFields(fields, "sources", "entry_status") {
			return musicContributionScore{}, fields
		}
		return musicContributionScore{Family: "artist.metadata", Type: "artist.metadata.edit", Points: 1}, fields
	case "album":
		beforeSongs := objectIDs(before["songs"])
		afterSongs := objectIDs(after["songs"])
		if hasAddedIDs(beforeSongs, afterSongs) {
			return musicContributionScore{Family: "album.tracks", Type: "album.tracks.add", Points: 3}, fields
		}
		albumBefore := nestedMap(before, "album")
		albumAfter := nestedMap(after, "album")
		if changed(albumBefore, albumAfter, "description") || changed(before, after, "artist_credits") {
			return musicContributionScore{Family: "album.metadata", Type: "album.metadata.substantial", Points: 2}, fields
		}
		if hasRemovedIDs(beforeSongs, afterSongs) {
			return musicContributionScore{Family: "album.tracks", Type: "album.tracks.remove", Points: 1}, fields
		}
		if onlyIgnoredFields(fields, "sources") {
			return musicContributionScore{}, fields
		}
		return musicContributionScore{Family: "album.metadata", Type: "album.metadata.edit", Points: 1}, fields
	case "song":
		if changed(before, after, "audio_url") {
			points, op := 2, "song.audio.replace"
			if !nonEmpty(after["audio_url"]) {
				points, op = 1, "song.audio.remove"
			}
			return musicContributionScore{Family: "song.audio", Type: op, Points: points}, fields
		}
		if changed(before, after, "description") || changed(before, after, "artist_credits") {
			return musicContributionScore{Family: "song.metadata", Type: "song.metadata.substantial", Points: 2}, fields
		}
		if onlyIgnoredFields(fields, "sources", "status", "lyrics") {
			return musicContributionScore{}, fields
		}
		return musicContributionScore{Family: "song.metadata", Type: "song.metadata.edit", Points: 1}, fields
	}
	return musicContributionScore{}, fields
}

func decodeJSONMap(raw []byte) (map[string]any, error) {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func nestedMap(value map[string]any, key string) map[string]any {
	result, ok := value[key].(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return result
}

func changed(before, after map[string]any, field string) bool {
	return !reflect.DeepEqual(before[field], after[field])
}

func changedTopLevelFields(before, after map[string]any) []string {
	keys := make(map[string]struct{}, len(before)+len(after))
	for key := range before {
		keys[key] = struct{}{}
	}
	for key := range after {
		keys[key] = struct{}{}
	}
	fields := make([]string, 0, len(keys))
	for key := range keys {
		if changed(before, after, key) {
			fields = append(fields, key)
		}
	}
	sort.Strings(fields)
	return fields
}

func onlyIgnoredFields(fields []string, ignored ...string) bool {
	set := make(map[string]struct{}, len(ignored))
	for _, field := range ignored {
		set[field] = struct{}{}
	}
	for _, field := range fields {
		if _, ok := set[field]; !ok {
			return false
		}
	}
	return true
}

func nonEmpty(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case nil:
		return false
	default:
		return !reflect.ValueOf(value).IsZero()
	}
}

func sliceLen(value any) int {
	if values, ok := value.([]any); ok {
		return len(values)
	}
	return 0
}

func artistHasIdentity(snapshot map[string]any) bool {
	for _, field := range []string{"legal_name", "stage_names_json", "nationality", "birth_place", "birth_date", "birth_year", "active_start_date"} {
		if nonEmpty(snapshot[field]) {
			return true
		}
	}
	return false
}

func objectIDs(value any) map[string]struct{} {
	result := map[string]struct{}{}
	values, _ := value.([]any)
	for _, item := range values {
		object, _ := item.(map[string]any)
		if id, ok := object["id"].(string); ok && id != "" {
			result[id] = struct{}{}
		}
	}
	return result
}

func hasAddedIDs(before, after map[string]struct{}) bool {
	for id := range after {
		if _, ok := before[id]; !ok {
			return true
		}
	}
	return false
}

func hasRemovedIDs(before, after map[string]struct{}) bool { return hasAddedIDs(after, before) }

func isAutomatedMusicOperation(summary string) bool {
	normalized := strings.ToLower(strings.TrimSpace(summary))
	if normalized == "" {
		return false
	}
	for _, marker := range []string{
		"initial version (migrated from existing data)",
		"初始版本",
		"从旧歌词字段迁移",
		"迁移为独立歌曲",
		"自动匹配",
		"自动识别",
		"自动补全",
		"批量修复",
		"修复 lrc 歌词时间轴",
		"通过专辑版本更新歌词",
		"通过歌曲版本更新歌词",
		"通过歌曲上传创建歌词",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func isReputationMusicEntity(contentType string) bool {
	return contentType == "artist" || contentType == "album" || contentType == "song"
}

func normalizedContributionTarget(target string) string {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return "all"
	}
	return target
}
