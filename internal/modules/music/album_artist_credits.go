package music

import (
	"errors"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	revisionservice "atoman/internal/service"

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

func replaceAlbumArtistCredits(tx *gorm.DB, albumID uuid.UUID, credits []AlbumArtistCreditInput, defaultMissingRoles bool, actorID uuid.UUID) error {
	rows, err := normalizeAlbumArtistCredits(tx, albumID, credits, defaultMissingRoles, actorID)
	if err != nil {
		return err
	}
	if err := tx.Where("album_id = ?", albumID).Delete(&model.AlbumArtist{}).Error; err != nil {
		return err
	}
	if err := tx.Create(&rows).Error; err != nil {
		return err
	}
	artistIDs := make([]uuid.UUID, 0, len(rows))
	seenArtistIDs := make(map[uuid.UUID]struct{}, len(rows))
	for _, row := range rows {
		if _, seen := seenArtistIDs[row.ArtistID]; seen {
			continue
		}
		seenArtistIDs[row.ArtistID] = struct{}{}
		artistIDs = append(artistIDs, row.ArtistID)
	}
	return revisionservice.PromoteArtistsWithAlbums(tx, artistIDs...)
}

func normalizeAlbumArtistCredits(tx *gorm.DB, albumID uuid.UUID, credits []AlbumArtistCreditInput, defaultMissingRoles bool, actorID uuid.UUID) ([]model.AlbumArtist, error) {
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
		var artist model.Artist
		if err := tx.Select("id", "lifecycle_status", "created_by").First(&artist, "id = ?", artistID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, apperr.NotFound("music.artist_not_found", "Artist not found")
			}
			return nil, err
		}
		if artist.LifecycleStatus != model.MusicLifecycleActive && artist.LifecycleStatus != model.MusicLifecycleDraft {
			return nil, apperr.NotFound("music.artist_not_found", "Artist not found")
		}
		if artist.LifecycleStatus == model.MusicLifecycleDraft && !canUseArtistDraftForCredit(tx, artist, actorID) {
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

func canUseArtistDraftForCredit(tx *gorm.DB, artist model.Artist, actorID uuid.UUID) bool {
	if actorID == uuid.Nil {
		return false
	}
	var user model.User
	if err := tx.Select("role").First(&user, "uuid = ?", actorID).Error; err == nil && authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return true
	}
	return artist.CreatedBy != nil && *artist.CreatedBy == actorID
}
