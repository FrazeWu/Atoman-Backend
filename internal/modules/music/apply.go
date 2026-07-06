package music

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

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
	Title       string                  `json:"title"`
	ArtistIDs   []string                `json:"artist_ids"`
	ReleaseDate string                  `json:"release_date"`
	ReleaseYear int                     `json:"release_year"`
	CoverURL    string                  `json:"cover_url"`
	CoverKey    string                  `json:"cover_key"`
	AlbumType   string                  `json:"album_type"`
	Tracks      []albumTrackEditPayload `json:"tracks"`
}

type albumTrackEditPayload struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	TrackNumber int    `json:"track_number"`
	Lyrics      string `json:"lyrics"`
	AudioURL    string `json:"audio_url"`
	Removed     bool   `json:"removed"`
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
		birthDate, err := parseOptionalReleaseDate(payload.BirthDate)
		if err != nil {
			return err
		}
		birthYear := payload.BirthYear
		if birthDate != nil {
			birthYear = birthDate.Year()
		}
		activeStartDate, err := parseOptionalDate(payload.ActiveStartDate, "active_start_date")
		if err != nil {
			return err
		}
		activeEndDate, err := parseOptionalDate(payload.ActiveEndDate, "active_end_date")
		if err != nil {
			return err
		}

		artist := model.Artist{
			Name:           payload.Name,
			LegalName:      payload.LegalName,
			StageNamesJSON: mustMarshalStageNames(payload.StageNames),
			Bio:            payload.Bio,
			ImageURL:       payload.ImageURL,
			Nationality:    payload.Nationality,
			BirthPlace:     payload.BirthPlace,
			BirthDate:      birthDate,
			BirthYear:      birthYear,
			DeathYear:      payload.DeathYear,
			ArtistForm:     normalizeArtistForm(payload.ArtistForm),
			EntryStatus:    "open",
		}
		if activeStartDate != nil {
			artist.ActiveStartDate = *activeStartDate
		}
		if activeEndDate != nil {
			artist.ActiveEndDate = *activeEndDate
		}
		if err := tx.Create(&artist).Error; err != nil {
			return err
		}
		if err := replaceArtistMembers(tx, artist.ID, payload.Members); err != nil {
			return err
		}
		edit.EntityID = &artist.ID
		return nil
	case "update_artist":
		if edit.EntityID == nil {
			return apperr.BadRequest("validation.invalid_request", "entity_id is required")
		}
		var rawChanges map[string]json.RawMessage
		if err := json.Unmarshal([]byte(edit.ChangesJSON), &rawChanges); err != nil {
			return apperr.BadRequest("validation.invalid_request", "changes are not valid JSON")
		}
		var changes artistEditFields
		if err := json.Unmarshal([]byte(edit.ChangesJSON), &changes); err != nil {
			return apperr.BadRequest("validation.invalid_request", "changes are not valid JSON")
		}
		updates := map[string]any{}
		if changes.Name != "" {
			updates["name"] = changes.Name
		}
		if fieldPresent(rawChanges, "bio") {
			updates["bio"] = changes.Bio
		}
		if fieldPresent(rawChanges, "legal_name") {
			updates["legal_name"] = changes.LegalName
		}
		if len(changes.StageNames) > 0 {
			updates["stage_names_json"] = mustMarshalStageNames(changes.StageNames)
		}
		if fieldPresent(rawChanges, "image_url") {
			updates["image_url"] = changes.ImageURL
		}
		if changes.Nationality != "" {
			updates["nationality"] = changes.Nationality
		}
		if changes.BirthPlace != "" {
			updates["birth_place"] = changes.BirthPlace
		}
		if changes.BirthDate != "" {
			birthDate, err := parseOptionalReleaseDate(changes.BirthDate)
			if err != nil {
				return err
			}
			if birthDate != nil {
				updates["birth_date"] = *birthDate
				updates["birth_year"] = birthDate.Year()
			}
		}
		if changes.BirthYear != 0 {
			updates["birth_year"] = changes.BirthYear
		}
		if changes.DeathYear != 0 {
			updates["death_year"] = changes.DeathYear
		}
		if changes.ArtistForm != "" {
			updates["artist_form"] = normalizeArtistForm(changes.ArtistForm)
		}
		if changes.ActiveStartDate != "" {
			activeStartDate, err := parseOptionalDate(changes.ActiveStartDate, "active_start_date")
			if err != nil {
				return err
			}
			if activeStartDate != nil {
				updates["active_start_date"] = *activeStartDate
			}
		}
		if fieldPresent(rawChanges, "active_end_date") {
			activeEndDate, err := parseOptionalDate(changes.ActiveEndDate, "active_end_date")
			if err != nil {
				return err
			}
			if activeEndDate != nil {
				updates["active_end_date"] = *activeEndDate
			} else {
				updates["active_end_date"] = time.Time{}
			}
		}
		if len(updates) == 0 {
			if !fieldPresent(rawChanges, "members") {
				return apperr.BadRequest("validation.invalid_request", "artist changes are required")
			}
		}
		result := tx.Model(&model.Artist{}).Where("id = ?", *edit.EntityID).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return apperr.NotFound("music.artist_not_found", "Artist not found")
		}
		if fieldPresent(rawChanges, "members") {
			if err := replaceArtistMembers(tx, *edit.EntityID, changes.Members); err != nil {
				return err
			}
		}
		return nil
	case "delete_artist":
		if edit.EntityID == nil {
			return apperr.BadRequest("validation.invalid_request", "entity_id is required")
		}
		result := tx.Model(&model.Artist{}).Where("id = ?", *edit.EntityID).Update("entry_status", "closed")
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
		artistIDs, err := parseArtistIDs(payload.ArtistIDs)
		if err != nil {
			return err
		}
		if len(artistIDs) == 0 {
			return apperr.BadRequest("validation.invalid_request", "artist_ids are required")
		}
		releaseDate, err := parseOptionalReleaseDate(payload.ReleaseDate)
		if err != nil {
			return err
		}
		albumType := payload.AlbumType
		if albumType == "" {
			albumType = "album"
		}
		album := model.Album{
			Title:       payload.Title,
			CoverURL:    payload.CoverURL,
			CoverSource: coverSourceFromURL(payload.CoverURL),
			Status:      "open",
			EntryStatus: "open",
			AlbumType:   albumType,
			ReleaseYear: payload.ReleaseYear,
			UploadedBy:  &edit.SubmittedBy,
		}
		if payload.ReleaseYear > 0 {
			album.Year = payload.ReleaseYear
		}
		if releaseDate != nil {
			album.ReleaseDate = *releaseDate
			album.Year = releaseDate.Year()
			album.ReleaseYear = releaseDate.Year()
		}
		if err := tx.Create(&album).Error; err != nil {
			return err
		}
		if err := linkAlbumArtists(tx, &album, artistIDs); err != nil {
			return err
		}
		edit.EntityID = &album.ID
		return nil
	case "update_album":
		if edit.EntityID == nil {
			return apperr.BadRequest("validation.invalid_request", "entity_id is required")
		}
		var changes albumEditFields
		if err := json.Unmarshal([]byte(edit.ChangesJSON), &changes); err != nil {
			return apperr.BadRequest("validation.invalid_request", "changes are not valid JSON")
		}
		var album model.Album
		if err := tx.First(&album, "id = ?", *edit.EntityID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return apperr.NotFound("music.album_not_found", "Album not found")
			}
			return err
		}
		updates := map[string]any{}
		if changes.Title != "" {
			updates["title"] = changes.Title
		}
		if changes.ReleaseDate != "" {
			releaseDate, err := parseOptionalReleaseDate(changes.ReleaseDate)
			if err != nil {
				return err
			}
			if releaseDate != nil {
				updates["release_date"] = *releaseDate
				updates["year"] = releaseDate.Year()
				updates["release_year"] = releaseDate.Year()
			}
		}
		if changes.ReleaseYear != 0 {
			updates["release_year"] = changes.ReleaseYear
			updates["year"] = changes.ReleaseYear
		}
		if changes.CoverURL != "" {
			updates["cover_url"] = changes.CoverURL
			updates["cover_source"] = coverSourceFromURL(changes.CoverURL)
		}
		if changes.AlbumType != "" {
			updates["album_type"] = changes.AlbumType
		}
		if len(updates) > 0 {
			if err := tx.Model(&album).Updates(updates).Error; err != nil {
				return err
			}
		}
		if len(changes.ArtistIDs) > 0 {
			artistIDs, err := parseArtistIDs(changes.ArtistIDs)
			if err != nil {
				return err
			}
			if err := tx.Model(&album).Association("Artists").Clear(); err != nil {
				return err
			}
			if err := linkAlbumArtists(tx, &album, artistIDs); err != nil {
				return err
			}
		}
		if len(changes.Tracks) > 0 {
			if err := syncAlbumTracks(tx, &album, &edit.SubmittedBy, changes.Tracks); err != nil {
				return err
			}
		}
		return nil
	case "delete_album":
		if edit.EntityID == nil {
			return apperr.BadRequest("validation.invalid_request", "entity_id is required")
		}
		result := tx.Model(&model.Album{}).Where("id = ?", *edit.EntityID).Updates(map[string]any{"entry_status": "closed", "status": "closed"})
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

