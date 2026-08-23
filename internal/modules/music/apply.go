package music

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	revisionservice "atoman/internal/service"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type artistEditFields struct {
	Name            string                   `json:"name"`
	LegalName       string                   `json:"legal_name"`
	StageNames      []ArtistStageNamePayload `json:"stage_names"`
	Bio             string                   `json:"bio"`
	ImageURL        string                   `json:"image_url"`
	Nationality     string                   `json:"nationality"`
	BirthPlace      string                   `json:"birth_place"`
	BirthDate       string                   `json:"birth_date"`
	BirthYear       int                      `json:"birth_year"`
	DeathYear       int                      `json:"death_year"`
	ArtistForm      string                   `json:"artist_form"`
	ActiveStartDate string                   `json:"active_start_date"`
	ActiveEndDate   string                   `json:"active_end_date"`
	Members         []ArtistMemberPayload    `json:"members"`
}

type albumEditFields struct {
	Title         string                   `json:"title"`
	ArtistIDs     []string                 `json:"artist_ids"`
	ArtistCredits []AlbumArtistCreditInput `json:"artist_credits"`
	ReleaseDate   string                   `json:"release_date"`
	ReleaseYear   int                      `json:"release_year"`
	CoverURL      string                   `json:"cover_url"`
	CoverKey      string                   `json:"cover_key"`
	AlbumType     string                   `json:"album_type"`
}

func applyEdit(tx *gorm.DB, edit *model.MusicEdit) error {
	switch edit.Type {
	case "create_artist":
		var payload artistEditFields
		if err := json.Unmarshal([]byte(edit.PayloadJSON), &payload); err != nil {
			return apperr.BadRequest("validation.invalid_request", "payload is not valid JSON")
		}
		if payload.Name == "" {
			return apperr.BadRequest("validation.invalid_request", "artist name is required")
		}
		birthDate, birthDatePrecision, err := parseOptionalDate(payload.BirthDate, "birth_date")
		if err != nil {
			return err
		}
		birthYear := payload.BirthYear
		if birthDate != nil {
			birthYear = birthDate.Year()
		}
		activeStartDate, activeStartDatePrecision, err := parseOptionalDate(payload.ActiveStartDate, "active_start_date")
		if err != nil {
			return err
		}
		activeEndDate, activeEndDatePrecision, err := parseOptionalDate(payload.ActiveEndDate, "active_end_date")
		if err != nil {
			return err
		}

		artist := model.Artist{
			Name:               payload.Name,
			LegalName:          payload.LegalName,
			StageNamesJSON:     mustMarshalStageNames(payload.StageNames),
			Bio:                payload.Bio,
			ImageURL:           payload.ImageURL,
			Nationality:        payload.Nationality,
			BirthPlace:         payload.BirthPlace,
			BirthDate:          birthDate,
			BirthDatePrecision: birthDatePrecision,
			BirthYear:          birthYear,
			DeathYear:          payload.DeathYear,
			ArtistForm:         normalizeArtistForm(payload.ArtistForm),
			EntryStatus:        "open",
			LifecycleStatus:    model.MusicLifecycleActive,
			EditStatus:         model.MusicEditDevelopment,
		}
		if activeStartDate != nil {
			artist.ActiveStartDate = *activeStartDate
		}
		artist.ActiveStartDatePrecision = activeStartDatePrecision
		if activeEndDate != nil {
			artist.ActiveEndDate = *activeEndDate
		}
		artist.ActiveEndDatePrecision = activeEndDatePrecision
		if err := tx.Create(&artist).Error; err != nil {
			return err
		}
		if err := replaceArtistMembers(tx, artist.ID, payload.Members); err != nil {
			return err
		}
		if _, err := revisionservice.NewRevisionService(tx).EnsureInitialRevision("artist", artist.ID, edit.SubmittedBy); err != nil {
			return err
		}
		edit.EntityID = &artist.ID
		return nil
	case "delete_artist":
		if edit.EntityID == nil {
			return apperr.BadRequest("validation.invalid_request", "entity_id is required")
		}
		result := tx.Model(&model.Artist{}).Where("id = ?", *edit.EntityID).Updates(map[string]any{"entry_status": "closed", "lifecycle_status": model.MusicLifecycleRetired})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return apperr.NotFound("music.artist_not_found", "Artist not found")
		}
		return nil
	case "create_album":
		var payload albumEditFields
		if err := json.Unmarshal([]byte(edit.PayloadJSON), &payload); err != nil {
			return apperr.BadRequest("validation.invalid_request", "payload is not valid JSON")
		}
		if payload.Title == "" {
			return apperr.BadRequest("validation.invalid_request", "album title is required")
		}
		credits := payload.ArtistCredits
		defaultMissingRoles := false
		if len(credits) == 0 {
			credits = legacyAlbumArtistCredits(payload.ArtistIDs)
			defaultMissingRoles = true
		}
		releaseDate, releaseDatePrecision, err := parseOptionalReleaseDate(payload.ReleaseDate)
		if err != nil {
			return err
		}
		albumType := payload.AlbumType
		if albumType == "" {
			albumType = "album"
		}
		album := model.Album{
			Title:           payload.Title,
			CoverURL:        payload.CoverURL,
			CoverSource:     coverSourceFromURL(payload.CoverURL),
			Status:          "open",
			EntryStatus:     "open",
			AlbumType:       albumType,
			ReleaseYear:     payload.ReleaseYear,
			UploadedBy:      &edit.SubmittedBy,
			LifecycleStatus: model.MusicLifecycleActive,
			EditStatus:      model.MusicEditDevelopment,
		}
		if payload.ReleaseYear > 0 {
			album.Year = payload.ReleaseYear
		}
		album.ReleaseDatePrecision = releaseDatePrecision
		if releaseDate != nil {
			album.ReleaseDate = *releaseDate
			album.Year = releaseDate.Year()
			album.ReleaseYear = releaseDate.Year()
		}
		if err := tx.Create(&album).Error; err != nil {
			return err
		}
		if err := replaceAlbumArtistCredits(tx, album.ID, credits, defaultMissingRoles, edit.SubmittedBy); err != nil {
			return err
		}
		if _, err := revisionservice.NewRevisionService(tx).EnsureInitialRevision("album", album.ID, edit.SubmittedBy); err != nil {
			return err
		}
		edit.EntityID = &album.ID
		return nil
	case "delete_album":
		if edit.EntityID == nil {
			return apperr.BadRequest("validation.invalid_request", "entity_id is required")
		}
		result := tx.Model(&model.Album{}).Where("id = ?", *edit.EntityID).Updates(map[string]any{"entry_status": "closed", "status": "closed", "lifecycle_status": model.MusicLifecycleRetired})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return apperr.NotFound("music.album_not_found", "Album not found")
		}
		return nil
	default:
		return apperr.Unprocessable("music.edit_invalid_type", fmt.Sprintf("unsupported edit type %s", edit.Type))
	}
}

