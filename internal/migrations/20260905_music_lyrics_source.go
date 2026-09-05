package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

// RunMusicLyricsSourceMigration labels legacy lyrics created by the LRCLIB importer.
func RunMusicLyricsSourceMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.MusicSongLyric{}) {
		return nil
	}
	return db.Model(&model.MusicSongLyric{}).
		Where("source = '' AND edit_summary = ?", "自动匹配歌词").
		Update("source", "lrclib").Error
}
