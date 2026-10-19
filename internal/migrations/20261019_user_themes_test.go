package migrations

import (
	"testing"

	"atoman/internal/testdb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunUserThemesMigration(t *testing.T) {
	db := testdb.OpenPostgres(t, "user_themes_migration")

	// Ensure base users table exists for foreign key references if needed
	if err := db.Exec(`
CREATE TABLE IF NOT EXISTS users (
	uuid uuid PRIMARY KEY,
	id serial UNIQUE,
	username varchar(255) UNIQUE NOT NULL,
	email varchar(255) UNIQUE NOT NULL,
	password text NOT NULL,
	auth_version integer DEFAULT 0 NOT NULL,
	created_at timestamptz,
	updated_at timestamptz,
	deleted_at timestamptz
);
`).Error; err != nil {
		t.Fatalf("setup base users table: %v", err)
	}

	// First run
	err := RunUserThemesMigration(db)
	require.NoError(t, err)

	// Verify table exists
	assert.True(t, db.Migrator().HasTable("user_themes"), "expected user_themes table to exist")

	// Verify columns exist
	expectedColumns := []string{
		"id",
		"created_at",
		"updated_at",
		"deleted_at",
		"user_id",
		"is_enabled",
		"mode",
		"preset_config",
		"custom_css",
		"sanitized_css",
	}
	for _, col := range expectedColumns {
		assert.True(t, db.Migrator().HasColumn("user_themes", col), "expected column %s on user_themes", col)
	}

	// Verify unique index exists
	assert.True(t, db.Migrator().HasIndex("user_themes", "idx_user_themes_user_id"), "expected idx_user_themes_user_id index")

	// Idempotency: second run should also succeed without error
	err = RunUserThemesMigration(db)
	require.NoError(t, err)
}
