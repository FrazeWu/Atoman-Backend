package service

import (
	"testing"
	"time"
)

func TestLoadFullTextWorkerConfigDefaults(t *testing.T) {
	t.Setenv("FULLTEXT_WORKER_ENABLED", "")
	t.Setenv("FULLTEXT_WORKER_STARTUP_DELAY", "")
	t.Setenv("FULLTEXT_WORKER_INTERVAL", "")
	t.Setenv("FULLTEXT_WORKER_BATCH_SIZE", "")
	t.Setenv("FULLTEXT_WORKER_CONCURRENCY", "")

	cfg := loadFullTextWorkerConfig()
	if !cfg.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if cfg.StartupDelay != 120*time.Second {
		t.Fatalf("StartupDelay = %v, want 120s", cfg.StartupDelay)
	}
	if cfg.Interval != 2*time.Minute {
		t.Fatalf("Interval = %v, want 2m", cfg.Interval)
	}
	if cfg.BatchSize != fullTextWorkerBatchSize {
		t.Fatalf("BatchSize = %d, want %d", cfg.BatchSize, fullTextWorkerBatchSize)
	}
	if cfg.Concurrency != FullTextWorkerConcurrency {
		t.Fatalf("Concurrency = %d, want %d", cfg.Concurrency, FullTextWorkerConcurrency)
	}
}

func TestLoadFullTextWorkerConfigOverrides(t *testing.T) {
	t.Setenv("FULLTEXT_WORKER_ENABLED", "false")
	t.Setenv("FULLTEXT_WORKER_STARTUP_DELAY", "10s")
	t.Setenv("FULLTEXT_WORKER_INTERVAL", "5m")
	t.Setenv("FULLTEXT_WORKER_BATCH_SIZE", "2")
	t.Setenv("FULLTEXT_WORKER_CONCURRENCY", "3")

	cfg := loadFullTextWorkerConfig()
	if cfg.Enabled {
		t.Fatal("Enabled = true, want false")
	}
	if cfg.StartupDelay != 10*time.Second {
		t.Fatalf("StartupDelay = %v, want 10s", cfg.StartupDelay)
	}
	if cfg.Interval != 5*time.Minute {
		t.Fatalf("Interval = %v, want 5m", cfg.Interval)
	}
	if cfg.BatchSize != 2 {
		t.Fatalf("BatchSize = %d, want 2", cfg.BatchSize)
	}
	if cfg.Concurrency != 3 {
		t.Fatalf("Concurrency = %d, want 3", cfg.Concurrency)
	}
}

func TestLoadFullTextWorkerConfigInvalidFallsBack(t *testing.T) {
	t.Setenv("FULLTEXT_WORKER_ENABLED", "bad")
	t.Setenv("FULLTEXT_WORKER_STARTUP_DELAY", "bad")
	t.Setenv("FULLTEXT_WORKER_INTERVAL", "0s")
	t.Setenv("FULLTEXT_WORKER_BATCH_SIZE", "0")
	t.Setenv("FULLTEXT_WORKER_CONCURRENCY", "0")

	cfg := loadFullTextWorkerConfig()
	if !cfg.Enabled {
		t.Fatal("Enabled = false, want fallback true")
	}
	if cfg.StartupDelay != 120*time.Second {
		t.Fatalf("StartupDelay = %v, want fallback 120s", cfg.StartupDelay)
	}
	if cfg.Interval != 2*time.Minute {
		t.Fatalf("Interval = %v, want fallback 2m", cfg.Interval)
	}
	if cfg.BatchSize != fullTextWorkerBatchSize {
		t.Fatalf("BatchSize = %d, want fallback %d", cfg.BatchSize, fullTextWorkerBatchSize)
	}
	if cfg.Concurrency != FullTextWorkerConcurrency {
		t.Fatalf("Concurrency = %d, want fallback %d", cfg.Concurrency, FullTextWorkerConcurrency)
	}
}

func TestFullTextBatchSizeForSettingsUsesManagedBatchWithinEnvironmentCeiling(t *testing.T) {
	settings := DefaultFeedFullTextSettings()
	settings.ReaderCrawlBatchSize = 40
	if got := fullTextBatchSizeForSettings(fullTextWorkerConfig{BatchSize: 100}, settings); got != 40 {
		t.Fatalf("managed batch size = %d, want 40", got)
	}
	settings.ReaderCrawlBatchSize = 200
	if got := fullTextBatchSizeForSettings(fullTextWorkerConfig{BatchSize: 100}, settings); got != 100 {
		t.Fatalf("capped batch size = %d, want 100", got)
	}
}