func syncAlbumTracks(tx *gorm.DB, album *model.Album, submittedBy *uuid.UUID, tracks []albumTrackEditPayload) error {
	for _, track := range tracks {
		trackID := track.ID
		if trackID != "" {
			id, err := uuid.Parse(trackID)
			if err != nil {
				return apperr.BadRequest("validation.invalid_request", "track id must be a valid UUID")
			}

			var song model.Song
			if err := tx.First(&song, "id = ? AND album_id = ?", id, album.ID).Error; err != nil {
				if err == gorm.ErrRecordNotFound {
					return apperr.NotFound("music.song_not_found", "Song not found")
				}
				return err
			}

			if track.Removed {
				if err := tx.Model(&song).Update("status", "closed").Error; err != nil {
					return err
				}
				continue
			}

			updates := map[string]any{}
			if track.Title != "" {
				updates["title"] = track.Title
			}
			if track.TrackNumber != 0 {
				updates["track_number"] = track.TrackNumber
			}
			updates["lyrics"] = track.Lyrics
			if track.AudioURL != "" {
				updates["audio_url"] = track.AudioURL
				updates["audio_source"] = coverSourceFromURL(track.AudioURL)
			}
			updates["status"] = "open"
			if len(updates) > 0 {
				if err := tx.Model(&song).Updates(updates).Error; err != nil {
					return err
				}
			}
			continue
		}

		if track.Removed || track.Title == "" || track.AudioURL == "" {
			continue
		}

		song := model.Song{
			Title:       track.Title,
			TrackNumber: track.TrackNumber,
			Lyrics:      track.Lyrics,
			AudioURL:    track.AudioURL,
			AudioSource: coverSourceFromURL(track.AudioURL),
			Status:      "open",
			AlbumID:     &album.ID,
			UploadedBy:  submittedBy,
		}
		song.ReleaseDate = album.ReleaseDate
		if err := tx.Create(&song).Error; err != nil {
			return err
		}
	}

	return nil
}

