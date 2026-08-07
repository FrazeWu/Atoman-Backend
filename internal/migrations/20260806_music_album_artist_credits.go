package migrations

import (
	"fmt"
	"strings"

	"atoman/internal/model"

	"gorm.io/gorm"
)

var albumArtistCreditPrimaryKey = []string{"album_id", "artist_id", "role", "custom_role"}

// RunMusicAlbumArtistCreditsMigration allows one artist to hold multiple album roles.
func RunMusicAlbumArtistCreditsMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.AlbumArtist{}) {
		return db.AutoMigrate(&model.AlbumArtist{})
	}
	if !db.Migrator().HasColumn(&model.AlbumArtist{}, "custom_role") {
		if err := db.Migrator().AddColumn(&model.AlbumArtist{}, "CustomRole"); err != nil {
			return fmt.Errorf("add album artist custom role: %w", err)
		}
	}
	if err := db.Exec("UPDATE album_artists SET role = 'primary' WHERE role IS NULL OR TRIM(role) = ''").Error; err != nil {
		return fmt.Errorf("backfill album artist role: %w", err)
	}
	if err := db.Exec("UPDATE album_artists SET custom_role = '' WHERE custom_role IS NULL").Error; err != nil {
		return fmt.Errorf("backfill album artist custom role: %w", err)
	}
	if err := db.Exec("UPDATE album_artists SET position = 1 WHERE position IS NULL OR position <= 0").Error; err != nil {
		return fmt.Errorf("backfill album artist position: %w", err)
	}

	switch db.Dialector.Name() {
	case "sqlite":
		return migrateSQLiteAlbumArtistCredits(db)
	case "postgres":
		return migratePostgresAlbumArtistCredits(db)
	default:
		return db.AutoMigrate(&model.AlbumArtist{})
	}
}

func migrateSQLiteAlbumArtistCredits(db *gorm.DB) error {
	columns, err := sqliteAlbumArtistPrimaryKey(db)
	if err != nil {
		return err
	}
	if sameColumnList(columns, albumArtistCreditPrimaryKey) {
		return db.Exec("CREATE INDEX IF NOT EXISTS idx_album_artists_artist_id ON album_artists (artist_id)").Error
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`CREATE TABLE album_artists_credit_migration (
			album_id uuid NOT NULL,
			artist_id uuid NOT NULL,
			role text NOT NULL DEFAULT 'primary',
			custom_role text NOT NULL DEFAULT '',
			position integer NOT NULL DEFAULT 1,
			created_at datetime,
			updated_at datetime,
			PRIMARY KEY (album_id, artist_id, role, custom_role),
			CONSTRAINT fk_album_artists_album FOREIGN KEY (album_id) REFERENCES "Albums"(id),
			CONSTRAINT fk_album_artists_artist FOREIGN KEY (artist_id) REFERENCES "Artists"(id)
		)`).Error; err != nil {
			return fmt.Errorf("create album artist credits table: %w", err)
		}
		if err := tx.Exec(`INSERT INTO album_artists_credit_migration
			(album_id, artist_id, role, custom_role, position, created_at, updated_at)
			SELECT album_id, artist_id, COALESCE(NULLIF(TRIM(role), ''), 'primary'),
				COALESCE(custom_role, ''), CASE WHEN position > 0 THEN position ELSE 1 END,
				created_at, updated_at
			FROM album_artists`).Error; err != nil {
			return fmt.Errorf("copy album artist credits: %w", err)
		}
		if err := tx.Exec("DROP TABLE album_artists").Error; err != nil {
			return fmt.Errorf("drop legacy album artists: %w", err)
		}
		if err := tx.Exec("ALTER TABLE album_artists_credit_migration RENAME TO album_artists").Error; err != nil {
			return fmt.Errorf("rename album artist credits: %w", err)
		}
		return tx.Exec("CREATE INDEX IF NOT EXISTS idx_album_artists_artist_id ON album_artists (artist_id)").Error
	})
}

func migratePostgresAlbumArtistCredits(db *gorm.DB) error {
	var constraints []struct {
		ConstraintName string `gorm:"column:constraint_name"`
		ColumnName     string `gorm:"column:column_name"`
	}
	if err := db.Raw(`SELECT tc.constraint_name, kcu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON tc.constraint_name = kcu.constraint_name AND tc.table_schema = kcu.table_schema
		WHERE tc.table_schema = current_schema()
		  AND tc.table_name = 'album_artists'
		  AND tc.constraint_type = 'PRIMARY KEY'
		ORDER BY kcu.ordinal_position`).Scan(&constraints).Error; err != nil {
		return fmt.Errorf("inspect album artist primary key: %w", err)
	}
	columns := make([]string, 0, len(constraints))
	for _, constraint := range constraints {
		columns = append(columns, constraint.ColumnName)
	}
	if !sameColumnList(columns, albumArtistCreditPrimaryKey) {
		if len(constraints) > 0 {
			constraintName := strings.ReplaceAll(constraints[0].ConstraintName, `"`, `""`)
			if err := db.Exec(`ALTER TABLE album_artists DROP CONSTRAINT "` + constraintName + `"`).Error; err != nil {
				return fmt.Errorf("drop album artist primary key: %w", err)
			}
		}
		if err := db.Exec(`ALTER TABLE album_artists
			ALTER COLUMN role SET DEFAULT 'primary',
			ALTER COLUMN role SET NOT NULL,
			ALTER COLUMN custom_role SET DEFAULT '',
			ALTER COLUMN custom_role SET NOT NULL,
			ADD PRIMARY KEY (album_id, artist_id, role, custom_role)`).Error; err != nil {
			return fmt.Errorf("create album artist credit primary key: %w", err)
		}
	}
	return db.Exec("CREATE INDEX IF NOT EXISTS idx_album_artists_artist_id ON album_artists (artist_id)").Error
}

func sqliteAlbumArtistPrimaryKey(db *gorm.DB) ([]string, error) {
	var rows []struct {
		Name string `gorm:"column:name"`
		PK   int    `gorm:"column:pk"`
	}
	if err := db.Raw("PRAGMA table_info(album_artists)").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("inspect album artist primary key: %w", err)
	}
	columns := make([]string, len(albumArtistCreditPrimaryKey))
	for _, row := range rows {
		if row.PK > 0 && row.PK <= len(columns) {
			columns[row.PK-1] = row.Name
		}
	}
	for len(columns) > 0 && columns[len(columns)-1] == "" {
		columns = columns[:len(columns)-1]
	}
	return columns, nil
}

func sameColumnList(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
