package service

import (
	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PromoteArtistsWithAlbums makes draft artists public once they have an album.
func PromoteArtistsWithAlbums(db *gorm.DB, artistIDs ...uuid.UUID) error {
	if len(artistIDs) == 0 {
		return nil
	}

	return db.Model(&model.Artist{}).
		Where(`"Artists".id IN ?`, artistIDs).
		Where(`"Artists".lifecycle_status = ?`, model.MusicLifecycleDraft).
		Where(`EXISTS (
			SELECT 1
			FROM album_artists aa
			JOIN "Albums" a ON a.id = aa.album_id
			WHERE aa.artist_id = "Artists".id
				AND aa.deleted_at IS NULL
				AND a.deleted_at IS NULL
				AND a.lifecycle_status IN ?
		)`, []string{model.MusicLifecycleDraft, model.MusicLifecycleActive}).
		Updates(map[string]any{
			"entry_status":     "open",
			"lifecycle_status": model.MusicLifecycleActive,
		}).Error
}
