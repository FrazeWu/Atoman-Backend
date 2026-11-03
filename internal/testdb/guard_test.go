package testdb

import (
	"strings"
	"testing"
)

func TestValidateTestDatabaseDSN(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dsn     string
		allowed bool
	}{
		{"isolated URL", "postgres://tester:password@127.0.0.1:5432/atoman_test?sslmode=disable", true},
		{"test database", "postgres://tester:password@127.0.0.1/test", true},
		{"prefixed test database", "host=localhost dbname=test_atoman user=tester", true},
		{"suffixed test database", "host=localhost dbname=atoman_test user=tester", true},
		{"production URL", "postgres://tester:password@127.0.0.1/atoman_prod", false},
		{"development URL", "postgres://tester:password@127.0.0.1/atoman_dev", false},
		{"production keyword DSN", "host=localhost dbname=atoman_prod user=tester", false},
		{"default PostgreSQL database", "host=localhost dbname=postgres user=tester", false},
		{"invalid DSN", "postgres://invalid%%%", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTestDatabaseDSN(tc.dsn)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v, error=%v", tc.allowed, err)
			}
			if err != nil && strings.Contains(err.Error(), "password") {
				t.Fatalf("error leaked credentials: %v", err)
			}
		})
	}
}
