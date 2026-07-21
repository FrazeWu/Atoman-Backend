package main

import (
	"context"
	"testing"
	"time"

	"atoman/internal/modules/music"
)

type fakeWorkerRunner struct{ calls int }

func (w *fakeWorkerRunner) RunOnce(context.Context, music.ImportProcessor) (bool, error) {
	w.calls++
	return false, nil
}

func TestWorkerIDFromEnvUsesExplicitValue(t *testing.T) {
	t.Setenv("MUSIC_IMPORT_WORKER_ID", "  import-host-a  ")
	if got := workerIDFromEnv(); got != "import-host-a" {
		t.Fatalf("worker id = %q", got)
	}
}

func TestRunWorkerWithoutProcessorWaitsInsteadOfReturningError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	worker := &fakeWorkerRunner{}
	if err := runWorker(ctx, worker, nil, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if worker.calls != 1 {
		t.Fatalf("RunOnce calls = %d", worker.calls)
	}
}

func TestWorkerPollIntervalFromEnv(t *testing.T) {
	t.Setenv("MUSIC_IMPORT_WORKER_POLL_INTERVAL", "750ms")
	if got := workerPollIntervalFromEnv(); got != 750*time.Millisecond {
		t.Fatalf("poll interval = %s", got)
	}
}

func TestRequiredWorkerConfigRejectsMissingDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if err := requiredWorkerConfig(); err == nil {
		t.Fatal("expected DATABASE_URL validation error")
	}
}
