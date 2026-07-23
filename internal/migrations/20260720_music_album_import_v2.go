package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

func RunMusicAlbumImportV2Migration(db *gorm.DB) error {
	for _, value := range []any{
		&model.AlbumImportSession{},
		&model.AlbumImportFile{},
		&model.AlbumImportJob{},
		&model.Song{},
	} {
		if err := db.AutoMigrate(value); err != nil {
			return err
		}
	}
	return nil
}
