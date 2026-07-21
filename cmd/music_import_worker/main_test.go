package main

import "testing"

func TestWorkerIDFromEnvUsesExplicitValue(t *testing.T) {
	t.Setenv("MUSIC_IMPORT_WORKER_ID", "  import-host-a  ")
	if got := workerIDFromEnv(); got != "import-host-a" {
		t.Fatalf("worker id = %q", got)
	}
}

func TestRequiredWorkerConfigRejectsMissingDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if err := requiredWorkerConfig(); err == nil {
		t.Fatal("expected DATABASE_URL validation error")
	}
}
