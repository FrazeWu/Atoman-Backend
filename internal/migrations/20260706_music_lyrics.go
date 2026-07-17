package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

func RunMusicLyricsMigration(db *gorm.DB) error {
	return db.AutoMigrate(
		&model.MusicSongLyric{},
		&model.MusicSongLyricLine{},
		&model.MusicSongLyricVersion{},
		&model.MusicLyricAnnotation{},
		&model.MusicLyricAnnotationVote{},
	)
}
