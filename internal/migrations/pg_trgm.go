package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

// pgTrgmOperatorClass enables the pg_trgm extension and returns the
// schema-qualified gin operator class for trigram indexes.
//
// The extension is installed into public explicitly: without a target schema
// PostgreSQL installs extensions into the first entry of the current
// search_path, which under the test harness is a per-test temporary schema.
// The extension would then live and die with that schema, and concurrent
// tests (whose search_path does not include it) could not resolve
// gin_trgm_ops. Pinning public keeps the extension stable, and qualifying
// the operator class keeps the index DDL valid even when the extension was
// already installed somewhere else.
func pgTrgmOperatorClass(db *gorm.DB) (string, error) {
	if err := db.Exec(`CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA public`).Error; err != nil {
		return "", fmt.Errorf("enable pg_trgm extension: %w", err)
	}
	var extensionSchema string
	if err := db.Raw(`SELECT n.nspname
		FROM pg_extension e
		JOIN pg_namespace n ON n.oid = e.extnamespace
		WHERE e.extname = 'pg_trgm'`).Scan(&extensionSchema).Error; err != nil {
		return "", fmt.Errorf("resolve pg_trgm schema: %w", err)
	}
	if extensionSchema == "" {
		return "", fmt.Errorf("resolve pg_trgm schema: extension is not installed")
	}
	return quotePostgresIdentifier(extensionSchema) + ".gin_trgm_ops", nil
}
