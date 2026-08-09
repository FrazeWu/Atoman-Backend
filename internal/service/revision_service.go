package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"atoman/internal/model"
	"atoman/internal/musiclyrics"
	"atoman/internal/platform/partialdate"
)

// RevisionService handles revision-related operations
type RevisionService struct {
	db *gorm.DB
}

type RevisionUserDTO struct {
	UUID        uuid.UUID `json:"uuid"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	AvatarURL   string    `json:"avatar_url"`
}

type RevisionDTO struct {
	ID                 uuid.UUID        `json:"id"`
	ContentType        string           `json:"content_type"`
	ContentID          uuid.UUID        `json:"content_id"`
	VersionNumber      int              `json:"version_number"`
	PreviousRevisionID *uuid.UUID       `json:"previous_revision_id,omitempty"`
	ContentSnapshot    json.RawMessage  `json:"content_snapshot" swaggertype:"object"`
	EditorID           uuid.UUID        `json:"editor_id"`
	Editor             *RevisionUserDTO `json:"editor,omitempty"`
	EditSummary        string           `json:"edit_summary"`
	EditType           string           `json:"edit_type"`
	Status             string           `json:"status"`
	ReviewerID         *uuid.UUID       `json:"reviewer_id,omitempty"`
	Reviewer           *RevisionUserDTO `json:"reviewer,omitempty"`
	ReviewedAt         *time.Time       `json:"reviewed_at,omitempty"`
	ReviewNotes        string           `json:"review_notes,omitempty"`
	IsCurrent          bool             `json:"is_current"`
	CreatedAt          time.Time        `json:"created_at"`
}

type RevisionContributorDTO struct {
	UserID            uuid.UUID `json:"user_id" gorm:"column:user_id"`
	Username          string    `json:"username"`
	DisplayName       string    `json:"display_name" gorm:"column:display_name"`
	AvatarURL         string    `json:"avatar_url" gorm:"column:avatar_url"`
	RevisionCount     int64     `json:"revision_count" gorm:"column:revision_count"`
	LastContributedAt string    `json:"last_contributed_at" gorm:"column:last_contributed_at"`
}

type albumRevisionSnapshot struct {
	Album         *albumRevisionAlbum   `json:"album"`
	ArtistCredits []albumRevisionCredit `json:"artist_credits"`
	Songs         []albumRevisionSong   `json:"songs"`
}

type albumRevisionAlbum struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	ReleaseDate string `json:"release_date"`
	AlbumType   string `json:"album_type"`
	EntryStatus string `json:"entry_status"`
	CoverURL    string `json:"cover_url"`
	CoverSource string `json:"cover_source"`
}

type albumRevisionCredit struct {
	ArtistID   string `json:"artist_id"`
	Role       string `json:"role"`
	CustomRole string `json:"custom_role,omitempty"`
	Position   int    `json:"position"`
}

type albumRevisionSong struct {
	ID            string                `json:"id,omitempty"`
	Title         string                `json:"title"`
	TrackNumber   int                   `json:"track_number"`
	DiscNumber    int                   `json:"disc_number"`
	Lyrics        string                `json:"lyrics"`
	AudioURL      string                `json:"audio_url"`
	CoverURL      string                `json:"cover_url"`
	Status        string                `json:"status"`
	ArtistCredits []albumRevisionCredit `json:"artist_credits"`
}

type songRevisionSnapshot struct {
	ID            string                `json:"id"`
	Title         string                `json:"title"`
	TrackNumber   int                   `json:"track_number"`
	DiscNumber    int                   `json:"disc_number"`
	Lyrics        string                `json:"lyrics"`
	AudioURL      string                `json:"audio_url"`
	CoverURL      string                `json:"cover_url"`
	Status        string                `json:"status"`
	ArtistCredits []albumRevisionCredit `json:"artist_credits"`
}

func NewRevisionService(db *gorm.DB) *RevisionService {
	return &RevisionService{db: db}
}

// GetDB returns the database instance
func (s *RevisionService) GetDB() *gorm.DB {
	return s.db
}

// EnsureInitialRevision creates the approved v1 snapshot once for newly created content.
func (s *RevisionService) EnsureInitialRevision(contentType string, contentID, editorID uuid.UUID) (*model.Revision, error) {
	var revision model.Revision
	result := s.db.Where("content_id = ? AND content_type = ?", contentID, contentType).
		Order("version_number ASC").Limit(1).Find(&revision)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected > 0 {
		return &revision, nil
	}
	return s.createBaselineRevision(s.db, contentType, contentID, editorID)
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
		var currentRevision model.Revision
		currentQuery := tx.Where("content_id = ? AND content_type = ? AND is_current = ?",
			contentID, contentType, true).
			Order("version_number DESC")
		if supportsRowLock(tx) {
			currentQuery = currentQuery.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := currentQuery.First(&currentRevision).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			baseline, err := s.createBaselineRevision(tx, contentType, contentID, editorID)
			if err != nil {
				return err
			}
			currentRevision = *baseline
		} else if err != nil {
			return fmt.Errorf("current revision not found: %w", err)
		}

		if baseRevisionNumber <= 0 {
			baseRevisionNumber = currentRevision.VersionNumber
		}
		var baseRevision model.Revision
		if err := tx.Where("content_id = ? AND content_type = ? AND version_number = ?",
			contentID, contentType, baseRevisionNumber).
			First(&baseRevision).Error; err != nil {
			return fmt.Errorf("base revision not found: %w", err)
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

		snapshot, err := mergeRevisionChanges(contentType, currentRevision.ContentSnapshot, changes)
		if err != nil {
			return err
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

func (s *RevisionService) createBaselineRevision(tx *gorm.DB, contentType string, contentID, editorID uuid.UUID) (*model.Revision, error) {
	snapshot, err := s.captureCurrentSnapshot(tx, contentType, contentID)
	if err != nil {
		return nil, err
	}
	createdAt, err := revisionContentCreatedAt(tx, contentType, contentID)
	if err != nil {
		return nil, err
	}
	baseline := model.Revision{
		ContentType:     contentType,
		ContentID:       contentID,
		VersionNumber:   1,
		ContentSnapshot: snapshot,
		EditorID:        editorID,
		EditSummary:     "初始版本",
		EditType:        "creation",
		Status:          "approved",
		IsCurrent:       true,
		CreatedAt:       createdAt,
	}
	if err := tx.Create(&baseline).Error; err != nil {
		return nil, fmt.Errorf("failed to create baseline revision: %w", err)
	}
	return &baseline, nil
}

func revisionContentCreatedAt(tx *gorm.DB, contentType string, contentID uuid.UUID) (time.Time, error) {
	var createdAt time.Time
	var query *gorm.DB
	switch contentType {
	case "artist":
		query = tx.Model(&model.Artist{})
	case "album":
		query = tx.Model(&model.Album{})
	case "song":
		query = tx.Model(&model.Song{})
	default:
		return time.Time{}, fmt.Errorf("cannot find creation time for content type: %s", contentType)
	}
	if err := query.Select("created_at").Where("id = ?", contentID).Scan(&createdAt).Error; err != nil {
		return time.Time{}, err
	}
	return createdAt, nil
}

func (s *RevisionService) captureCurrentSnapshot(tx *gorm.DB, contentType string, contentID uuid.UUID) ([]byte, error) {
	switch contentType {
	case "album":
		var album model.Album
		if err := tx.Preload("ArtistCredits").Preload("Songs.ArtistCredits").First(&album, "id = ?", contentID).Error; err != nil {
			return nil, fmt.Errorf("album not found: %w", err)
		}
		snapshot := albumRevisionSnapshot{
			Album: &albumRevisionAlbum{
				ID:          album.ID.String(),
				Title:       album.Title,
				Description: album.Description,
				AlbumType:   album.AlbumType,
				EntryStatus: album.EntryStatus,
				CoverURL:    album.CoverURL,
				CoverSource: album.CoverSource,
			},
			ArtistCredits: make([]albumRevisionCredit, 0, len(album.ArtistCredits)),
			Songs:         make([]albumRevisionSong, 0, len(album.Songs)),
		}
		snapshot.Album.ReleaseDate = partialdate.Format(album.ReleaseDate, album.ReleaseDatePrecision)
		for _, credit := range album.ArtistCredits {
			snapshot.ArtistCredits = append(snapshot.ArtistCredits, albumRevisionCredit{
				ArtistID: credit.ArtistID.String(), CustomRole: credit.CustomRole,
				Role: credit.Role, Position: credit.Position,
			})
		}
		for _, song := range album.Songs {
			if song.Status == "closed" {
				continue
			}
			snapshot.Songs = append(snapshot.Songs, albumRevisionSong{
				ID: song.ID.String(), Title: song.Title, TrackNumber: song.TrackNumber,
				DiscNumber: song.DiscNumber, Lyrics: song.Lyrics, AudioURL: song.AudioURL,
				CoverURL: song.CoverURL, Status: song.Status,
				ArtistCredits: songCreditsSnapshot(song.ArtistCredits),
			})
		}
		return json.Marshal(snapshot)
	case "song":
		var song model.Song
		if err := tx.Preload("ArtistCredits").First(&song, "id = ?", contentID).Error; err != nil {
			return nil, fmt.Errorf("song not found: %w", err)
		}
		return json.Marshal(songRevisionSnapshot{
			ID: song.ID.String(), Title: song.Title, TrackNumber: song.TrackNumber,
			DiscNumber: song.DiscNumber, Lyrics: song.Lyrics, AudioURL: song.AudioURL,
			CoverURL: song.CoverURL, Status: song.Status,
			ArtistCredits: songCreditsSnapshot(song.ArtistCredits),
		})
	case "artist":
		var artist model.Artist
		if err := tx.First(&artist, "id = ?", contentID).Error; err != nil {
			return nil, fmt.Errorf("artist not found: %w", err)
		}
		snapshot := map[string]interface{}{
			"name": artist.Name, "legal_name": artist.LegalName, "stage_names_json": artist.StageNamesJSON,
			"bio": artist.Bio, "image_url": artist.ImageURL, "nationality": artist.Nationality,
			"birth_place": artist.BirthPlace, "birth_year": artist.BirthYear, "death_year": artist.DeathYear,
			"birth_date": "", "artist_form": artist.ArtistForm, "entry_status": artist.EntryStatus,
		}
		if artist.BirthDate != nil {
			snapshot["birth_date"] = partialdate.Format(*artist.BirthDate, artist.BirthDatePrecision)
		}
		return json.Marshal(snapshot)
	default:
		return nil, fmt.Errorf("cannot create baseline for content type: %s", contentType)
	}
}

func songCreditsSnapshot(credits []model.SongArtist) []albumRevisionCredit {
	result := make([]albumRevisionCredit, 0, len(credits))
	for _, credit := range credits {
		result = append(result, albumRevisionCredit{
			ArtistID: credit.ArtistID.String(), Role: credit.Role,
			CustomRole: credit.CustomRole, Position: credit.Position,
		})
	}
	return result
}

type albumRevisionRoleInput struct {
	Role  string `json:"role"`
	Label string `json:"label"`
}

type albumRevisionCreditInput struct {
	ArtistID string                   `json:"artist_id"`
	Roles    []albumRevisionRoleInput `json:"roles"`
	Position int                      `json:"position"`
}

var revisionArtistRoles = map[string]struct{}{
	"primary": {}, "featured": {}, "vocals": {}, "backing_vocals": {},
	"writer": {}, "composer": {}, "arranger": {}, "producer": {},
	"vocal_producer": {}, "recording_engineer": {}, "mixing_engineer": {},
	"mastering_engineer": {}, "remixer": {}, "custom": {},
}

type albumRevisionTrackInput struct {
	ID            string                     `json:"id"`
	Title         string                     `json:"title"`
	TrackNumber   int                        `json:"track_number"`
	DiscNumber    int                        `json:"disc_number"`
	Lyrics        string                     `json:"lyrics"`
	AudioURL      string                     `json:"audio_url"`
	CoverURL      string                     `json:"cover_url"`
	ArtistCredits []albumRevisionCreditInput `json:"artist_credits"`
	Removed       bool                       `json:"removed"`
}

type songRevisionChanges struct {
	Title         *string                     `json:"title"`
	TrackNumber   *int                        `json:"track_number"`
	DiscNumber    *int                        `json:"disc_number"`
	Lyrics        *string                     `json:"lyrics"`
	AudioURL      *string                     `json:"audio_url"`
	CoverURL      *string                     `json:"cover_url"`
	ArtistCredits *[]albumRevisionCreditInput `json:"artist_credits"`
}

type albumRevisionChanges struct {
	Title         *string                     `json:"title"`
	Description   *string                     `json:"description"`
	ReleaseDate   *string                     `json:"release_date"`
	AlbumType     *string                     `json:"album_type"`
	CoverURL      *string                     `json:"cover_url"`
	ArtistIDs     *[]string                   `json:"artist_ids"`
	ArtistCredits *[]albumRevisionCreditInput `json:"artist_credits"`
	Tracks        *[]albumRevisionTrackInput  `json:"tracks"`
}

func mergeRevisionChanges(contentType string, current []byte, changes map[string]interface{}) ([]byte, error) {
	if contentType == "artist" {
		var snapshot map[string]interface{}
		if err := json.Unmarshal(current, &snapshot); err != nil {
			return nil, fmt.Errorf("failed to parse current content: %w", err)
		}
		allowed := map[string]struct{}{
			"name": {}, "legal_name": {}, "stage_names_json": {}, "bio": {}, "image_url": {},
			"nationality": {}, "birth_place": {}, "birth_date": {}, "birth_year": {},
			"death_year": {}, "artist_form": {},
		}
		for key, value := range changes {
			if _, ok := allowed[key]; !ok {
				return nil, fmt.Errorf("unsupported artist revision field: %s", key)
			}
			snapshot[key] = value
		}
		return json.Marshal(snapshot)
	}
	if contentType == "song" {
		return mergeSongRevisionChanges(current, changes)
	}
	if contentType != "album" {
		return nil, fmt.Errorf("unsupported revision content type: %s", contentType)
	}
	allowed := map[string]struct{}{
		"title": {}, "description": {}, "release_date": {}, "album_type": {},
		"cover_url": {}, "cover_key": {}, "artist_ids": {}, "artist_credits": {}, "tracks": {},
	}
	for key := range changes {
		if _, ok := allowed[key]; !ok {
			return nil, fmt.Errorf("unsupported album revision field: %s", key)
		}
	}

	var snapshot albumRevisionSnapshot
	if err := json.Unmarshal(current, &snapshot); err != nil {
		return nil, fmt.Errorf("failed to parse album snapshot: %w", err)
	}
	if snapshot.Album == nil || snapshot.Songs == nil {
		return nil, errors.New("album snapshot must contain album and songs")
	}
	rawChanges, err := json.Marshal(changes)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize album changes: %w", err)
	}
	var input albumRevisionChanges
	if err := json.Unmarshal(rawChanges, &input); err != nil {
		return nil, fmt.Errorf("failed to parse album changes: %w", err)
	}
	if input.Title != nil {
		snapshot.Album.Title = *input.Title
	}
	if input.Description != nil {
		snapshot.Album.Description = *input.Description
	}
	if input.ReleaseDate != nil {
		snapshot.Album.ReleaseDate = *input.ReleaseDate
	}
	if input.AlbumType != nil {
		snapshot.Album.AlbumType = *input.AlbumType
	}
	if input.CoverURL != nil {
		snapshot.Album.CoverURL = *input.CoverURL
		snapshot.Album.CoverSource = coverSourceForRevision(*input.CoverURL)
	}
	if input.ArtistCredits == nil && input.ArtistIDs != nil {
		credits := make([]albumRevisionCredit, 0, len(*input.ArtistIDs))
		for index, rawArtistID := range *input.ArtistIDs {
			artistID := strings.TrimSpace(rawArtistID)
			if _, err := uuid.Parse(artistID); err != nil {
				return nil, errors.New("artist_ids must contain valid UUID values")
			}
			credits = append(credits, albumRevisionCredit{
				ArtistID: artistID, Role: "primary", Position: index + 1,
			})
		}
		snapshot.ArtistCredits = credits
	}
	if input.ArtistCredits != nil {
		if err := validateRevisionCredits(*input.ArtistCredits, true); err != nil {
			return nil, err
		}
		credits := make([]albumRevisionCredit, 0)
		for index, credit := range *input.ArtistCredits {
			if _, err := uuid.Parse(strings.TrimSpace(credit.ArtistID)); err != nil {
				return nil, errors.New("artist_credits must contain valid artist_id values")
			}
			position := credit.Position
			if position <= 0 {
				position = index + 1
			}
			for _, role := range credit.Roles {
				roleName := strings.ToLower(strings.TrimSpace(role.Role))
				if roleName == "" {
					return nil, errors.New("artist credit role is required")
				}
				credits = append(credits, albumRevisionCredit{
					ArtistID: credit.ArtistID, Role: roleName,
					CustomRole: strings.TrimSpace(role.Label), Position: position,
				})
			}
		}
		snapshot.ArtistCredits = credits
	}
	if input.Tracks != nil {
		songs := make([]albumRevisionSong, 0, len(*input.Tracks))
		for _, track := range *input.Tracks {
			if track.Removed {
				continue
			}
			if err := validateRevisionCredits(track.ArtistCredits, false); err != nil {
				return nil, err
			}
			songs = append(songs, albumRevisionSong{
				ID: track.ID, Title: track.Title, TrackNumber: track.TrackNumber,
				DiscNumber: track.DiscNumber, Lyrics: track.Lyrics, AudioURL: track.AudioURL,
				CoverURL: track.CoverURL, Status: "open",
				ArtistCredits: revisionCreditsFromInput(track.ArtistCredits),
			})
		}
		snapshot.Songs = songs
	}
	return json.Marshal(snapshot)
}

func mergeSongRevisionChanges(current []byte, changes map[string]interface{}) ([]byte, error) {
	allowed := map[string]struct{}{
		"title": {}, "track_number": {}, "disc_number": {}, "lyrics": {},
		"audio_url": {}, "cover_url": {}, "artist_credits": {},
	}
	for key := range changes {
		if _, ok := allowed[key]; !ok {
			return nil, fmt.Errorf("unsupported song revision field: %s", key)
		}
	}
	var snapshot songRevisionSnapshot
	if err := json.Unmarshal(current, &snapshot); err != nil {
		return nil, fmt.Errorf("failed to parse song snapshot: %w", err)
	}
	raw, err := json.Marshal(changes)
	if err != nil {
		return nil, err
	}
	var input songRevisionChanges
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, err
	}
	if input.Title != nil {
		snapshot.Title = strings.TrimSpace(*input.Title)
	}
	if input.TrackNumber != nil {
		snapshot.TrackNumber = *input.TrackNumber
	}
	if input.DiscNumber != nil {
		snapshot.DiscNumber = *input.DiscNumber
	}
	if input.Lyrics != nil {
		snapshot.Lyrics = *input.Lyrics
	}
	if input.AudioURL != nil {
		snapshot.AudioURL = strings.TrimSpace(*input.AudioURL)
	}
	if input.CoverURL != nil {
		snapshot.CoverURL = strings.TrimSpace(*input.CoverURL)
	}
	if input.ArtistCredits != nil {
		if err := validateRevisionCredits(*input.ArtistCredits, false); err != nil {
			return nil, err
		}
		snapshot.ArtistCredits = revisionCreditsFromInput(*input.ArtistCredits)
	}
	if snapshot.Title == "" {
		return nil, errors.New("song title is required")
	}
	return json.Marshal(snapshot)
}

func revisionCreditsFromInput(input []albumRevisionCreditInput) []albumRevisionCredit {
	credits := make([]albumRevisionCredit, 0)
	for index, credit := range input {
		position := credit.Position
		if position <= 0 {
			position = index + 1
		}
		for _, role := range credit.Roles {
			credits = append(credits, albumRevisionCredit{
				ArtistID: strings.TrimSpace(credit.ArtistID), Role: strings.ToLower(strings.TrimSpace(role.Role)),
				CustomRole: strings.TrimSpace(role.Label), Position: position,
			})
		}
	}
	return credits
}

func validateRevisionCredits(input []albumRevisionCreditInput, requirePrimary bool) error {
	if requirePrimary && len(input) == 0 {
		return errors.New("artist_credits are required")
	}
	seen := make(map[string]struct{})
	hasPrimary := false
	for _, credit := range input {
		artistID := strings.TrimSpace(credit.ArtistID)
		if _, err := uuid.Parse(artistID); err != nil {
			return errors.New("artist_credits must contain valid artist_id values")
		}
		if len(credit.Roles) == 0 {
			return errors.New("each artist credit must contain at least one role")
		}
		for _, inputRole := range credit.Roles {
			role := strings.ToLower(strings.TrimSpace(inputRole.Role))
			if _, ok := revisionArtistRoles[role]; !ok {
				return errors.New("artist credit role is not supported")
			}
			hasPrimary = hasPrimary || role == "primary"
			label := strings.TrimSpace(inputRole.Label)
			if role == "custom" && label == "" {
				return errors.New("custom artist roles require a label")
			}
			key := artistID + "\x00" + role + "\x00" + strings.ToLower(label)
			if _, exists := seen[key]; exists {
				return errors.New("duplicate artist role")
			}
			seen[key] = struct{}{}
		}
	}
	if requirePrimary && !hasPrimary {
		return errors.New("at least one primary artist is required")
	}
	return nil
}

func coverSourceForRevision(url string) string {
	trimmed := strings.TrimSpace(url)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "/uploads/") || strings.HasPrefix(trimmed, "uploads/") {
		return "local"
	}
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		publicUploadsBase := strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_UPLOADS_BASE_URL")), "/")
		if publicUploadsBase != "" && (trimmed == publicUploadsBase || strings.HasPrefix(trimmed, publicUploadsBase+"/")) {
			return "local"
		}
		s3Prefix := strings.TrimRight(strings.TrimSpace(os.Getenv("S3_URL_PREFIX")), "/")
		if s3Prefix != "" && (trimmed == s3Prefix || strings.HasPrefix(trimmed, s3Prefix+"/")) {
			return "s3"
		}
		return "external"
	}
	if strings.HasPrefix(trimmed, "s3/") || strings.TrimSpace(os.Getenv("STORAGE_TYPE")) == "s3" {
		return "s3"
	}
	return "local"
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
	var userData map[string]interface{}
	if baseRevision.ContentType == "album" {
		if merged, err := mergeRevisionChanges("album", baseRevision.ContentSnapshot, userChanges); err == nil {
			_ = json.Unmarshal(merged, &userData)
		}
	}

	// Check each changed field
	for field, userValue := range userChanges {
		baseValue := revisionConflictValue(baseData, baseRevision.ContentType, field)
		currentValue := revisionConflictValue(currentData, baseRevision.ContentType, field)
		if baseRevision.ContentType == "album" {
			userValue = revisionConflictValue(userData, baseRevision.ContentType, field)
		}

		// Case 1: Field unchanged by user → no conflict
		if reflect.DeepEqual(userValue, baseValue) {
			continue
		}

		// Case 2: Field changed by user, but current version has same value → no conflict
		if reflect.DeepEqual(userValue, currentValue) {
			continue
		}

		// Case 3: Field changed by user AND changed differently in current → CONFLICT
		if !reflect.DeepEqual(baseValue, currentValue) {
			conflict := model.EditConflict{
				FieldName: field,
				BaseValue: fmt.Sprintf("%v", baseValue),
				Value1:    fmt.Sprintf("%v", userValue),
				Value2:    fmt.Sprintf("%v", currentValue),
				Status:    "unresolved",
				CreatedAt: time.Now(),
			}
			conflicts = append(conflicts, conflict)
		}
	}

	return conflicts
}

func revisionConflictValue(data map[string]interface{}, contentType, field string) interface{} {
	if contentType != "album" {
		return data[field]
	}
	switch field {
	case "artist_ids", "artist_credits":
		return data["artist_credits"]
	case "tracks":
		return data["songs"]
	case "cover_key":
		return nil
	default:
		album, _ := data["album"].(map[string]interface{})
		return album[field]
	}
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

func (s *RevisionService) ApplySongAudioReplacement(jobID uuid.UUID) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var job model.SongAudioReplacement
		query := tx.Where("id = ? AND status = ?", jobID, "processing")
		if supportsRowLock(tx) {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.First(&job).Error; err != nil {
			return err
		}

		var current model.Revision
		currentQuery := tx.Where("content_id = ? AND content_type = ? AND is_current = ?", job.SongID, "song", true).Order("version_number DESC")
		if supportsRowLock(tx) {
			currentQuery = currentQuery.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := currentQuery.First(&current).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			baseline, baselineErr := s.createBaselineRevision(tx, "song", job.SongID, job.RequestedBy)
			if baselineErr != nil {
				return baselineErr
			}
			current = *baseline
		} else if err != nil {
			return err
		}

		var snapshot songRevisionSnapshot
		if err := json.Unmarshal(current.ContentSnapshot, &snapshot); err != nil {
			return err
		}
		if strings.TrimSpace(job.AudioURL) == "" {
			return errors.New("replacement audio URL is required")
		}
		snapshot.AudioURL = strings.TrimSpace(job.AudioURL)
		raw, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		if err := tx.Model(&model.Revision{}).Where("content_id = ? AND content_type = ? AND is_current = ?", job.SongID, "song", true).Update("is_current", false).Error; err != nil {
			return err
		}
		revision := model.Revision{
			ContentType: "song", ContentID: job.SongID, VersionNumber: current.VersionNumber + 1,
			PreviousRevisionID: &current.ID, ContentSnapshot: raw, EditorID: job.RequestedBy,
			EditSummary: "替换歌曲音频", EditType: "audio_replace", Status: "approved", IsCurrent: true, CreatedAt: time.Now(),
		}
		if err := tx.Create(&revision).Error; err != nil {
			return err
		}
		if err := applySongRevisionSnapshot(tx, job.SongID, job.RequestedBy, raw); err != nil {
			return err
		}
		now := time.Now().UTC()
		return tx.Model(&model.SongAudioReplacement{}).Where("id = ?", job.ID).Updates(map[string]any{
			"status": "completed", "revision_id": revision.ID, "finished_at": now, "error_message": "",
		}).Error
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
		return s.applyAlbumRevisionSnapshot(tx, revision.ContentID, revision.EditorID, revision.ContentSnapshot)

	case "song":
		return applySongRevisionSnapshot(tx, revision.ContentID, revision.EditorID, revision.ContentSnapshot)

	case "artist":
		return applyArtistRevisionSnapshot(tx, revision.ContentID, content)

	default:
		return fmt.Errorf("unsupported content type: %s", revision.ContentType)
	}
}

func applyArtistRevisionSnapshot(tx *gorm.DB, artistID uuid.UUID, content map[string]interface{}) error {
	allowed := map[string]struct{}{
		"name": {}, "legal_name": {}, "stage_names_json": {}, "bio": {}, "image_url": {},
		"nationality": {}, "birth_place": {}, "birth_year": {}, "death_year": {},
		"artist_form": {}, "entry_status": {},
	}
	updates := make(map[string]interface{})
	for key, value := range content {
		if _, ok := allowed[key]; ok {
			updates[key] = value
		}
	}
	if value, ok := content["birth_date"].(string); ok {
		if strings.TrimSpace(value) == "" {
			updates["birth_date"] = nil
			updates["birth_date_precision"] = ""
		} else {
			parsed, precision, err := partialdate.Parse(value)
			if err != nil {
				return fmt.Errorf("failed to parse artist birth date: %w", err)
			}
			updates["birth_date"] = parsed
			updates["birth_date_precision"] = precision
			updates["birth_year"] = parsed.Year()
		}
	}
	result := tx.Model(&model.Artist{}).Where("id = ?", artistID).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("artist not found")
	}
	return nil
}

func applySongRevisionSnapshot(tx *gorm.DB, songID, actorID uuid.UUID, raw []byte) error {
	var snapshot songRevisionSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return err
	}
	if strings.TrimSpace(snapshot.Title) == "" {
		return errors.New("song title is required")
	}
	updates := map[string]interface{}{
		"title": strings.TrimSpace(snapshot.Title), "track_number": snapshot.TrackNumber,
		"disc_number": snapshot.DiscNumber, "lyrics": snapshot.Lyrics,
		"audio_url": strings.TrimSpace(snapshot.AudioURL), "cover_url": strings.TrimSpace(snapshot.CoverURL),
	}
	if strings.TrimSpace(snapshot.AudioURL) != "" {
		updates["audio_source"] = coverSourceForRevision(snapshot.AudioURL)
	}
	if strings.TrimSpace(snapshot.CoverURL) != "" {
		updates["cover_source"] = coverSourceForRevision(snapshot.CoverURL)
	}
	result := tx.Model(&model.Song{}).Where("id = ?", songID).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("song not found")
	}
	if snapshot.ArtistCredits != nil {
		if err := replaceSongArtistCredits(tx, songID, snapshot.ArtistCredits); err != nil {
			return err
		}
	}
	return musiclyrics.SyncLegacySongLyrics(tx, actorID, songID, snapshot.Lyrics, "通过歌曲版本更新歌词")
}

func replaceSongArtistCredits(tx *gorm.DB, songID uuid.UUID, credits []albumRevisionCredit) error {
	rows := make([]model.SongArtist, 0, len(credits))
	for _, credit := range credits {
		artistID, err := uuid.Parse(strings.TrimSpace(credit.ArtistID))
		if err != nil {
			return errors.New("song snapshot contains an invalid artist credit")
		}
		role := strings.ToLower(strings.TrimSpace(credit.Role))
		if role == "" {
			return errors.New("song snapshot contains an empty artist role")
		}
		rows = append(rows, model.SongArtist{SongID: songID, ArtistID: artistID, Role: role, CustomRole: strings.TrimSpace(credit.CustomRole), Position: credit.Position})
	}
	if err := tx.Where("song_id = ?", songID).Delete(&model.SongArtist{}).Error; err != nil {
		return err
	}
	if len(rows) > 0 {
		return tx.Create(&rows).Error
	}
	return nil
}

func (s *RevisionService) applyAlbumRevisionSnapshot(tx *gorm.DB, albumID, actorID uuid.UUID, raw []byte) error {
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
	album.Description = snapshot.Album.Description
	album.CoverURL = strings.TrimSpace(snapshot.Album.CoverURL)
	album.CoverSource = strings.TrimSpace(snapshot.Album.CoverSource)
	if snapshot.Album.ReleaseDate != "" {
		releaseDate, precision, err := partialdate.Parse(snapshot.Album.ReleaseDate)
		if err != nil {
			return fmt.Errorf("failed to parse album release date: %w", err)
		}
		album.ReleaseDate = *releaseDate
		album.ReleaseDatePrecision = precision
		album.Year = releaseDate.Year()
		album.ReleaseYear = releaseDate.Year()
	} else {
		album.ReleaseDate = time.Time{}
		album.ReleaseDatePrecision = ""
		album.Year = 0
		album.ReleaseYear = 0
	}

	if err := tx.Save(&album).Error; err != nil {
		return err
	}
	if err := tx.Model(&model.Song{}).Where("album_id = ?", album.ID).Updates(map[string]any{
		"release_date": album.ReleaseDate, "release_date_precision": album.ReleaseDatePrecision,
	}).Error; err != nil {
		return err
	}
	if snapshot.ArtistCredits != nil {
		rows := make([]model.AlbumArtist, 0, len(snapshot.ArtistCredits))
		hasPrimary := false
		for _, credit := range snapshot.ArtistCredits {
			artistID, err := uuid.Parse(strings.TrimSpace(credit.ArtistID))
			if err != nil {
				return errors.New("album snapshot contains an invalid artist credit")
			}
			role := strings.ToLower(strings.TrimSpace(credit.Role))
			if role == "" {
				return errors.New("album snapshot contains an empty artist role")
			}
			hasPrimary = hasPrimary || role == "primary"
			rows = append(rows, model.AlbumArtist{
				AlbumID: albumID, ArtistID: artistID, Role: role,
				CustomRole: strings.TrimSpace(credit.CustomRole), Position: credit.Position,
			})
		}
		if len(rows) > 0 && !hasPrimary {
			return errors.New("album snapshot must contain a primary artist")
		}
		if err := tx.Where("album_id = ?", albumID).Delete(&model.AlbumArtist{}).Error; err != nil {
			return err
		}
		if len(rows) > 0 {
			if err := tx.Create(&rows).Error; err != nil {
				return err
			}
		}
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
				existingSong.DiscNumber = songSnap.DiscNumber
				existingSong.Lyrics = songSnap.Lyrics
				existingSong.AudioURL = audioURL
				existingSong.CoverURL = songSnap.CoverURL
				existingSong.AudioSource = coverSourceForRevision(audioURL)
				existingSong.Status = status
				existingSong.AlbumID = &albumID
				existingSong.ReleaseDate = album.ReleaseDate
				if err := tx.Save(existingSong).Error; err != nil {
					return err
				}
				if songSnap.ArtistCredits != nil {
					if err := replaceSongArtistCredits(tx, existingSong.ID, songSnap.ArtistCredits); err != nil {
						return err
					}
				}
				if err := musiclyrics.SyncLegacySongLyrics(tx, actorID, existingSong.ID, songSnap.Lyrics, "通过专辑版本更新歌词"); err != nil {
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
				DiscNumber:  songSnap.DiscNumber,
				Lyrics:      songSnap.Lyrics,
				AudioURL:    audioURL,
				CoverURL:    songSnap.CoverURL,
				AudioSource: coverSourceForRevision(audioURL),
				Status:      status,
				AlbumID:     &albumID,
				ReleaseDate: album.ReleaseDate,
				UploadedBy:  album.UploadedBy,
			}
			if err := tx.Create(&newSong).Error; err != nil {
				return err
			}
			if songSnap.ArtistCredits != nil {
				if err := replaceSongArtistCredits(tx, newSong.ID, songSnap.ArtistCredits); err != nil {
					return err
				}
			}
			if err := musiclyrics.SyncLegacySongLyrics(tx, actorID, newSong.ID, songSnap.Lyrics, "通过专辑版本更新歌词"); err != nil {
				return err
			}
			continue
		}

		newSong := model.Song{
			Title:       title,
			TrackNumber: songSnap.TrackNumber,
			DiscNumber:  songSnap.DiscNumber,
			Lyrics:      songSnap.Lyrics,
			AudioURL:    audioURL,
			CoverURL:    songSnap.CoverURL,
			AudioSource: coverSourceForRevision(audioURL),
			Status:      status,
			AlbumID:     &albumID,
			ReleaseDate: album.ReleaseDate,
			UploadedBy:  album.UploadedBy,
		}
		if err := tx.Create(&newSong).Error; err != nil {
			return err
		}
		if songSnap.ArtistCredits != nil {
			if err := replaceSongArtistCredits(tx, newSong.ID, songSnap.ArtistCredits); err != nil {
				return err
			}
		}
		if err := musiclyrics.SyncLegacySongLyrics(tx, actorID, newSong.ID, songSnap.Lyrics, "通过专辑版本更新歌词"); err != nil {
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
) ([]RevisionDTO, int64, error) {
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

	result := make([]RevisionDTO, 0, len(revisions))
	for i := range revisions {
		result = append(result, revisionDTO(revisions[i]))
	}
	return result, total, nil
}

func (s *RevisionService) GetContributors(
	contentType string,
	contentID uuid.UUID,
	limit int,
) ([]RevisionContributorDTO, int64, error) {
	if limit <= 0 || limit > 10 {
		limit = 10
	}

	baseQuery := func() *gorm.DB {
		return s.db.Table("revisions").
			Where("content_type = ? AND content_id = ? AND status = ?", contentType, contentID, "approved")
	}

	var total int64
	if err := baseQuery().Distinct("editor_id").Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var contributors []RevisionContributorDTO
	err := baseQuery().
		Select(`users.uuid AS user_id, users.username, users.display_name, users.avatar_url,
			COUNT(revisions.id) AS revision_count, MAX(revisions.created_at) AS last_contributed_at`).
		Joins(`JOIN "Users" AS users ON users.uuid = revisions.editor_id`).
		Group("users.uuid, users.username, users.display_name, users.avatar_url").
		Order("MAX(revisions.created_at) DESC, users.uuid ASC").
		Limit(limit).
		Scan(&contributors).Error
	if err != nil {
		return nil, 0, err
	}

	return contributors, total, nil
}

func revisionUserDTO(user *model.User) *RevisionUserDTO {
	if user == nil {
		return nil
	}
	return &RevisionUserDTO{
		UUID: user.UUID, Username: user.Username, DisplayName: user.DisplayName, AvatarURL: user.AvatarURL,
	}
}

func revisionDTO(revision model.Revision) RevisionDTO {
	return RevisionDTO{
		ID: revision.ID, ContentType: revision.ContentType, ContentID: revision.ContentID,
		VersionNumber: revision.VersionNumber, PreviousRevisionID: revision.PreviousRevisionID,
		ContentSnapshot: json.RawMessage(revision.ContentSnapshot), EditorID: revision.EditorID,
		Editor: revisionUserDTO(revision.Editor), EditSummary: revision.EditSummary,
		EditType: revision.EditType, Status: revision.Status, ReviewerID: revision.ReviewerID,
		Reviewer: revisionUserDTO(revision.Reviewer), ReviewedAt: revision.ReviewedAt,
		ReviewNotes: revision.ReviewNotes, IsCurrent: revision.IsCurrent, CreatedAt: revision.CreatedAt,
	}
}

func (s *RevisionService) GetRevision(
	contentType string,
	contentID uuid.UUID,
	version int,
) (RevisionDTO, error) {
	var revision model.Revision
	err := s.db.Where("content_id = ? AND content_type = ? AND version_number = ?", contentID, contentType, version).
		Preload("Editor").Preload("Reviewer").First(&revision).Error
	if err != nil {
		return RevisionDTO{}, err
	}
	return revisionDTO(revision), nil
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

func supportsRowLock(db *gorm.DB) bool {
	return db.Dialector.Name() == "postgres"
}
