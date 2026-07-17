package migrations

import (
	"atoman/internal/model"
	"atoman/internal/musiclyrics"

	"gorm.io/gorm"
)

func RunMusicLyricsMigration(db *gorm.DB) error {
	if err := db.AutoMigrate(
		&model.MusicSongLyric{},
		&model.MusicSongLyricLine{},
		&model.MusicSongLyricVersion{},
		&model.MusicLyricAnnotation{},
		&model.MusicLyricAnnotationVote{},
	); err != nil {
		return err
	}
	var legacyCount int64
	if err := db.Model(&model.Song{}).
		Where("TRIM(COALESCE(lyrics, '')) <> ''").
		Where("NOT EXISTS (?)", db.Model(&model.MusicSongLyric{}).Select("1").Where("music_song_lyrics.song_id = \"Songs\".id AND music_song_lyrics.deleted_at IS NULL")).
		Count(&legacyCount).Error; err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if legacyCount > 0 {
			systemUserID, err := ensureRevisionMigrationSystemUser(tx)
			if err != nil {
				return err
			}
			var songs []model.Song
			if err := tx.Where("TRIM(COALESCE(lyrics, '')) <> ''").Find(&songs).Error; err != nil {
				return err
			}
			for _, song := range songs {
				var count int64
				if err := tx.Model(&model.MusicSongLyric{}).Where("song_id = ?", song.ID).Count(&count).Error; err != nil {
					return err
				}
				if count > 0 {
					continue
				}
				actorID := systemUserID
				if song.UploadedBy != nil {
					actorID = *song.UploadedBy
				}
				if err := musiclyrics.SyncLegacySongLyrics(tx, actorID, song.ID, song.Lyrics, "从旧歌词字段迁移"); err != nil {
					return err
				}
			}
		}

		var lyrics []model.MusicSongLyric
		if err := tx.Find(&lyrics).Error; err != nil {
			return err
		}
		for _, lyric := range lyrics {
			if err := tx.Model(&model.Song{}).Where("id = ?", lyric.SongID).Update("lyrics", lyric.Content).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
