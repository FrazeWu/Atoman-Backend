package music

import (
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ArchiveMusicEntry removes an album (including its tracks) or a song from
// public discovery without deleting wiki history or media objects.
func (s *Service) ArchiveMusicEntry(user authctx.CurrentUser, entityType string, id uuid.UUID, reason string) error {
	if user.Role != authctx.RoleAdmin && user.Role != authctx.RoleOwner {
		return apperr.Forbidden("music.archive_forbidden", "Administrator access is required")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return apperr.BadRequest("validation.invalid_request", "archive reason is required")
	}
	if entityType != "album" && entityType != "song" {
		return apperr.BadRequest("validation.invalid_request", "invalid archive entity type")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var result *gorm.DB
		switch entityType {
		case "album":
			var album model.Album
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&album, "id = ?", id).Error; err != nil {
				return err
			}
			if album.LifecycleStatus != model.MusicLifecycleActive {
				return apperr.Conflict("music.archive_invalid_state", "Only active entries can be archived")
			}
			result = tx.Model(&album).Updates(map[string]any{"lifecycle_status": model.MusicLifecycleRetired, "entry_status": "closed", "status": "closed"})
			if result.Error != nil {
				return result.Error
			}
			if err := tx.Model(&model.Song{}).Where("album_id = ? AND lifecycle_status = ?", id, model.MusicLifecycleActive).
				Updates(map[string]any{"lifecycle_status": model.MusicLifecycleRetired, "status": "closed"}).Error; err != nil {
				return err
			}
		case "song":
			var song model.Song
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&song, "id = ?", id).Error; err != nil {
				return err
			}
			if song.LifecycleStatus != model.MusicLifecycleActive {
				return apperr.Conflict("music.archive_invalid_state", "Only active entries can be archived")
			}
			result = tx.Model(&song).Updates(map[string]any{"lifecycle_status": model.MusicLifecycleRetired, "status": "closed"})
			if result.Error != nil {
				return result.Error
			}
		}
		return tx.Create(&model.AuditLog{ActorID: &user.ID, Action: "music.archive", EntityType: entityType, EntityID: &id, Reason: reason}).Error
	})
}
