package service

import (
	"testing"
	"time"
)

func TestLoadRSSCronConfigDefaults(t *testing.T) {
	t.Setenv("RSS_CRON_ENABLED", "")
	t.Setenv("RSS_CRON_STARTUP_DELAY", "")
	t.Setenv("RSS_CRON_INTERVAL", "")

	cfg := loadRSSCronConfig()
	if !cfg.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if cfg.StartupDelay != 60*time.Second {
		t.Fatalf("StartupDelay = %v, want 60s", cfg.StartupDelay)
	}
	if cfg.Interval != 15*time.Minute {
		t.Fatalf("Interval = %v, want 15m", cfg.Interval)
	}
}

func TestLoadRSSCronConfigOverrides(t *testing.T) {
	t.Setenv("RSS_CRON_ENABLED", "false")
	t.Setenv("RSS_CRON_STARTUP_DELAY", "5s")
	t.Setenv("RSS_CRON_INTERVAL", "30m")

	cfg := loadRSSCronConfig()
	if cfg.Enabled {
		t.Fatal("Enabled = true, want false")
	}
	if cfg.StartupDelay != 5*time.Second {
		t.Fatalf("StartupDelay = %v, want 5s", cfg.StartupDelay)
	}
	if cfg.Interval != 30*time.Minute {
		t.Fatalf("Interval = %v, want 30m", cfg.Interval)
	}
}

func TestLoadRSSCronConfigInvalidFallsBack(t *testing.T) {
	t.Setenv("RSS_CRON_ENABLED", "not-a-bool")
	t.Setenv("RSS_CRON_STARTUP_DELAY", "bad")
	t.Setenv("RSS_CRON_INTERVAL", "0s")

	cfg := loadRSSCronConfig()
	if !cfg.Enabled {
		t.Fatal("Enabled = false, want fallback true")
	}
	if cfg.StartupDelay != 60*time.Second {
		t.Fatalf("StartupDelay = %v, want fallback 60s", cfg.StartupDelay)
	}
	if cfg.Interval != 15*time.Minute {
		t.Fatalf("Interval = %v, want fallback 15m", cfg.Interval)
	}
}
