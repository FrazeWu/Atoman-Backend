package migrations

import (
	"fmt"
	"strings"

	"atoman/internal/model"

	"gorm.io/gorm"
)

var songArtistCreditPrimaryKey = []string{"song_id", "artist_id", "role", "custom_role"}

func RunMusicSongCreditsMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.SongArtist{}) {
		return db.AutoMigrate(&model.Song{}, &model.SongArtist{})
	}
	if !db.Migrator().HasColumn(&model.Song{}, "disc_number") {
		if err := db.Migrator().AddColumn(&model.Song{}, "DiscNumber"); err != nil {
			return fmt.Errorf("add song disc number: %w", err)
		}
	}
	if !db.Migrator().HasColumn(&model.SongArtist{}, "custom_role") {
		if err := db.Migrator().AddColumn(&model.SongArtist{}, "CustomRole"); err != nil {
			return fmt.Errorf("add song artist custom role: %w", err)
		}
	}
	if err := db.Exec("UPDATE song_artists SET role = 'primary' WHERE role IS NULL OR TRIM(role) = ''").Error; err != nil {
		return err
	}
	if err := db.Exec("UPDATE song_artists SET custom_role = '' WHERE custom_role IS NULL").Error; err != nil {
		return err
	}
	if err := db.Exec("UPDATE song_artists SET position = 1 WHERE position IS NULL OR position <= 0").Error; err != nil {
		return err
	}
	if err := db.Exec(`UPDATE "Songs" SET disc_number = 1 WHERE disc_number IS NULL OR disc_number <= 0`).Error; err != nil {
		return err
	}

	switch db.Dialector.Name() {
	case "sqlite":
		return migrateSQLiteSongArtistCredits(db)
	case "postgres":
		return migratePostgresSongArtistCredits(db)
	default:
		return db.AutoMigrate(&model.SongArtist{})
	}
}

func migrateSQLiteSongArtistCredits(db *gorm.DB) error {
	var info []struct {
		Name string `gorm:"column:name"`
		PK   int    `gorm:"column:pk"`
	}
	if err := db.Raw("PRAGMA table_info(song_artists)").Scan(&info).Error; err != nil {
		return err
	}
	columns := make([]string, 0, len(info))
	for _, column := range info {
		if column.PK > 0 {
			columns = append(columns, column.Name)
		}
	}
	if sameColumnList(columns, songArtistCreditPrimaryKey) {
		return db.Exec("CREATE INDEX IF NOT EXISTS idx_song_artists_artist_id ON song_artists (artist_id)").Error
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`CREATE TABLE song_artists_credit_migration (
			song_id uuid NOT NULL,
			artist_id uuid NOT NULL,
			role text NOT NULL DEFAULT 'primary',
			custom_role text NOT NULL DEFAULT '',
			position integer NOT NULL DEFAULT 1,
			created_at datetime,
			updated_at datetime,
			PRIMARY KEY (song_id, artist_id, role, custom_role),
			CONSTRAINT fk_song_artists_song FOREIGN KEY (song_id) REFERENCES "Songs"(id),
			CONSTRAINT fk_song_artists_artist FOREIGN KEY (artist_id) REFERENCES "Artists"(id)
		)`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO song_artists_credit_migration
			(song_id, artist_id, role, custom_role, position, created_at, updated_at)
			SELECT song_id, artist_id, COALESCE(NULLIF(TRIM(role), ''), 'primary'),
				COALESCE(custom_role, ''), CASE WHEN position > 0 THEN position ELSE 1 END,
				created_at, updated_at FROM song_artists`).Error; err != nil {
			return err
		}
		if err := tx.Exec("DROP TABLE song_artists").Error; err != nil {
			return err
		}
		if err := tx.Exec("ALTER TABLE song_artists_credit_migration RENAME TO song_artists").Error; err != nil {
			return err
		}
		return tx.Exec("CREATE INDEX IF NOT EXISTS idx_song_artists_artist_id ON song_artists (artist_id)").Error
	})
}

func migratePostgresSongArtistCredits(db *gorm.DB) error {
	var constraints []struct {
		ConstraintName string `gorm:"column:constraint_name"`
		ColumnName     string `gorm:"column:column_name"`
	}
	if err := db.Raw(`SELECT tc.constraint_name, kcu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON tc.constraint_name = kcu.constraint_name AND tc.table_schema = kcu.table_schema
		WHERE tc.table_schema = current_schema() AND tc.table_name = 'song_artists'
		  AND tc.constraint_type = 'PRIMARY KEY'
		ORDER BY kcu.ordinal_position`).Scan(&constraints).Error; err != nil {
		return err
	}
	columns := make([]string, 0, len(constraints))
	for _, constraint := range constraints {
		columns = append(columns, constraint.ColumnName)
	}
	if !sameColumnList(columns, songArtistCreditPrimaryKey) {
		if len(constraints) > 0 {
			name := strings.ReplaceAll(constraints[0].ConstraintName, `"`, `""`)
			if err := db.Exec(`ALTER TABLE song_artists DROP CONSTRAINT "` + name + `"`).Error; err != nil {
				return err
			}
		}
		if err := db.Exec(`ALTER TABLE song_artists
			ALTER COLUMN role SET DEFAULT 'primary', ALTER COLUMN role SET NOT NULL,
			ALTER COLUMN custom_role SET DEFAULT '', ALTER COLUMN custom_role SET NOT NULL,
			ADD PRIMARY KEY (song_id, artist_id, role, custom_role)`).Error; err != nil {
			return err
		}
	}
	return db.Exec("CREATE INDEX IF NOT EXISTS idx_song_artists_artist_id ON song_artists (artist_id)").Error
}
