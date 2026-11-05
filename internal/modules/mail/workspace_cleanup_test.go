package mail

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type cleanupInterceptStore struct {
	MailPendingStore
	beforeDelete func(context.Context, string) error
}

func (s *cleanupInterceptStore) Delete(ctx context.Context, key string) error {
	if err := s.beforeDelete(ctx, key); err != nil {
		return err
	}
	return s.MailPendingStore.Delete(ctx, key)
}

func TestMailCleanupRetirementBlocksConcurrentDraftAndRetriesFailedDelete(t *testing.T) {
	s, sender, storage, alice, _ := workspaceFixture(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	upload, err := s.Upload(ctx, alice, "old.txt", "text/plain", strings.NewReader("old private attachment"))
	require.NoError(t, err)
	require.NoError(t, s.db.Model(&model.MailUpload{}).Where("id = ?", upload.ID).UpdateColumn("created_at", time.Now().Add(-31*24*time.Hour)).Error)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseDelete := func() { once.Do(func() { close(release) }) }
	defer releaseDelete()
	s.store = &cleanupInterceptStore{MailPendingStore: storage, beforeDelete: func(ctx context.Context, key string) error {
		close(entered)
		select {
		case <-release:
			return errors.New("synthetic transient object delete failure")
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	type answer struct {
		result MailMaintenanceResult
		err    error
	}
	done := make(chan answer, 1)
	go func() { r, e := s.RunMaintenance(ctx, nil); done <- answer{r, e} }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("cleanup did not reach object deletion")
	}
	// Object I/O is in flight. The committed retired marker prevents a dangling
	// reference even if this remote delete fails or the process dies immediately.
	draftID := uuid.New()
	_, err = s.SaveDraft(ctx, alice, draftID, SaveMailDraftInput{Compose: DraftCompose{Subject: "racing save", Attachments: []model.MailUpload{upload}}})
	assertAppCode(t, err, "mail.invalid_attachment")
	input := composeFixture()
	input.AttachmentIDs = []uuid.UUID{upload.ID}
	_, err = sender.Queue(ctx, alice, input)
	require.Error(t, err)
	var current model.MailUpload
	require.NoError(t, s.db.Where("id = ?", upload.ID).Take(&current).Error)
	require.NotNil(t, current.RetiredAt)
	releaseDelete()
	first := <-done
	require.NoError(t, first.err)
	require.Zero(t, first.result.ExpiredUploads)
	require.Equal(t, 1, storage.length())
	s.store = storage
	second, err := s.RunMaintenance(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, 1, second.ExpiredUploads)
	require.Zero(t, storage.length())
	var remaining int64
	require.NoError(t, s.db.Model(&model.MailUpload{}).Where("id = ?", upload.ID).Count(&remaining).Error)
	require.Zero(t, remaining)
}

func TestMailDraftSaveAndCleanupShareUserLockAndPreserveReferences(t *testing.T) {
	s, _, storage, alice, bob := workspaceFixture(t, nil)
	// The shared test fixture normally permits two connections. This lock test
	// needs a third for Bob while the lock owner and waiting save are active.
	sqlDB, err := s.db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	defer sqlDB.SetMaxOpenConns(2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	upload, err := s.Upload(ctx, alice, "referenced.txt", "text/plain", strings.NewReader("keep while referenced"))
	require.NoError(t, err)
	require.NoError(t, s.db.Model(&model.MailUpload{}).Where("id = ?", upload.ID).UpdateColumn("created_at", time.Now().Add(-31*24*time.Hour)).Error)
	tx := s.db.WithContext(ctx).Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	require.NoError(t, lockMailDraftArtifacts(tx, alice.ID))
	draftID := uuid.New()
	started, done := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		_, e := s.SaveDraft(ctx, alice, draftID, SaveMailDraftInput{Compose: DraftCompose{Subject: "keep", Attachments: []model.MailUpload{upload}}})
		done <- e
	}()
	<-started
	// Another user can save while Alice is serialized by the cleanup lock.
	_, err = s.SaveDraft(ctx, bob, uuid.New(), SaveMailDraftInput{Compose: DraftCompose{Subject: "independent user"}})
	require.NoError(t, err)
	select {
	case err := <-done:
		t.Fatalf("draft save ignored cleanup lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, tx.Rollback().Error)
	require.NoError(t, <-done)
	result, err := s.RunMaintenance(ctx, nil)
	require.NoError(t, err)
	require.Zero(t, result.ExpiredUploads)
	require.Equal(t, 1, storage.length())
	var current model.MailUpload
	require.NoError(t, s.db.Where("id = ?", upload.ID).Take(&current).Error)
	require.Nil(t, current.RetiredAt)
	require.NoError(t, s.DeleteDraft(ctx, alice, draftID))
	result, err = s.RunMaintenance(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.ExpiredUploads)
}

func TestMailCleanupTerminalSnapshotsAreBoundedAndPreserveActiveEvidence(t *testing.T) {
	s, _, storage, alice, _ := workspaceFixture(t, nil)
	ctx := context.Background()
	old := time.Now().UTC().Add(-31 * 24 * time.Hour)
	rows := make([]model.MailPendingSend, 0, 205)
	add := func(status string, when time.Time) {
		id := uuid.New()
		key := "synthetic-snapshot/" + id.String()
		rows = append(rows, model.MailPendingSend{ID: id, UserID: alice.ID, RequestID: uuid.New(), AccountID: "synthetic-account", SnapshotKey: key, PayloadHash: strings.Repeat("a", 64), Status: status, SendAfter: when, CreatedAt: when, UpdatedAt: when})
		require.NoError(t, storage.Put(ctx, key, []byte("encrypted synthetic snapshot")))
	}
	for i := 0; i < 201; i++ {
		add([]string{"sent", "cancelled", "blocked"}[i%3], old)
	}
	for _, state := range []string{"queued", "dispatching", "uncertain"} {
		add(state, old)
	}
	add("sent", time.Now().UTC())
	require.NoError(t, s.db.Create(&rows).Error)
	first, err := s.RunMaintenance(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, 200, first.ExpiredSnapshots)
	require.Equal(t, 5, storage.length())
	second, err := s.RunMaintenance(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, 1, second.ExpiredSnapshots)
	third, err := s.RunMaintenance(ctx, nil)
	require.NoError(t, err)
	require.Zero(t, third.ExpiredSnapshots)
	require.Equal(t, 4, storage.length())
	var marked int64
	require.NoError(t, s.db.Model(&model.MailPendingSend{}).Where("snapshot_deleted_at IS NOT NULL").Count(&marked).Error)
	require.EqualValues(t, 201, marked)
	for _, row := range rows[201:] {
		_, err := storage.Get(ctx, row.SnapshotKey)
		require.NoError(t, err, "queued, dispatching, uncertain, and recent terminal evidence must survive")
	}
}
