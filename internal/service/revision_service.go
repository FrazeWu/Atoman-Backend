package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
)

// RevisionService handles revision-related operations
type RevisionService struct {
	db *gorm.DB
}

func NewRevisionService(db *gorm.DB) *RevisionService {
	return &RevisionService{db: db}
}

// GetDB returns the database instance
func (s *RevisionService) GetDB() *gorm.DB {
	return s.db
}

// CreateRevision creates a new revision with conflict detection
func (s *RevisionService) CreateRevision(
	contentType string,
	contentID uuid.UUID,
	editorID uuid.UUID,
	changes map[string]interface{},
	editSummary string,
	baseRevisionNumber int,
	autoApprove bool,
) (*model.Revision, []model.EditConflict, error) {
	// Get base revision
	var baseRevision model.Revision
	if err := s.db.Where("content_id = ? AND content_type = ? AND version_number = ?",
		contentID, contentType, baseRevisionNumber).
		First(&baseRevision).Error; err != nil {
		return nil, nil, fmt.Errorf("base revision not found: %w", err)
	}

	// Get current revision
	var currentRevision model.Revision
	if err := s.db.Where("content_id = ? AND content_type = ? AND is_current = ?",
		contentID, contentType, true).
		Order("version_number DESC").
		First(&currentRevision).Error; err != nil {
		return nil, nil, fmt.Errorf("current revision not found: %w", err)
	}

	// Detect conflicts if base != current
	var conflicts []model.EditConflict
	if baseRevision.VersionNumber != currentRevision.VersionNumber {
		conflicts = s.DetectConflicts(&baseRevision, changes, &currentRevision)
	}

	// If conflicts exist, return them without creating revision
	if len(conflicts) > 0 {
		// Save conflicts to database
		for i := range conflicts {
			conflicts[i].ContentType = contentType
			conflicts[i].ContentID = contentID
			conflicts[i].BaseRevisionID = baseRevision.ID
			conflicts[i].ConflictRevisionID = currentRevision.ID
			s.db.Create(&conflicts[i])
		}
		return nil, conflicts, nil
	}

	// Apply changes to current content
	var currentContent map[string]interface{}
	if err := json.Unmarshal(currentRevision.ContentSnapshot, &currentContent); err != nil {
		return nil, nil, fmt.Errorf("failed to parse current content: %w", err)
	}

	for key, value := range changes {
		currentContent[key] = value
	}

	// Serialize updated content
	snapshot, err := json.Marshal(currentContent)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to serialize content: %w", err)
	}

	// Determine status
	status := "pending"
	if autoApprove {
		status = "approved"
	}

	// Create new revision
	newRevision := model.Revision{
		ContentType:        contentType,
		ContentID:          contentID,
		VersionNumber:      currentRevision.VersionNumber + 1,
		PreviousRevisionID: &currentRevision.ID,
		ContentSnapshot:    snapshot,
		EditorID:           editorID,
		EditSummary:        editSummary,
		EditType:           "edit",
		Status:             status,
		IsCurrent:          autoApprove,
		CreatedAt:          time.Now(),
	}

	if err := s.db.Create(&newRevision).Error; err != nil {
		return nil, nil, fmt.Errorf("failed to create revision: %w", err)
	}

	// If auto-approved, mark previous as not current
	if autoApprove {
		s.db.Model(&currentRevision).Update("is_current", false)
	}

	return &newRevision, nil, nil
}

// DetectConflicts performs 3-way merge conflict detection
func (s *RevisionService) DetectConflicts(
	baseRevision *model.Revision,
	userChanges map[string]interface{},
	currentRevision *model.Revision,
) []model.EditConflict {
	var conflicts []model.EditConflict

	// Parse revisions
	var baseData map[string]interface{}
	var currentData map[string]interface{}

	json.Unmarshal(baseRevision.ContentSnapshot, &baseData)
	json.Unmarshal(currentRevision.ContentSnapshot, &currentData)

	// Check each changed field
	for field, userValue := range userChanges {
		baseValue := baseData[field]
		currentValue := currentData[field]

		// Convert to strings for comparison
		baseStr := fmt.Sprintf("%v", baseValue)
		userStr := fmt.Sprintf("%v", userValue)
		currentStr := fmt.Sprintf("%v", currentValue)

		// Case 1: Field unchanged by user → no conflict
		if userStr == baseStr {
			continue
		}

		// Case 2: Field changed by user, but current version has same value → no conflict
		if userStr == currentStr {
			continue
		}

		// Case 3: Field changed by user AND changed differently in current → CONFLICT
		if baseStr != currentStr && userStr != currentStr {
			conflict := model.EditConflict{
				FieldName: field,
				BaseValue: baseStr,
				Value1:    userStr,    // User's value
				Value2:    currentStr, // Current value
				Status:    "unresolved",
				CreatedAt: time.Now(),
			}
			conflicts = append(conflicts, conflict)
		}
	}

	return conflicts
}

