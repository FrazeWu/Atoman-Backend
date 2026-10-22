package testdb

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// LocalPostgresDSN matches docker-compose.test.yml and never points at the dev database.
const LocalPostgresDSN = "postgres://atoman_test:atoman_test@127.0.0.1:55432/atoman_test?sslmode=disable"

// TestPostgresDSN prefers explicit test configuration, then the local test service.
func TestPostgresDSN() string {
	if dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN")); dsn != "" {
		return dsn
	}
	if dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")); dsn != "" {
		return dsn
	}
	if dsn := testPostgresDSNFromDevEnv(); dsn != "" {
		return dsn
	}
	return LocalPostgresDSN
}

func testPostgresDSNFromDevEnv() string {
	workingDir, err := os.Getwd()
	if err != nil {
		return ""
	}

	for dir := workingDir; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if dsn := testPostgresDSNFromEnvFile(filepath.Join(dir, ".env.dev")); dsn != "" {
				return dsn
			}
			if dsn := testPostgresDSNFromGitCommonRoot(dir); dsn != "" {
				return dsn
			}
			return ""
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

// A worktree has no .env.dev of its own. Its git common directory points back
// to the main checkout, where local-only environment files are kept.
func testPostgresDSNFromGitCommonRoot(worktreeRoot string) string {
	gitPath := filepath.Join(worktreeRoot, ".git")
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir:") {
		return ""
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktreeRoot, gitDir)
	}
	commonDirPath := filepath.Join(gitDir, "commondir")
	commonDir, err := os.ReadFile(commonDirPath)
	if err != nil {
		return ""
	}
	commonPath := strings.TrimSpace(string(commonDir))
	if !filepath.IsAbs(commonPath) {
		commonPath = filepath.Join(gitDir, commonPath)
	}
	return testPostgresDSNFromEnvFile(filepath.Join(filepath.Dir(commonPath), ".env.dev"))
}
