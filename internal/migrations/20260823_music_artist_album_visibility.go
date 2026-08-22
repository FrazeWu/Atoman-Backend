package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

// RunMusicArtistAlbumVisibilityMigration publishes draft artists that already have albums.
// It is idempotent because migration steps run on every startup.
func RunMusicArtistAlbumVisibilityMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.Artist{}) ||
		!db.Migrator().HasTable(&model.Album{}) ||
		!db.Migrator().HasTable(&model.AlbumArtist{}) {
		return nil
	}

	return db.Exec(`UPDATE "Artists"
		SET entry_status = 'open', lifecycle_status = 'active', updated_at = CURRENT_TIMESTAMP
		WHERE lifecycle_status = 'draft'
		  AND EXISTS (
			SELECT 1
			FROM album_artists aa
			JOIN "Albums" a ON a.id = aa.album_id
			WHERE aa.artist_id = "Artists".id
			  AND aa.deleted_at IS NULL
			  AND a.deleted_at IS NULL
			  AND a.lifecycle_status IN ('draft', 'active')
		  )`).Error
}
