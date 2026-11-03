package testdb

import (
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// validateTestDatabaseDSN rejects accidental writes to a non-test database
// before test setup creates or migrates schemas. Both URL and keyword DSNs
// are supported so that a deployment DSN cannot bypass this check.
func validateTestDatabaseDSN(dsn string) error {
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("invalid test PostgreSQL connection string")
	}
	name := strings.ToLower(strings.TrimSpace(cfg.Database))
	if name == "test" || strings.HasPrefix(name, "test_") || strings.HasSuffix(name, "_test") || strings.Contains(name, "_test_") {
		return nil
	}
	return fmt.Errorf("refusing to run tests against database %q: use a dedicated *_test database", name)
}
