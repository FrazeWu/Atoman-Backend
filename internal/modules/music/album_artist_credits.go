package music

import (
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const albumArtistCustomRole = "custom"

var fixedAlbumArtistRoles = map[string]struct{}{
	"primary": {}, "featured": {}, "vocals": {}, "backing_vocals": {},
	"writer": {}, "composer": {}, "arranger": {}, "producer": {},
	"vocal_producer": {}, "recording_engineer": {}, "mixing_engineer": {},
	"mastering_engineer": {}, "remixer": {},
}

type AlbumArtistRoleInput struct {
	Role  string `json:"role"`
	Label string `json:"label,omitempty"`
}

type AlbumArtistCreditInput struct {
	ArtistID string                 `json:"artist_id"`
	Roles    []AlbumArtistRoleInput `json:"roles"`
	Position int                    `json:"position"`
}

func legacyAlbumArtistCredits(artistIDs []string) []AlbumArtistCreditInput {
	credits := make([]AlbumArtistCreditInput, 0, len(artistIDs))
	for index, artistID := range artistIDs {
		credits = append(credits, AlbumArtistCreditInput{
			ArtistID: artistID,
			Roles:    []AlbumArtistRoleInput{{Role: "primary"}},
			Position: index + 1,
		})
	}
	return credits
}

func replaceAlbumArtistCredits(tx *gorm.DB, albumID uuid.UUID, credits []AlbumArtistCreditInput, defaultMissingRoles bool) error {
	rows, err := normalizeAlbumArtistCredits(tx, albumID, credits, defaultMissingRoles)
	if err != nil {
		return err
	}
	if err := tx.Where("album_id = ?", albumID).Delete(&model.AlbumArtist{}).Error; err != nil {
		return err
	}
	return tx.Create(&rows).Error
}

func normalizeAlbumArtistCredits(tx *gorm.DB, albumID uuid.UUID, credits []AlbumArtistCreditInput, defaultMissingRoles bool) ([]model.AlbumArtist, error) {
	if len(credits) == 0 {
		return nil, apperr.BadRequest("validation.invalid_request", "artist_credits are required")
	}

	rows := make([]model.AlbumArtist, 0, len(credits))
	seen := make(map[string]struct{})
	hasPrimary := false
	for creditIndex, credit := range credits {
		artistID, err := uuid.Parse(strings.TrimSpace(credit.ArtistID))
		if err != nil {
			return nil, apperr.BadRequest("validation.invalid_request", "artist_credits must contain valid artist_id values")
		}
		var count int64
		if err := tx.Model(&model.Artist{}).Where("id = ? AND COALESCE(entry_status, '') <> ?", artistID, "closed").Count(&count).Error; err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, apperr.NotFound("music.artist_not_found", "Artist not found")
		}

		roles := credit.Roles
		if len(roles) == 0 && defaultMissingRoles {
			roles = []AlbumArtistRoleInput{{Role: "primary"}}
		}
		if len(roles) == 0 {
			return nil, apperr.BadRequest("validation.invalid_request", "each artist credit must contain at least one role")
		}
		position := credit.Position
		if position <= 0 {
			position = creditIndex + 1
		}
		for _, roleInput := range roles {
			role := strings.ToLower(strings.TrimSpace(roleInput.Role))
			customRole := ""
			if role == albumArtistCustomRole {
				customRole = strings.TrimSpace(roleInput.Label)
				if customRole == "" {
					return nil, apperr.BadRequest("validation.invalid_request", "custom artist roles require a label")
				}
			} else if _, ok := fixedAlbumArtistRoles[role]; !ok {
				return nil, apperr.BadRequest("validation.invalid_request", "artist role is not supported")
			}
			key := artistID.String() + "\x00" + role + "\x00" + strings.ToLower(customRole)
			if _, duplicate := seen[key]; duplicate {
				return nil, apperr.BadRequest("validation.invalid_request", "duplicate artist role")
			}
			seen[key] = struct{}{}
			hasPrimary = hasPrimary || role == "primary"
			rows = append(rows, model.AlbumArtist{
				AlbumID: albumID, ArtistID: artistID, Role: role, CustomRole: customRole, Position: position,
			})
		}
	}
	if !hasPrimary {
		return nil, apperr.BadRequest("validation.invalid_request", "at least one primary artist is required")
	}
	return rows, nil
}
