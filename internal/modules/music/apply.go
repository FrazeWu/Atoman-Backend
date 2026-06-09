package music

import (
	"encoding/json"
	"fmt"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"gorm.io/gorm"
)

func applyEdit(tx *gorm.DB, edit *model.MusicEdit) error {
	switch edit.Type {
	case "create_artist":
		var payload struct {
			Name        string `json:"name"`
			Bio         string `json:"bio"`
			ImageURL    string `json:"image_url"`
			Nationality string `json:"nationality"`
			BirthYear   int    `json:"birth_year"`
			DeathYear   int    `json:"death_year"`
		}
		if err := json.Unmarshal([]byte(edit.PayloadJSON), &payload); err != nil {
			return apperr.BadRequest("validation.invalid_request", "payload is not valid JSON")
		}
		if payload.Name == "" {
			return apperr.BadRequest("validation.invalid_request", "artist name is required")
		}

		artist := model.Artist{
			Name:        payload.Name,
			Bio:         payload.Bio,
			ImageURL:    payload.ImageURL,
			Nationality: payload.Nationality,
			BirthYear:   payload.BirthYear,
			DeathYear:   payload.DeathYear,
			EntryStatus: "open",
		}
		if err := tx.Create(&artist).Error; err != nil {
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
	default:
		return apperr.Unprocessable("music.edit_invalid_type", fmt.Sprintf("unsupported edit type %s", edit.Type))
	}
}
