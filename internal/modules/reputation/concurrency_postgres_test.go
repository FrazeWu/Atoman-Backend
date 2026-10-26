package reputation

import (
	"context"
	"errors"
	"testing"
	"time"

	"atoman/internal/testdb"
)

func TestRunShadowRejectsConcurrentDatabaseOwner(t *testing.T) {
	db := testdb.OpenPostgres(t, "reputation_lock")
	owner := db.Begin()
	if owner.Error != nil {
		t.Fatal(owner.Error)
	}
	defer owner.Rollback()

	if err := owner.Exec("SELECT pg_advisory_xact_lock(?)", reputationRunLockKey).Error; err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := NewService(db).RunShadow(ctx, time.Now().UTC())
	if !errors.Is(err, ErrReputationRunInProgress) {
		t.Fatalf("concurrent run must be rejected with in-progress error, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("run did not reject the existing owner promptly")
	}

	if err := owner.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	var lock struct{ Acquired bool }
	if err := db.Raw("SELECT pg_try_advisory_xact_lock(?) AS acquired", reputationRunLockKey).Scan(&lock).Error; err != nil {
		t.Fatal(err)
	}
	if !lock.Acquired {
		t.Fatal("advisory transaction lock was not released")
	}
}
