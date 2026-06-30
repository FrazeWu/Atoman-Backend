package main

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestResolveLogDirDefaultsToDotSlashLog(t *testing.T) {
	t.Setenv("LOG_DIR", "")

	if got := resolveLogDir(); got != "./log" {
		t.Fatalf("expected ./log, got %q", got)
	}
}

func TestResolveLogDirUsesEnvironmentOverride(t *testing.T) {
	t.Setenv("LOG_DIR", "/app/logs")

	if got := resolveLogDir(); got != "/app/logs" {
		t.Fatalf("expected /app/logs, got %q", got)
	}
}

func TestSetupLoggingWritesToTerminalAndSplitFiles(t *testing.T) {
	oldLogWriter := log.Writer()
	oldLogFlags := log.Flags()
	oldGinWriter := gin.DefaultWriter
	oldGinErrorWriter := gin.DefaultErrorWriter
	t.Cleanup(func() {
		log.SetOutput(oldLogWriter)
		log.SetFlags(oldLogFlags)
		gin.DefaultWriter = oldGinWriter
		gin.DefaultErrorWriter = oldGinErrorWriter
	})

	dir := t.TempDir()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	logs, err := setupLogging(loggingConfig{Dir: dir, Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		t.Fatalf("setup logging: %v", err)
	}
	t.Cleanup(func() {
		if err := logs.Close(); err != nil {
			t.Fatalf("close logs: %v", err)
		}
	})

	log.Print("app event")
	_, _ = gin.DefaultWriter.Write([]byte("GET /health 200\n"))
	logs.FatalLogger.Print("fatal event")
	_, _ = gin.DefaultErrorWriter.Write([]byte("gin error\n"))

	assertFileContains(t, filepath.Join(dir, "app.log"), "app event")
	assertFileContains(t, filepath.Join(dir, "access.log"), "GET /health 200")
	assertFileContains(t, filepath.Join(dir, "error.log"), "fatal event")
	assertFileContains(t, filepath.Join(dir, "error.log"), "gin error")

	if !strings.Contains(stdout.String(), "app event") {
		t.Fatalf("expected stdout to contain app event, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "GET /health 200") {
		t.Fatalf("expected stdout to contain access log, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "fatal event") {
		t.Fatalf("expected stderr to contain fatal event, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "gin error") {
		t.Fatalf("expected stderr to contain gin error, got %q", stderr.String())
	}
}

func TestLoadEnvironmentBeforeLoggingAllowsEnvFileLogDir(t *testing.T) {
	workDir := t.TempDir()
	logDir := filepath.Join(workDir, "env-log")
	if err := os.WriteFile(filepath.Join(workDir, ".env.dev"), []byte("LOG_DIR="+logDir+"\n"), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldDir); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	oldLogDir, hadLogDir := os.LookupEnv("LOG_DIR")
	if err := os.Unsetenv("LOG_DIR"); err != nil {
		t.Fatalf("unset LOG_DIR: %v", err)
	}
	t.Cleanup(func() {
		if hadLogDir {
			if err := os.Setenv("LOG_DIR", oldLogDir); err != nil {
				t.Fatalf("restore LOG_DIR: %v", err)
			}
		} else if err := os.Unsetenv("LOG_DIR"); err != nil {
			t.Fatalf("clear LOG_DIR: %v", err)
		}
	})

	envMessage := loadEnvironment("dev")
	logs, err := setupLogging(loggingConfig{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("setup logging: %v", err)
	}
	log.Println(envMessage)
	t.Cleanup(func() {
		if err := logs.Close(); err != nil {
			t.Fatalf("close logs: %v", err)
		}
	})

	log.Print("env log dir")
	assertFileContains(t, filepath.Join(logDir, "app.log"), "Loaded .env.dev")
	assertFileContains(t, filepath.Join(logDir, "app.log"), "env log dir")
}

func TestLoadEnvironmentUsesExplicitEnvFile(t *testing.T) {
	workDir := t.TempDir()
	logDir := filepath.Join(workDir, "prod-log")
	if err := os.WriteFile(filepath.Join(workDir, ".env.prod"), []byte("LOG_DIR="+logDir+"\n"), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldDir); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("change working directory: %v", err)
	}

	if err := os.Unsetenv("LOG_DIR"); err != nil {
		t.Fatalf("unset LOG_DIR: %v", err)
	}

	envMessage := loadEnvironment("prod")
	if envMessage != "Loaded .env.prod" {
		t.Fatalf("expected prod env file message, got %q", envMessage)
	}
	if got := os.Getenv("LOG_DIR"); got != logDir {
		t.Fatalf("expected LOG_DIR from prod env file, got %q", got)
	}
}

func TestLoadEnvironmentDefaultsUnknownModeToDev(t *testing.T) {
	workDir := t.TempDir()
	logDir := filepath.Join(workDir, "dev-log")
	if err := os.WriteFile(filepath.Join(workDir, ".env.dev"), []byte("LOG_DIR="+logDir+"\n"), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldDir); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("change working directory: %v", err)
	}

	if err := os.Unsetenv("LOG_DIR"); err != nil {
		t.Fatalf("unset LOG_DIR: %v", err)
	}

	envMessage := loadEnvironment("staging")
	if envMessage != "Loaded .env.dev" {
		t.Fatalf("expected fallback dev env file message, got %q", envMessage)
	}
	if got := os.Getenv("LOG_DIR"); got != logDir {
		t.Fatalf("expected LOG_DIR from fallback dev env file, got %q", got)
	}
}

func TestSetupLoggingUsesResolveLogDirWhenDirIsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOG_DIR", dir)

	oldLogWriter := log.Writer()
	oldLogFlags := log.Flags()
	oldGinWriter := gin.DefaultWriter
	oldGinErrorWriter := gin.DefaultErrorWriter
	t.Cleanup(func() {
		log.SetOutput(oldLogWriter)
		log.SetFlags(oldLogFlags)
		gin.DefaultWriter = oldGinWriter
		gin.DefaultErrorWriter = oldGinErrorWriter
	})

	log.SetFlags(0)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	logs, err := setupLogging(loggingConfig{Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		t.Fatalf("setup logging: %v", err)
	}
	t.Cleanup(func() {
		if err := logs.Close(); err != nil {
			t.Fatalf("close logs: %v", err)
		}
	})

	logs.FatalLogger.Print("fatal event")
	assertFileContains(t, filepath.Join(dir, "error.log"), "fatal event")
	if strings.Contains(stderr.String(), "/") || strings.Contains(stderr.String(), "Jan ") {
		t.Fatalf("expected fatal logger to use current log flags, got %q", stderr.String())
	}
}

func TestSetupLoggingFailsWhenLogPathIsAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("occupied"), 0o644); err != nil {
		t.Fatalf("write occupied path: %v", err)
	}

	_, err := setupLogging(loggingConfig{Dir: path, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err == nil {
		t.Fatal("expected setup logging to fail")
	}
	if !errors.Is(err, errCreateLogDir) {
		t.Fatalf("expected errCreateLogDir, got %v", err)
	}
}

func TestDatabaseLogTargetRedactsCredentialsAndQuery(t *testing.T) {
	rawURL := "postgres://user:secret@db.example.com:5432/atoman?sslmode=require&password=leak"

	got := databaseLogTarget("postgres", rawURL)

	for _, leaked := range []string{"user", "secret", "sslmode", "password", "require", "leak", rawURL} {
		if strings.Contains(got, leaked) {
			t.Fatalf("databaseLogTarget leaked %q in %q", leaked, got)
		}
	}
	if !strings.Contains(got, "postgres") {
		t.Fatalf("databaseLogTarget() = %q, want database type", got)
	}
	if !strings.Contains(got, "host=db.example.com:5432") {
		t.Fatalf("databaseLogTarget() = %q, want host", got)
	}
	if !strings.Contains(got, "dbname=atoman") {
		t.Fatalf("databaseLogTarget() = %q, want dbname", got)
	}
}

func TestDatabaseConnectionLogRedactsURLSecrets(t *testing.T) {
	rawURL := "postgres://dbuser:dbsecret@db.example.com:5432/atoman?sslmode=require&password=leak"

	line := "Connecting to " + databaseLogTarget("postgres", rawURL)

	for _, leaked := range []string{"dbuser", "dbsecret", "sslmode", "password", "require", "leak", rawURL} {
		if strings.Contains(line, leaked) {
			t.Fatalf("database connection log leaked %q in %q", leaked, line)
		}
	}
	if !strings.Contains(line, "Connecting to postgres database") {
		t.Fatalf("database connection log = %q, want database type", line)
	}
	if !strings.Contains(line, "host=db.example.com:5432") {
		t.Fatalf("database connection log = %q, want host", line)
	}
	if !strings.Contains(line, "dbname=atoman") {
		t.Fatalf("database connection log = %q, want dbname", line)
	}
}

func TestDatabaseLogTargetRedactsKeywordDSN(t *testing.T) {
	rawURL := "host=localhost port=5432 user=atoman password=secret dbname=studio sslmode=disable"

	got := databaseLogTarget("postgres", rawURL)

	for _, leaked := range []string{"user", "atoman", "password", "secret", "sslmode", "disable", rawURL} {
		if strings.Contains(got, leaked) {
			t.Fatalf("databaseLogTarget leaked %q in %q", leaked, got)
		}
	}
	if !strings.Contains(got, "host=localhost:5432") {
		t.Fatalf("databaseLogTarget() = %q, want host with port", got)
	}
	if !strings.Contains(got, "dbname=studio") {
		t.Fatalf("databaseLogTarget() = %q, want dbname", got)
	}
}

func assertFileContains(t *testing.T, path string, want string) {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(content), want) {
		t.Fatalf("expected %s to contain %q, got %q", path, want, string(content))
	}
}
