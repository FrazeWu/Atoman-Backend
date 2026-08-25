package testdb

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// TestPostgresDSN returns the explicitly configured test connection. When tests
// run from a local checkout, it also reads Atoman-Backend/.env.dev so direct
// go test invocations share the project's development database configuration.
func TestPostgresDSN() string {
	if dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN")); dsn != "" {
		return dsn
	}
	if dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")); dsn != "" {
		return dsn
	}
	return testPostgresDSNFromDevEnv()
}

func testPostgresDSNFromDevEnv() string {
	workingDir, err := os.Getwd()
	if err != nil {
		return ""
	}

	for dir := workingDir; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return testPostgresDSNFromEnvFile(filepath.Join(dir, ".env.dev"))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
	}
}

func testPostgresDSNFromEnvFile(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	values := make(map[string]string, 2)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		if key != "TEST_POSTGRES_DSN" && key != "TEST_DATABASE_URL" {
			continue
		}
		values[key] = strings.Trim(strings.TrimSpace(value), `"'`)
	}

	if err := scanner.Err(); err != nil {
		return ""
	}
	if dsn := strings.TrimSpace(values["TEST_POSTGRES_DSN"]); dsn != "" {
		return dsn
	}
	return strings.TrimSpace(values["TEST_DATABASE_URL"])
}
