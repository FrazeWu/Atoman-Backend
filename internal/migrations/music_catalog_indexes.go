package migrations

import "gorm.io/gorm"

// RunMusicCatalogIndexesMigration creates the indexes used by catalog pages,
// import history, and ordered track loading. Every statement is idempotent.
func RunMusicCatalogIndexesMigration(db *gorm.DB) error {
	statements := []struct {
		table string
		sql   string
	}{
		{"Albums", `CREATE INDEX IF NOT EXISTS idx_music_albums_visible_release
			ON "Albums" (entry_status, release_date DESC, title) WHERE deleted_at IS NULL`},
		{"Songs", `CREATE INDEX IF NOT EXISTS idx_music_songs_album_track
			ON "Songs" (album_id, track_number, created_at) WHERE deleted_at IS NULL`},
		{"music_album_import_sessions", `CREATE INDEX IF NOT EXISTS idx_music_import_sessions_user_status_updated
			ON music_album_import_sessions (user_id, status, updated_at DESC) WHERE deleted_at IS NULL`},
	}
	for _, statement := range statements {
		if !db.Migrator().HasTable(statement.table) {
			continue
		}
		if err := db.Exec(statement.sql).Error; err != nil {
			return err
		}
	}
	return nil
}
