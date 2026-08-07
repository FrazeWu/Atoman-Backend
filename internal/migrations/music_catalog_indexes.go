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
		{"music_search_interactions", `CREATE INDEX IF NOT EXISTS idx_music_search_interactions_user_created
			ON music_search_interactions (user_id, created_at DESC) WHERE deleted_at IS NULL`},
	}
	for _, statement := range statements {
		if !db.Migrator().HasTable(statement.table) {
			continue
		}
		if err := db.Exec(statement.sql).Error; err != nil {
			return err
		}
	}
	if db.Dialector.Name() == "postgres" || db.Dialector.Name() == "pgx" {
		if err := db.Exec(`CREATE EXTENSION IF NOT EXISTS pg_trgm`).Error; err != nil {
			return err
		}
		searchIndexes := []struct {
			table string
			sql   string
		}{
			{"Songs", `CREATE INDEX IF NOT EXISTS idx_music_songs_title_trgm ON "Songs" USING GIN (LOWER(title) gin_trgm_ops) WHERE deleted_at IS NULL`},
			{"Albums", `CREATE INDEX IF NOT EXISTS idx_music_albums_title_trgm ON "Albums" USING GIN (LOWER(title) gin_trgm_ops) WHERE deleted_at IS NULL`},
			{"Artists", `CREATE INDEX IF NOT EXISTS idx_music_artists_name_trgm ON "Artists" USING GIN (LOWER(name) gin_trgm_ops) WHERE deleted_at IS NULL`},
			{"music_playlists", `CREATE INDEX IF NOT EXISTS idx_music_playlists_name_trgm ON music_playlists USING GIN (LOWER(name) gin_trgm_ops) WHERE deleted_at IS NULL`},
		}
		for _, statement := range searchIndexes {
			if db.Migrator().HasTable(statement.table) {
				if err := db.Exec(statement.sql).Error; err != nil {
					return err
				}
			}
		}
	}
	return nil
}