// ApproveRevision approves a pending revision
func (s *RevisionService) ApproveRevision(
	revisionID uuid.UUID,
	reviewerID uuid.UUID,
	reviewNotes string,
) error {
	var revision model.Revision
	if err := s.db.First(&revision, "id = ?", revisionID).Error; err != nil {
		return fmt.Errorf("revision not found: %w", err)
	}

	if revision.Status != "pending" {
		return errors.New("only pending revisions can be approved")
	}

	now := time.Now()

	return s.db.Transaction(func(tx *gorm.DB) error {
		// Update revision
		if err := tx.Model(&revision).Updates(map[string]interface{}{
			"status":       "approved",
			"reviewer_id":  reviewerID,
			"reviewed_at":  now,
			"review_notes": reviewNotes,
			"is_current":   true,
		}).Error; err != nil {
			return err
		}

		// Mark previous current revision as not current
		if err := tx.Model(&model.Revision{}).
			Where("content_id = ? AND content_type = ? AND is_current = ? AND id != ?",
				revision.ContentID, revision.ContentType, true, revisionID).
			Update("is_current", false).Error; err != nil {
			return err
		}

		// Apply changes to actual Album/Song
		if err := s.applyRevisionToContent(tx, &revision); err != nil {
			return fmt.Errorf("failed to apply revision: %w", err)
		}

		return nil
	})
}

// RejectRevision rejects a pending revision
func (s *RevisionService) RejectRevision(
	revisionID uuid.UUID,
	reviewerID uuid.UUID,
	reviewNotes string,
) error {
	var revision model.Revision
	if err := s.db.First(&revision, "id = ?", revisionID).Error; err != nil {
		return fmt.Errorf("revision not found: %w", err)
	}

	if revision.Status != "pending" {
		return errors.New("only pending revisions can be rejected")
	}

	now := time.Now()

	return s.db.Model(&revision).Updates(map[string]interface{}{
		"status":       "rejected",
		"reviewer_id":  reviewerID,
		"reviewed_at":  now,
		"review_notes": reviewNotes,
	}).Error
}

// RevertToRevision creates a new revision with content from an older version
func (s *RevisionService) RevertToRevision(
	contentType string,
	contentID uuid.UUID,
	targetVersionNumber int,
	editorID uuid.UUID,
	editSummary string,
) (*model.Revision, error) {
	// Get target revision
	var targetRevision model.Revision
	if err := s.db.Where("content_id = ? AND content_type = ? AND version_number = ?",
		contentID, contentType, targetVersionNumber).
		First(&targetRevision).Error; err != nil {
		return nil, fmt.Errorf("target revision not found: %w", err)
	}

	// Get current revision
	var currentRevision model.Revision
	if err := s.db.Where("content_id = ? AND content_type = ? AND is_current = ?",
		contentID, contentType, true).
		Order("version_number DESC").
		First(&currentRevision).Error; err != nil {
		return nil, fmt.Errorf("current revision not found: %w", err)
	}

	// Create revert revision
	revertRevision := model.Revision{
		ContentType:        contentType,
		ContentID:          contentID,
		VersionNumber:      currentRevision.VersionNumber + 1,
		PreviousRevisionID: &currentRevision.ID,
		ContentSnapshot:    targetRevision.ContentSnapshot, // Use target's content
		EditorID:           editorID,
		EditSummary:        fmt.Sprintf("Reverted to version %d: %s", targetVersionNumber, editSummary),
		EditType:           "revert",
		Status:             "approved", // Auto-approve reverts
		IsCurrent:          true,
		CreatedAt:          time.Now(),
	}

	return &revertRevision, s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&revertRevision).Error; err != nil {
			return err
		}

		// Mark previous as not current
		tx.Model(&currentRevision).Update("is_current", false)

		// Apply to actual content
		return s.applyRevisionToContent(tx, &revertRevision)
	})
}

// applyRevisionToContent applies revision changes to the actual Album/Song record
func (s *RevisionService) applyRevisionToContent(tx *gorm.DB, revision *model.Revision) error {
	var content map[string]interface{}
	if err := json.Unmarshal(revision.ContentSnapshot, &content); err != nil {
		return err
	}

	switch revision.ContentType {
	case "album":
		return tx.Model(&model.Album{}).
			Where("id = ?", revision.ContentID).
			Updates(content).Error

	case "song":
		return tx.Model(&model.Song{}).
			Where("id = ?", revision.ContentID).
			Updates(content).Error

	case "artist":
		return tx.Model(&model.Artist{}).
			Where("id = ?", revision.ContentID).
			Updates(content).Error

	default:
		return fmt.Errorf("unsupported content type: %s", revision.ContentType)
	}
}

