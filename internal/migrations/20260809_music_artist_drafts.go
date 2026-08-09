package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

func RunMusicArtistDraftsMigration(db *gorm.DB) error {
	if err := db.AutoMigrate(&model.Artist{}, &model.Album{}); err != nil {
		return err
	}
	if !db.Migrator().HasColumn(&model.Artist{}, "Disambiguation") {
		if err := db.Migrator().AddColumn(&model.Artist{}, "Disambiguation"); err != nil {
			return err
		}
	}
	if db.Dialector.Name() == "postgres" {
		if err := db.Exec(`ALTER TABLE "Artists" DROP CONSTRAINT IF EXISTS "uni_Artists_name"`).Error; err != nil {
			return err
		}
	}
	if err := db.Exec(`DROP INDEX IF EXISTS "uni_Artists_name"`).Error; err != nil {
		return err
	}
	return db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_artists_display_name_unique
		ON "Artists" (LOWER(TRIM(name)), LOWER(TRIM(COALESCE(disambiguation, ''))))
		WHERE deleted_at IS NULL`).Error
}
