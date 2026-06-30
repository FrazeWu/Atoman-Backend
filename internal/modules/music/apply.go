package music

import (
	"encoding/json"
	"fmt"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type artistEditFields struct {
	Name        string                   `json:"name"`
	LegalName   string                   `json:"legal_name"`
	StageNames  []ArtistStageNamePayload `json:"stage_names"`
	Bio         string                   `json:"bio"`
	ImageURL    string                   `json:"image_url"`
	Nationality string                   `json:"nationality"`
	BirthPlace  string                   `json:"birth_place"`
	BirthDate   string                   `json:"birth_date"`
	BirthYear   int                      `json:"birth_year"`
	DeathYear   int                      `json:"death_year"`
}

type albumEditFields struct {
	Title       string                    `json:"title"`
	ArtistIDs   []string                  `json:"artist_ids"`
	ReleaseDate string                    `json:"release_date"`
	ReleaseYear int                       `json:"release_year"`
	CoverURL    string                    `json:"cover_url"`
	CoverKey    string                    `json:"cover_key"`
	AlbumType   string                    `json:"album_type"`
	Tracks      []AlbumImportTrackPayload `json:"tracks"`
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
			EntryStatus:    "open",
		}
		if err := tx.Create(&artist).Error; err != nil {
			return err
		}
		edit.EntityID = &artist.ID
		return nil
	case "update_artist":
		if edit.EntityID == nil {
			return apperr.BadRequest("validation.invalid_request", "entity_id is required")
		}
		var changes artistEditFields
		if err := json.Unmarshal([]byte(edit.ChangesJSON), &changes); err != nil {
			return apperr.BadRequest("validation.invalid_request", "changes are not valid JSON")
		}
		updates := map[string]any{}
		if changes.Name != "" {
			updates["name"] = changes.Name
		}
		if changes.Bio != "" {
			updates["bio"] = changes.Bio
		}
		if changes.LegalName != "" {
			updates["legal_name"] = changes.LegalName
		}
		if len(changes.StageNames) > 0 {
			updates["stage_names_json"] = mustMarshalStageNames(changes.StageNames)
		}
		if changes.ImageURL != "" {
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
		if len(updates) == 0 {
			return apperr.BadRequest("validation.invalid_request", "artist changes are required")
		}
		result := tx.Model(&model.Artist{}).Where("id = ?", *edit.EntityID).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return apperr.NotFound("music.artist_not_found", "Artist not found")
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
	releaseDate, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return nil, apperr.BadRequest("validation.invalid_request", "release_date must be YYYY-MM-DD")
	}
	return &releaseDate, nil
}

func coverSourceFromURL(url string) string {
	if url == "" {
		return ""
	}
	return "s3"
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