// GetRevisions returns revision history for a content
func (s *RevisionService) GetRevisions(
	contentType string,
	contentID uuid.UUID,
	limit int,
	offset int,
) ([]model.Revision, int64, error) {
	var revisions []model.Revision
	var total int64

	query := s.db.Where("content_id = ? AND content_type = ?", contentID, contentType)

	if err := query.Model(&model.Revision{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.
		Preload("Editor").
		Preload("Reviewer").
		Order("version_number DESC").
		Limit(limit).
		Offset(offset).
		Find(&revisions).Error; err != nil {
		return nil, 0, err
	}

	return revisions, total, nil
}

// GetRevisionDiff compares two revisions and returns the differences
func (s *RevisionService) GetRevisionDiff(
	contentType string,
	contentID uuid.UUID,
	version1 int,
	version2 int,
) (map[string]interface{}, error) {
	var rev1, rev2 model.Revision

	if err := s.db.Where("content_id = ? AND content_type = ? AND version_number = ?",
		contentID, contentType, version1).First(&rev1).Error; err != nil {
		return nil, fmt.Errorf("revision %d not found", version1)
	}

	if err := s.db.Where("content_id = ? AND content_type = ? AND version_number = ?",
		contentID, contentType, version2).First(&rev2).Error; err != nil {
		return nil, fmt.Errorf("revision %d not found", version2)
	}

	var data1, data2 map[string]interface{}
	json.Unmarshal(rev1.ContentSnapshot, &data1)
	json.Unmarshal(rev2.ContentSnapshot, &data2)

	diff := make(map[string]interface{})

	// Find changed fields
	for key, val2 := range data2 {
		val1 := data1[key]
		if fmt.Sprintf("%v", val1) != fmt.Sprintf("%v", val2) {
			diff[key] = map[string]interface{}{
				"from": val1,
				"to":   val2,
			}
		}
	}

	return diff, nil
}

// CreateAlbumSnapshot captures the current album state (with songs) as a new revision.
// This is the lightweight "append-only history" helper used after direct wiki edits.
func (s *RevisionService) CreateAlbumSnapshot(
	albumID uuid.UUID,
	editorID uuid.UUID,
	editSummary string,
	db *gorm.DB,
) error {
	var album model.Album
	if err := db.Preload("Artists").Preload("Songs").First(&album, "id = ?", albumID).Error; err != nil {
		return fmt.Errorf("album not found: %w", err)
	}

	type songSnap struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		TrackNumber int    `json:"track_number"`
		Lyrics      string `json:"lyrics"`
		AudioURL    string `json:"audio_url"`
	}
	type albumSnap struct {
		Album struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			AlbumType   string `json:"album_type"`
			EntryStatus string `json:"entry_status"`
		} `json:"album"`
		Songs []songSnap `json:"songs"`
	}

	snap := albumSnap{}
	snap.Album.ID = album.ID.String()
	snap.Album.Title = album.Title
	snap.Album.AlbumType = album.AlbumType
	snap.Album.EntryStatus = album.EntryStatus

	for _, song := range album.Songs {
		snap.Songs = append(snap.Songs, songSnap{
			ID:          song.ID.String(),
			Title:       song.Title,
			TrackNumber: song.TrackNumber,
			Lyrics:      song.Lyrics,
			AudioURL:    song.AudioURL,
		})
	}

	snapshot, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("failed to serialize snapshot: %w", err)
	}

	// Get current version number
	var latestRevision model.Revision
	versionNumber := 1
	if err := db.Where("content_id = ? AND content_type = ?", albumID, "album").
		Order("version_number DESC").
		First(&latestRevision).Error; err == nil {
		versionNumber = latestRevision.VersionNumber + 1
		// Mark old current as not-current
		db.Model(&latestRevision).Update("is_current", false)
	}

	prevID := &latestRevision.ID
	if versionNumber == 1 {
		prevID = nil
	}

	newRevision := model.Revision{
		ContentType:        "album",
		ContentID:          albumID,
		VersionNumber:      versionNumber,
		PreviousRevisionID: prevID,
		ContentSnapshot:    snapshot,
		EditorID:           editorID,
		EditSummary:        editSummary,
		EditType:           "edit",
		Status:             "approved",
		IsCurrent:          true,
		CreatedAt:          time.Now(),
	}

	return db.Create(&newRevision).Error
}
