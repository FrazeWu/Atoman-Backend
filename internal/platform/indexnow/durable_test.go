package indexnow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestDurableIndexNowRetriesAfterWorkerRestart(t *testing.T) {
	db := testdb.OpenPostgres(t, "indexnow_restart")
	testdb.Migrate(t, db, &model.IndexNowSubmission{})

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body submission
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.URLList) != 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	submitter, err := New(Config{Key: "test-key-12345678", SiteURL: "https://www.atoman.org", Endpoint: server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	first := &Worker{db: db, submitter: submitter}
	if err := first.enqueueDurable([]string{"/posts/post/safe", "/posts/post/safe"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	handled, err := first.runDurableOnce(ctx)
	if !handled || err == nil {
		t.Fatalf("expected first attempt to fail but remain queued: handled=%t err=%v", handled, err)
	}
	var pending model.IndexNowSubmission
	if err := db.First(&pending, "url = ?", "https://www.atoman.org/posts/post/safe").Error; err != nil {
		t.Fatal(err)
	}
	if pending.Attempts != 1 || pending.LeaseToken != "" {
		t.Fatalf("failed submission must be available for retry: %+v", pending)
	}

	// A new worker process sees the durable row without any in-memory queue.
	second := &Worker{db: db, submitter: submitter}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("restarted worker did not deliver the queued URL")
		}
		handled, err = second.runDurableOnce(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if handled {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	var count int64
	if err := db.Model(&model.IndexNowSubmission{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 || attempts.Load() != 2 {
		t.Fatalf("not acknowledged after restart: pending=%d attempts=%d", count, attempts.Load())
	}
}

func TestDurableIndexNowReenqueueWhileSubmittingPreservesNewRevision(t *testing.T) {
	db := testdb.OpenPostgres(t, "indexnow_merged_revision")
	testdb.Migrate(t, db, &model.IndexNowSubmission{})
	started, release := make(chan struct{}), make(chan struct{})
	var submissions atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if submissions.Add(1) == 1 {
			close(started)
			<-release
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	submitter, err := New(Config{Key: "test-key-12345678", SiteURL: "https://www.atoman.org", Endpoint: server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	first := &Worker{db: db, submitter: submitter}
	if err := first.enqueueDurable([]string{"/posts/post/updated"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	go func() {
		_, err := first.runDurableOnce(ctx)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(4 * time.Second):
		close(release)
		t.Fatal("first HTTP submission never started")
	}
	if err := first.enqueueDurable([]string{"/posts/post/updated"}); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("first submission did not finish")
	}
	var pending model.IndexNowSubmission
	url := "https://www.atoman.org/posts/post/updated"
	if err := db.First(&pending, "url = ?", url).Error; err != nil {
		t.Fatalf("new URL revision was lost: %v", err)
	}
	if pending.Revision != 2 || pending.LeaseToken != "" {
		t.Fatalf("re-enqueued update must be pending, got %+v", pending)
	}
	handled, err := (&Worker{db: db, submitter: submitter}).runDurableOnce(ctx)
	if err != nil || !handled {
		t.Fatalf("re-enqueued update not delivered: handled=%v err=%v", handled, err)
	}
	var count int64
	if err := db.Model(&model.IndexNowSubmission{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 || submissions.Load() != 2 {
		t.Fatalf("queued revision not acknowledged: rows=%d submissions=%d", count, submissions.Load())
	}
}

func TestDurableIndexNowKeepsNewRevisionEnqueuedDuringOldDelivery(t *testing.T) {
	db := testdb.OpenPostgres(t, "indexnow_revision")
	testdb.Migrate(t, db, &model.IndexNowSubmission{})

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			close(started)
			<-release
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	submitter, err := New(Config{
		Key: "test-key-12345678", SiteURL: "https://www.atoman.org", Endpoint: server.URL,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	first := &Worker{db: db, submitter: submitter}
	if err := first.enqueueDurable([]string{"/posts/post/fencing"}); err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		handled bool
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		handled, err := first.runDurableOnce(context.Background())
		done <- outcome{handled, err}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first submission never started")
	}

	// Change the same URL while the first HTTP request is in flight.
	if err := first.enqueueDurable([]string{"/posts/post/fencing"}); err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	select {
	case out := <-done:
		if out.err != nil || !out.handled {
			t.Fatalf("first submit failed: %+v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original submission did not finish")
	}
	var pending model.IndexNowSubmission
	if err := db.First(&pending, "url = ?", "https://www.atoman.org/posts/post/fencing").Error; err != nil {
		t.Fatalf("new revision was lost when old HTTP request finished: %v", err)
	}
	if pending.Revision != 2 || pending.LeaseToken != "" {
		t.Fatalf("new revision must remain queued: %+v", pending)
	}
	next := &Worker{db: db, submitter: submitter}
	handled, err := next.runDurableOnce(context.Background())
	if err != nil || !handled {
		t.Fatalf("new revision did not deliver: handled=%v err=%v", handled, err)
	}
	var count int64
	if err := db.Model(&model.IndexNowSubmission{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 || attempts.Load() != 2 {
		t.Fatalf("final acknowledgement failed: pending=%d requests=%d", count, attempts.Load())
	}
}
