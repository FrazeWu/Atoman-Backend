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
	Title         string                   `json:"title"`
	ArtistIDs     []string                 `json:"artist_ids"`
	ArtistCredits []AlbumArtistCreditInput `json:"artist_credits"`
	ReleaseDate   string                   `json:"release_date"`
	ReleaseYear   int                      `json:"release_year"`
	CoverURL      string                   `json:"cover_url"`
	CoverKey      string                   `json:"cover_key"`
	AlbumType     string                   `json:"album_type"`
	Tracks        []albumTrackEditPayload  `json:"tracks"`
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
		credits := payload.ArtistCredits
		defaultMissingRoles := false
		if len(credits) == 0 {
			credits = legacyAlbumArtistCredits(payload.ArtistIDs)
			defaultMissingRoles = true
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
		if err := replaceAlbumArtistCredits(tx, album.ID, credits, defaultMissingRoles); err != nil {
			return err
		}
		edit.EntityID = &album.ID
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
			if err := SyncLegacySongLyrics(tx, *submittedBy, song.ID, track.Lyrics, "通过专辑编辑更新歌词"); err != nil {
				return err
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
		if err := SyncLegacySongLyrics(tx, *submittedBy, song.ID, track.Lyrics, "通过专辑编辑创建歌词"); err != nil {
			return err
		}
	}

	return nil
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