func parseArtistIDs(raw []string) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0, len(raw))
	for _, value := range raw {
		id, err := uuid.Parse(value)
		if err != nil {
			return nil, apperr.BadRequest("validation.invalid_request", "artist_ids must contain valid UUIDs")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func parseOptionalReleaseDate(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	return parseOptionalDate(raw, "release_date")
}

func parseOptionalDate(raw string, fieldName string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	parsed, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return nil, apperr.BadRequest("validation.invalid_request", fmt.Sprintf("%s must be YYYY-MM-DD", fieldName))
	}
	return &parsed, nil
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

func linkAlbumArtists(tx *gorm.DB, album *model.Album, artistIDs []uuid.UUID) error {
	for _, artistID := range artistIDs {
		var artist model.Artist
		if err := tx.First(&artist, "id = ?", artistID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return apperr.NotFound("music.artist_not_found", "Artist not found")
			}
			return err
		}
		if err := tx.Model(album).Association("Artists").Append(&artist); err != nil {
			return err
		}
	}
	return nil
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
		memberArtistID, err := uuid.Parse(member.ArtistID)
		if err != nil {
			return apperr.BadRequest("validation.invalid_request", "members.artist_id must be a valid UUID")
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
		joinDate, err := parseOptionalDate(member.JoinDate, "join_date")
		if err != nil {
			return err
		}
		leaveDate, err := parseOptionalDate(member.LeaveDate, "leave_date")
		if err != nil {
			return err
		}
		artistMember := model.ArtistMember{
			GroupArtistID:  groupArtistID,
			MemberArtistID: memberArtistID,
			JoinDate:       joinDate,
			LeaveDate:      leaveDate,
		}
		if err := tx.Create(&artistMember).Error; err != nil {
			return err
		}
	}
	return nil
}
