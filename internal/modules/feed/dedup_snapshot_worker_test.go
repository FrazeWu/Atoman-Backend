package feed

import (
	"context"
	"errors"
	"testing"
	"time"

	"atoman/internal/model"
)

func TestDedupWorkerBackoffBoundedAndResets(t *testing.T) {
	if got := dedupRetryDelay(2*time.Second, 9*time.Second, 1); got != 2*time.Second {
		t.Fatalf("first backoff: %s", got)
	}
	if got := dedupRetryDelay(2*time.Second, 9*time.Second, 2); got != 4*time.Second {
		t.Fatalf("second backoff: %s", got)
	}
	if got := dedupRetryDelay(2*time.Second, 9*time.Second, 99); got != 9*time.Second {
		t.Fatalf("capped backoff: %s", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Millisecond)
	defer cancel()
	var observed []PublicDedupRefreshAttempt
	attempts := 0
	err := runPublicRecentDedupRefreshWorker(ctx, PublicDedupRefreshWorkerOptions{
		PollInterval: time.Second, AttemptTimeout: 50 * time.Millisecond,
		RetryInitialDelay: 10 * time.Millisecond, RetryMaxDelay: 40 * time.Millisecond,
		OnAttempt: func(result PublicDedupRefreshAttempt) { observed = append(observed, result) },
	}, func(ctx context.Context, p PublicDedupRefreshPolicy) (PublicDedupRefreshResult, error) {
		if p.MinFullRebuildInterval != defaultDedupFullRebuildInterval {
			t.Errorf("missing worker rebuild cooldown")
		}
		attempts++
		if attempts <= 2 {
			return "", errors.New("transient postgres failure")
		}
		return PublicDedupRefreshCurrent, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("worker should stop with its parent: %v", err)
	}
	if attempts != 3 || len(observed) != 3 {
		t.Fatalf("worker did not recover: attempts=%d events=%d", attempts, len(observed))
	}
	if observed[0].ConsecutiveFailures != 1 || observed[0].NextDelay != 10*time.Millisecond ||
		observed[1].ConsecutiveFailures != 2 || observed[1].NextDelay != 20*time.Millisecond ||
		observed[2].ConsecutiveFailures != 0 || observed[2].NextDelay != time.Second {
		t.Fatalf("unexpected backoff/reset: %+v", observed)
	}
}

func TestDedupWorkerAttemptTimeoutAndRevisionConflict(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1250*time.Millisecond)
	defer cancel()
	var observed []PublicDedupRefreshAttempt
	attempts := 0
	err := runPublicRecentDedupRefreshWorker(ctx, PublicDedupRefreshWorkerOptions{
		PollInterval: time.Second, AttemptTimeout: 12 * time.Millisecond,
		RetryInitialDelay: 10 * time.Millisecond, RetryMaxDelay: 40 * time.Millisecond,
		OnAttempt: func(result PublicDedupRefreshAttempt) { observed = append(observed, result) },
	}, func(attemptCtx context.Context, _ PublicDedupRefreshPolicy) (PublicDedupRefreshResult, error) {
		attempts++
		if attempts == 1 {
			return "", ErrDedupSnapshotStale
		}
		<-attemptCtx.Done()
		return "", attemptCtx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("parent cancellation not observed: %v", err)
	}
	if attempts < 2 || len(observed) < 2 {
		t.Fatalf("worker did not retry timed-out attempt: %d", attempts)
	}
	if observed[0].Result != PublicDedupRefreshConflict || observed[0].ConsecutiveFailures != 0 ||
		observed[0].NextDelay != time.Second {
		t.Fatalf("revision conflict incorrectly counted as outage: %+v", observed[0])
	}
}

func TestDedupWorkerRejectsInvalidPolicies(t *testing.T) {
	cases := []PublicDedupRefreshWorkerOptions{
		{PollInterval: time.Millisecond},
		{PollInterval: time.Second, AttemptTimeout: -time.Second},
		{PollInterval: time.Second, RetryInitialDelay: 5 * time.Second, RetryMaxDelay: time.Second},
		{PollInterval: time.Second, RefreshPolicy: PublicDedupRefreshPolicy{MinFullRebuildInterval: -time.Second}},
	}
	for _, opts := range cases {
		if _, err := normalizeDedupWorkerOptions(opts); err == nil {
			t.Fatalf("accepted invalid option: %+v", opts)
		}
	}
}

func TestDedupRefreshCooldownDefersFullRebuild(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	first := model.FeedItem{FeedSourceID: source.ID, Title: "First story", Link: "https://cooldown.example/first", GUID: "first", PublishedAt: time.Now().UTC()}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if state, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{}); err != nil || state != PublicDedupRefreshRebuilt {
		t.Fatalf("initial build %s %v", state, err)
	}
	var before feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&before).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.FeedItem{}).Where("id = ?", first.ID).Update("title", "Updated story").Error; err != nil {
		t.Fatal(err)
	}
	cooldown := PublicDedupRefreshPolicy{MinFullRebuildInterval: time.Hour}
	if state, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), cooldown); err != nil || state != PublicDedupRefreshDeferred {
		t.Fatalf("full rebuild should defer: %s %v", state, err)
	}
	var after feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	if before.GenerationID != after.GenerationID {
		t.Fatal("cooldown must not publish a generation")
	}
	if _, _, err := repo.readPublicRecentDedupSnapshotPageTracked(context.Background(), 1, 10); !errors.Is(err, ErrDedupSnapshotStale) {
		t.Fatalf("deferred generation must remain stale: %v", err)
	}
	if state, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{}); err != nil || state != PublicDedupRefreshRebuilt {
		t.Fatalf("unrestricted rebuild failed: %s %v", state, err)
	}
}

func TestDedupRefreshCooldownAllowsProvenSafePrefix(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	first := model.FeedItem{FeedSourceID: source.ID, GUID: "old", Title: "Original climate journal article", Link: "https://cooldown.example/old", PublishedAt: time.Now().UTC()}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if state, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{}); err != nil || state != PublicDedupRefreshRebuilt {
		t.Fatalf("initial build %s %v", state, err)
	}
	newer := model.FeedItem{FeedSourceID: source.ID, GUID: "new", Title: "Brand new optical physics experiment", Link: "https://cooldown.example/new", PublishedAt: first.PublishedAt.Add(time.Hour)}
	if err := db.Create(&newer).Error; err != nil {
		t.Fatal(err)
	}
	if state, err := repo.refreshPublicRecentDedupIfQuiet(context.Background(), PublicDedupRefreshPolicy{MinFullRebuildInterval: time.Hour}); err != nil || state != PublicDedupRefreshIncremental {
		t.Fatalf("safe incremental must bypass full-rebuild cooldown: %s %v", state, err)
	}
	assertIncrementalMatchesReference(t, repo)
}
