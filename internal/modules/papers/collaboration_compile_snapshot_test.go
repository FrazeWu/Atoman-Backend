package papers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Exercise the existing trusted queue and named snapshot APIs against
// actual Yjs updates, including changes still being merged outside the
// committing transaction. No new compile or snapshot authorization path.
func TestDurableCollabCompileAndSnapshotRevisionFences(t *testing.T) {
	e := testCollabEngine(t)
	f := newCompileHTTPFixture(t, true)
	owner := authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}
	editor := authctx.CurrentUser{ID: f.other.UUID, Role: authctx.RoleUser}
	pid, fid := f.project.ID, f.project.MainFileID
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_, err := f.svc.SetMember(owner, pid, editor.ID, RoleProjectEditor)
	require.NoError(t, err)
	chapter, err := f.svc.CreateFile(owner, pid, "chapter.tex", "original chapter")
	require.NoError(t, err)
	store := &collaborationStore{service: f.svc, engine: e}
	initial, err := store.Open(ctx, owner, pid, fid)
	require.NoError(t, err)
	second, err := store.Open(ctx, editor, pid, chapter.ID)
	require.NoError(t, err)
	require.Equal(t, initial.ProjectRevision, second.ProjectRevision)
	// One editor is preparing a merge, not yet visible to the canonical DB.
	pending := peerUpdate(t, second.Checkpoint.State, 0, 0, "accepted later ")
	barrier := mergeBarrier{e: e, ready: make(chan struct{}, 1), release: make(chan struct{})}
	racing := &collaborationStore{service: f.svc, engine: barrier}
	result := make(chan error, 1)
	released := false
	defer func() {
		if !released {
			close(barrier.release)
		}
	}()
	go func() { _, err := racing.Apply(ctx, editor, pid, chapter.ID, second.Epoch, pending); result <- err }()
	select {
	case <-barrier.ready:
	case <-ctx.Done():
		t.Fatal("merge barrier not reached")
	}
	checkpoint, err := f.svc.CreateProjectSnapshot(owner, pid, "Before accepted merge", initial.ProjectRevision)
	require.NoError(t, err)
	require.Equal(t, initial.ProjectRevision, checkpoint.SourceRevision)
	require.Equal(t, 404, f.request("POST", f.base()+"/compiles", fmt.Sprintf(`{"base_revision":%d,"idempotency_key":"editor-no-compile"}`, initial.ProjectRevision), f.otherToken).Code)
	require.Equal(t, 404, f.request("POST", f.base()+"/snapshots", fmt.Sprintf(`{"name":"editor-no-snapshot","base_revision":%d}`, initial.ProjectRevision), f.otherToken).Code)
	job, err := f.svc.SubmitCompile(ctx, owner, pid, initial.ProjectRevision, "before-accepted-merge", "pdflatex")
	require.NoError(t, err)
	indexed, found, err := f.queue.FindByIdempotencyKey(ctx, owner.ID, pid, "before-accepted-merge")
	require.NoError(t, err)
	require.True(t, found)
	frozen, err := f.publisher.store.LoadSnapshot(indexed.SnapshotDigest)
	require.NoError(t, err)
	require.Equal(t, initial.ProjectRevision, frozen.Revision)
	sources := map[string]string{}
	for _, file := range frozen.Files {
		sources[file.Path] = file.Content
	}
	require.Equal(t, "original chapter", sources["chapter.tex"])
	require.Equal(t, initial.Checkpoint.Text, sources["main.tex"])
	close(barrier.release)
	released = true
	require.NoError(t, <-result)
	latest, err := store.Read(ctx, owner, pid, fid, initial.Epoch)
	require.NoError(t, err)
	require.Greater(t, latest.ProjectRevision, initial.ProjectRevision)
	require.Equal(t, initial.FileRevision, latest.FileRevision)
	require.Equal(t, initial.Sequence, latest.Sequence)
	_, err = f.svc.CreateProjectSnapshot(owner, pid, "Stale", initial.ProjectRevision)
	collabCode(t, err, "papers.revision_conflict")
	// Clear the queue admission gate before testing a stale new submission.
	require.Equal(t, http.StatusOK, f.request("DELETE", f.base()+"/compiles/"+job.ID.String(), "", f.ownerToken).Code)
	beforePublish := f.publisher.calls
	_, err = f.svc.SubmitCompile(ctx, owner, pid, initial.ProjectRevision, "stale-new-compile", "pdflatex")
	collabCode(t, err, "papers.revision_conflict")
	require.Equal(t, beforePublish, f.publisher.calls)
	duplicate, err := f.svc.SubmitCompile(ctx, owner, pid, initial.ProjectRevision, "before-accepted-merge", "pdflatex")
	require.NoError(t, err)
	require.Equal(t, job.ID, duplicate.ID)
	require.Equal(t, beforePublish, f.publisher.calls)
	// A collaborative commit during publication must not enqueue a new job
	// with a now-stale source revision.
	nextDelta := peerUpdate(t, latest.Checkpoint.State, 0, 0, "% committed during publication\n")
	f.publisher.before = func() {
		f.publisher.before = nil
		_, applyErr := store.Apply(ctx, editor, pid, fid, initial.Epoch, nextDelta)
		require.NoError(t, applyErr)
	}
	_, err = f.svc.SubmitCompile(ctx, owner, pid, latest.ProjectRevision, "publication-race", "pdflatex")
	require.Error(t, err)
	_, found, err = f.queue.FindByIdempotencyKey(ctx, owner.ID, pid, "publication-race")
	require.NoError(t, err)
	require.False(t, found)
	current, err := store.Read(ctx, owner, pid, fid, initial.Epoch)
	require.NoError(t, err)
	require.Greater(t, current.ProjectRevision, latest.ProjectRevision)
	// Restore through the normal owner API while another file is still open.
	require.NoError(t, f.svc.configureCollaboration(e))
	server := httptest.NewServer(f.api)
	defer server.Close()
	peer, _, err := dialGateway(t, server.URL, f.base()+"/files/"+chapter.ID.String()+"/collaboration", f.otherToken, second.Epoch)
	require.NoError(t, err)
	defer peer.Close()
	state := readGateway(t, peer)
	require.Equal(t, second.Epoch, state.Epoch)
	restored, err := f.svc.RestoreProjectSnapshot(owner, pid, checkpoint.ID, current.ProjectRevision)
	require.NoError(t, err)
	require.True(t, restored.Backup.Automatic)
	expectClose(t, peer, "papers.collab_epoch_stale")
	for _, old := range []struct {
		file, epoch uuid.UUID
		delta       []byte
	}{{fid, initial.Epoch, nextDelta}, {chapter.ID, second.Epoch, pending}} {
		_, err = store.Apply(ctx, editor, pid, old.file, old.epoch, old.delta)
		collabCode(t, err, "papers.collab_epoch_stale")
	}
	fresh, err := store.Open(ctx, owner, pid, chapter.ID)
	require.NoError(t, err)
	require.NotEqual(t, second.Epoch, fresh.Epoch)
	require.Equal(t, "original chapter", fresh.Checkpoint.Text)
	require.Equal(t, restored.Project.HeadRevision, fresh.ProjectRevision)
	again, err := f.publisher.store.LoadSnapshot(indexed.SnapshotDigest)
	require.NoError(t, err)
	require.Equal(t, frozen, again)
	var jobs int64
	require.NoError(t, f.db.Model(&model.PaperCompileJob{}).Count(&jobs).Error)
	require.EqualValues(t, 1, jobs)
}
