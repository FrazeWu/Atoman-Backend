package migrations

import (
	"gorm.io/gorm"
)

func RunMusicAlbumImportsMigration(db *gorm.DB) error {
	return RunMusicAlbumImportV2Migration(db)
}