func coverSourceFromURL(url string) string {
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

func normalizeArtistForm(raw string) string {
	switch raw {
	case "group":
		return "group"
	default:
		return "person"
	}
}

func fieldPresent(raw map[string]json.RawMessage, key string) bool {
	_, ok := raw[key]
	return ok
}

func replaceArtistMembers(tx *gorm.DB, groupArtistID uuid.UUID, members []ArtistMemberPayload) error {
	if err := tx.Where("group_artist_id = ?", groupArtistID).Delete(&model.ArtistMember{}).Error; err != nil {
		return err
	}
	for _, member := range members {
		memberArtistID, err := resolveArtistMemberID(tx, member)
		if err != nil {
			return err
		}
		if memberArtistID == uuid.Nil {
			continue
		}
		if memberArtistID == groupArtistID {
			return apperr.BadRequest("validation.invalid_request", "group artist cannot reference itself as a member")
		}
		var artist model.Artist
		if err := tx.First(&artist, "id = ?", memberArtistID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return apperr.NotFound("music.artist_not_found", "Artist not found")
			}
			return err
		}
		joinDate, joinDatePrecision, err := parseOptionalDate(member.JoinDate, "join_date")
		if err != nil {
			return err
		}
		leaveDate, leaveDatePrecision, err := parseOptionalDate(member.LeaveDate, "leave_date")
		if err != nil {
			return err
		}
		artistMember := model.ArtistMember{
			GroupArtistID:      groupArtistID,
			MemberArtistID:     memberArtistID,
			JoinDate:           joinDate,
			JoinDatePrecision:  joinDatePrecision,
			LeaveDate:          leaveDate,
			LeaveDatePrecision: leaveDatePrecision,
		}
		if err := tx.Create(&artistMember).Error; err != nil {
			return err
		}
	}
	return nil
}

func resolveArtistMemberID(_ *gorm.DB, member ArtistMemberPayload) (uuid.UUID, error) {
	if artistID := strings.TrimSpace(member.ArtistID); artistID != "" {
		parsed, err := uuid.Parse(artistID)
		if err != nil {
			return uuid.Nil, apperr.BadRequest("validation.invalid_request", "members.artist_id must be a valid UUID")
		}
		return parsed, nil
	}
	return uuid.Nil, apperr.BadRequest("validation.invalid_request", "members.artist_id is required")
}
