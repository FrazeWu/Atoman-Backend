package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"atoman/internal/model"
)

// RevisionService handles revision-related operations
type RevisionService struct {
	db *gorm.DB
}

type albumRevisionSnapshot struct {
	Album *albumRevisionAlbum `json:"album"`
	Songs []albumRevisionSong `json:"songs"`
}

type albumRevisionAlbum struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	ReleaseDate string `json:"release_date"`
	AlbumType   string `json:"album_type"`
	EntryStatus string `json:"entry_status"`
	CoverURL    string `json:"cover_url"`
}

type albumRevisionSong struct {
	ID          string `json:"id,omitempty"`
	Title       string `json:"title"`
	TrackNumber int    `json:"track_number"`
	Lyrics      string `json:"lyrics"`
	AudioURL    string `json:"audio_url"`
	Status      string `json:"status"`
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
	var newRevision model.Revision
	var conflicts []model.EditConflict

	err := s.db.Transaction(func(tx *gorm.DB) error {
		// Get base revision
		var baseRevision model.Revision
		if err := tx.Where("content_id = ? AND content_type = ? AND version_number = ?",
			contentID, contentType, baseRevisionNumber).
			First(&baseRevision).Error; err != nil {
			return fmt.Errorf("base revision not found: %w", err)
		}

		// Get current revision
		var currentRevision model.Revision
		currentQuery := tx.Where("content_id = ? AND content_type = ? AND is_current = ?",
			contentID, contentType, true).
			Order("version_number DESC")
		if supportsRowLock(tx) {
			currentQuery = currentQuery.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := currentQuery.First(&currentRevision).Error; err != nil {
			return fmt.Errorf("current revision not found: %w", err)
		}

		// Detect conflicts if base != current
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
				if err := tx.Create(&conflicts[i]).Error; err != nil {
					return err
				}
			}
			return nil
		}

		// Apply changes to current content
		var currentContent map[string]interface{}
		if err := json.Unmarshal(currentRevision.ContentSnapshot, &currentContent); err != nil {
			return fmt.Errorf("failed to parse current content: %w", err)
		}

		for key, value := range changes {
			currentContent[key] = value
		}

		// Serialize updated content
		snapshot, err := json.Marshal(currentContent)
		if err != nil {
			return fmt.Errorf("failed to serialize content: %w", err)
		}

		// Determine status
		status := "pending"
		if autoApprove {
			status = "approved"
		}

		// Create new revision
		newRevision = model.Revision{
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

		// If auto-approved, make room for the partial unique current index first.
		if autoApprove {
			if err := tx.Model(&model.Revision{}).
				Where("content_id = ? AND content_type = ? AND is_current = ?",
					contentID, contentType, true).
				Update("is_current", false).Error; err != nil {
				return err
			}
		}

		if err := tx.Create(&newRevision).Error; err != nil {
			return fmt.Errorf("failed to create revision: %w", err)
		}
		if autoApprove {
			if err := s.applyRevisionToContent(tx, &newRevision); err != nil {
				return fmt.Errorf("failed to apply revision: %w", err)
			}
		}

		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if len(conflicts) > 0 {
		return nil, conflicts, nil
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
		// Mark previous current revision as not current
		if err := tx.Model(&model.Revision{}).
			Where("content_id = ? AND content_type = ? AND is_current = ? AND id != ?",
				revision.ContentID, revision.ContentType, true, revisionID).
			Update("is_current", false).Error; err != nil {
			return err
		}

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
	var revertRevision model.Revision

	return &revertRevision, s.db.Transaction(func(tx *gorm.DB) error {
		// Get target revision
		var targetRevision model.Revision
		if err := tx.Where("content_id = ? AND content_type = ? AND version_number = ?",
			contentID, contentType, targetVersionNumber).
			First(&targetRevision).Error; err != nil {
			return fmt.Errorf("target revision not found: %w", err)
		}

		// Get current revision
		var currentRevision model.Revision
		currentQuery := tx.Where("content_id = ? AND content_type = ? AND is_current = ?",
			contentID, contentType, true).
			Order("version_number DESC")
		if supportsRowLock(tx) {
			currentQuery = currentQuery.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := currentQuery.First(&currentRevision).Error; err != nil {
			return fmt.Errorf("current revision not found: %w", err)
		}

		// Create revert revision
		revertRevision = model.Revision{
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

		// Mark previous as not current
		if err := tx.Model(&model.Revision{}).
			Where("content_id = ? AND content_type = ? AND is_current = ?",
				contentID, contentType, true).
			Update("is_current", false).Error; err != nil {
			return err
		}

		if err := tx.Create(&revertRevision).Error; err != nil {
			return err
		}

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
		return s.applyAlbumRevisionSnapshot(tx, revision.ContentID, revision.ContentSnapshot)

	case "song":
		return applyFlatRevisionSnapshot(tx, &model.Song{}, revision.ContentID, revision.ContentType, content)

	case "artist":
		return applyFlatRevisionSnapshot(tx, &model.Artist{}, revision.ContentID, revision.ContentType, content)

	default:
		return fmt.Errorf("unsupported content type: %s", revision.ContentType)
	}
}

func applyFlatRevisionSnapshot(tx *gorm.DB, target any, contentID uuid.UUID, contentType string, content map[string]interface{}) error {
	result := tx.Model(target).Where("id = ?", contentID).Updates(content)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%s not found", contentType)
	}
	return nil
}

func (s *RevisionService) applyAlbumRevisionSnapshot(tx *gorm.DB, albumID uuid.UUID, raw []byte) error {
	var snapshot albumRevisionSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return fmt.Errorf("failed to parse album snapshot: %w", err)
	}
	if snapshot.Album == nil || snapshot.Songs == nil {
		return errors.New("album snapshot must contain album and songs")
	}

	var album model.Album
	if err := tx.First(&album, "id = ?", albumID).Error; err != nil {
		return err
	}

	if strings.TrimSpace(snapshot.Album.Title) != "" {
		album.Title = strings.TrimSpace(snapshot.Album.Title)
	}
	if strings.TrimSpace(snapshot.Album.AlbumType) != "" {
		album.AlbumType = strings.TrimSpace(snapshot.Album.AlbumType)
	}
	if strings.TrimSpace(snapshot.Album.EntryStatus) != "" {
		album.EntryStatus = strings.TrimSpace(snapshot.Album.EntryStatus)
	}
	album.CoverURL = strings.TrimSpace(snapshot.Album.CoverURL)
	if snapshot.Album.ReleaseDate != "" {
		releaseDate, err := time.Parse("2006-01-02", snapshot.Album.ReleaseDate)
		if err != nil {
			return fmt.Errorf("failed to parse album release date: %w", err)
		}
		album.ReleaseDate = releaseDate
		album.Year = releaseDate.Year()
	}

	if err := tx.Save(&album).Error; err != nil {
		return err
	}

	var existingSongs []model.Song
	if err := tx.Where("album_id = ?", albumID).Find(&existingSongs).Error; err != nil {
		return err
	}

	existingByID := make(map[string]*model.Song, len(existingSongs))
	for i := range existingSongs {
		existingByID[existingSongs[i].ID.String()] = &existingSongs[i]
	}

	seen := make(map[string]bool, len(snapshot.Songs))
	for _, songSnap := range snapshot.Songs {
		songID := strings.TrimSpace(songSnap.ID)
		if songID != "" {
			seen[songID] = true
		}

		title := strings.TrimSpace(songSnap.Title)
		if title == "" {
			continue
		}
		audioURL := strings.TrimSpace(songSnap.AudioURL)
		status := strings.TrimSpace(songSnap.Status)
		if status == "" {
			status = "open"
		}

		if songID != "" {
			if existingSong, ok := existingByID[songID]; ok {
				existingSong.Title = title
				existingSong.TrackNumber = songSnap.TrackNumber
				existingSong.Lyrics = songSnap.Lyrics
				existingSong.AudioURL = audioURL
				existingSong.Status = status
				existingSong.AlbumID = &albumID
				existingSong.ReleaseDate = album.ReleaseDate
				if err := tx.Save(existingSong).Error; err != nil {
					return err
				}
				continue
			}

			parsedID, err := uuid.Parse(songID)
			if err != nil {
				return fmt.Errorf("invalid song id in snapshot: %w", err)
			}
			newSong := model.Song{
				Base:        model.Base{ID: parsedID},
				Title:       title,
				TrackNumber: songSnap.TrackNumber,
				Lyrics:      songSnap.Lyrics,
				AudioURL:    audioURL,
				AudioSource: "s3",
				Status:      status,
				AlbumID:     &albumID,
				ReleaseDate: album.ReleaseDate,
				UploadedBy:  album.UploadedBy,
			}
			if err := tx.Create(&newSong).Error; err != nil {
				return err
			}
			continue
		}

		newSong := model.Song{
			Title:       title,
			TrackNumber: songSnap.TrackNumber,
			Lyrics:      songSnap.Lyrics,
			AudioURL:    audioURL,
			AudioSource: "s3",
			Status:      status,
			AlbumID:     &albumID,
			ReleaseDate: album.ReleaseDate,
			UploadedBy:  album.UploadedBy,
		}
		if err := tx.Create(&newSong).Error; err != nil {
			return err
		}
		seen[newSong.ID.String()] = true
	}

	for _, existingSong := range existingSongs {
		if seen[existingSong.ID.String()] {
			continue
		}
		if err := tx.Model(&model.Song{}).
			Where("id = ?", existingSong.ID).
			Update("status", "closed").Error; err != nil {
			return err
		}
	}

	return nil
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
	return db.Transaction(func(tx *gorm.DB) error {
		var album model.Album
		if err := tx.Preload("Artists").Preload("Songs").First(&album, "id = ?", albumID).Error; err != nil {
			return fmt.Errorf("album not found: %w", err)
		}

		snap := albumRevisionSnapshot{
			Album: &albumRevisionAlbum{},
			Songs: make([]albumRevisionSong, 0, len(album.Songs)),
		}
		snap.Album.ID = album.ID.String()
		snap.Album.Title = album.Title
		snap.Album.AlbumType = album.AlbumType
		snap.Album.EntryStatus = album.EntryStatus
		snap.Album.CoverURL = album.CoverURL
		if !album.ReleaseDate.IsZero() {
			snap.Album.ReleaseDate = album.ReleaseDate.Format("2006-01-02")
		}

		for _, song := range album.Songs {
			snap.Songs = append(snap.Songs, albumRevisionSong{
				ID:          song.ID.String(),
				Title:       song.Title,
				TrackNumber: song.TrackNumber,
				Lyrics:      song.Lyrics,
				AudioURL:    song.AudioURL,
				Status:      song.Status,
			})
		}

		snapshot, err := json.Marshal(snap)
		if err != nil {
			return fmt.Errorf("failed to serialize snapshot: %w", err)
		}

		// Get current version number
		var latestRevision model.Revision
		versionNumber := 1
		currentQuery := tx.Where("content_id = ? AND content_type = ? AND is_current = ?", albumID, "album", true).
			Order("version_number DESC")
		if supportsRowLock(tx) {
			currentQuery = currentQuery.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := currentQuery.First(&latestRevision).Error; err == nil {
			versionNumber = latestRevision.VersionNumber + 1
			// Mark old current as not-current
			if err := tx.Model(&model.Revision{}).
				Where("content_id = ? AND content_type = ? AND is_current = ?", albumID, "album", true).
				Update("is_current", false).Error; err != nil {
				return err
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
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

		return tx.Create(&newRevision).Error
	})
}

func supportsRowLock(db *gorm.DB) bool {
	return db.Dialector.Name() == "postgres"
}
