package papers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/paperscrdt"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func testCollabEngine(t *testing.T) collabEngine {
	t.Helper()
	if socket := os.Getenv("PAPERS_CRDT_TEST_SOCKET"); socket != "" {
		return testIsolatedCollabEngine(t, socket)
	}
	runtime := os.Getenv("PAPERS_CRDT_TEST_RUNTIME")
	if runtime == "" {
		t.Skip("Run scripts/test-papers-crdt.sh for actual Yjs integration")
	}
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	node, err = filepath.Abs(node)
	require.NoError(t, err)
	e, err := paperscrdt.New(node, filepath.Join(runtime, "cli.mjs"))
	require.NoError(t, err)
	return e
}
func peerUpdate(t *testing.T, state []byte, pos, remove int, insert string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"state": state, "position": pos, "remove": remove, "insert": insert})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join(os.Getenv("PAPERS_CRDT_TEST_RUNTIME"), "test-peer.mjs"))
	cmd.Stdin = bytes.NewReader(data)
	raw, err := cmd.Output()
	require.NoError(t, err)
	out, err := base64.StdEncoding.DecodeString(string(raw))
	require.NoError(t, err)
	return out
}
func collabCode(t *testing.T, err error, code string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, code, apperr.FromError(err).Code)
}

type mergeBarrier struct {
	e       collabEngine
	ready   chan struct{}
	release chan struct{}
}

func (b mergeBarrier) Run(ctx context.Context, r paperscrdt.Request) (paperscrdt.Checkpoint, error) {
	cp, err := b.e.Run(ctx, r)
	if err != nil || r.Op != "merge" {
		return cp, err
	}
	select {
	case b.ready <- struct{}{}:
	case <-ctx.Done():
		return cp, ctx.Err()
	}
	select {
	case <-b.release:
		return cp, nil
	case <-ctx.Done():
		return cp, ctx.Err()
	}
}
func TestDurableCollabConcurrentUnicodeAndRestart(t *testing.T) {
	e := testCollabEngine(t)
	f := newCompileHTTPFixture(t, false)
	owner := authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}
	editor := authctx.CurrentUser{ID: f.other.UUID, Role: authctx.RoleUser}
	pid, fid := f.project.ID, f.project.MainFileID
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := f.svc.SetMember(owner, pid, editor.ID, RoleProjectEditor)
	require.NoError(t, err)
	original := "alpha " + string(rune(0x4e2d)) + string(rune(0x6587)) + string(rune(0x1f600))
	saved, err := f.svc.SaveFile(owner, pid, fid, original, 1)
	require.NoError(t, err)
	store := &collaborationStore{service: f.svc, engine: e}
	seed, err := store.Open(ctx, owner, pid, fid)
	require.NoError(t, err)
	require.Equal(t, saved.Revision, seed.FileRevision)
	again, err := store.Open(ctx, editor, pid, fid)
	require.NoError(t, err)
	require.Equal(t, seed, again)
	a := peerUpdate(t, seed.Checkpoint.State, 0, 0, "A ")
	b := peerUpdate(t, seed.Checkpoint.State, 0, 0, "B ")
	barrier := mergeBarrier{e: e, ready: make(chan struct{}, 2), release: make(chan struct{})}
	racing := &collaborationStore{service: f.svc, engine: barrier}
	type result struct {
		user   authctx.CurrentUser
		update []byte
		state  collaborationState
		err    error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, r := range []result{{user: owner, update: a}, {user: editor, update: b}} {
		wg.Add(1)
		go func(r result) {
			defer wg.Done()
			r.state, r.err = racing.Apply(ctx, r.user, pid, fid, seed.Epoch, r.update)
			results <- r
		}(r)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-barrier.ready:
		case <-ctx.Done():
			close(barrier.release)
			wg.Wait()
			t.Fatal("merge did not reach barrier")
		}
	}
	close(barrier.release)
	wg.Wait()
	var loser result
	winners := 0
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			collabCode(t, r.err, "papers.collab_retry")
			loser = r
		} else {
			winners++
			require.Equal(t, seed.Sequence+1, r.state.Sequence)
		}
	}
	require.Equal(t, 1, winners)
	final, err := store.Apply(ctx, loser.user, pid, fid, seed.Epoch, loser.update)
	require.NoError(t, err)
	require.Equal(t, seed.Sequence+2, final.Sequence)
	require.Equal(t, seed.FileRevision+2, final.FileRevision)
	require.Contains(t, final.Checkpoint.Text, original)
	require.Contains(t, final.Checkpoint.Text, "A ")
	require.Contains(t, final.Checkpoint.Text, "B ")
	require.True(t, final.Checkpoint.Text == "A B "+original || final.Checkpoint.Text == "B A "+original)
	repeated, err := store.Apply(ctx, loser.user, pid, fid, seed.Epoch, loser.update)
	require.NoError(t, err)
	require.Equal(t, final, repeated)
	restarted := &collaborationStore{service: NewService(f.db), engine: testCollabEngine(t)}
	reloaded, err := restarted.Read(ctx, editor, pid, fid, seed.Epoch)
	require.NoError(t, err)
	require.Equal(t, final, reloaded)
	decoded, err := e.Run(ctx, paperscrdt.Request{Op: "read", Text: final.Checkpoint.Text, State: final.Checkpoint.State})
	require.NoError(t, err)
	require.Equal(t, final.Checkpoint, decoded)
	current, err := f.svc.GetFile(owner, pid, fid)
	require.NoError(t, err)
	require.Equal(t, final.Checkpoint.Text, current.Content)
	deleted := peerUpdate(t, final.Checkpoint.State, 0, 2, "")
	after, err := restarted.Apply(ctx, editor, pid, fid, seed.Epoch, deleted)
	require.NoError(t, err)
	require.Equal(t, final.Checkpoint.Vector, after.Checkpoint.Vector)
	require.NotEqual(t, final.Checkpoint.State, after.Checkpoint.State)
	require.Equal(t, final.FileRevision+1, after.FileRevision)
	var version model.PaperFileVersion
	require.NoError(t, f.db.Where("file_id=? AND revision=?", fid, after.FileRevision).First(&version).Error)
	require.Equal(t, editor.ID, version.AuthorID)
	require.Equal(t, after.Checkpoint.Text, version.Content)
}

func TestDurableCollabPermissionFencesAndSnapshotRestore(t *testing.T) {
	e := testCollabEngine(t)
	f := newCompileHTTPFixture(t, false)
	owner := authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}
	other := authctx.CurrentUser{ID: f.other.UUID, Role: authctx.RoleUser}
	pid, fid := f.project.ID, f.project.MainFileID
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := &collaborationStore{service: f.svc, engine: e}
	seed, err := store.Open(ctx, owner, pid, fid)
	require.NoError(t, err)
	delta := peerUpdate(t, seed.Checkpoint.State, 0, 0, "new ")
	_, err = store.Read(ctx, other, pid, fid, seed.Epoch)
	collabCode(t, err, "papers.project_not_found")
	_, err = f.svc.SetMember(owner, pid, other.ID, RoleProjectViewer)
	require.NoError(t, err)
	viewer, err := store.Read(ctx, other, pid, fid, seed.Epoch)
	require.NoError(t, err)
	require.Equal(t, seed, viewer)
	_, err = store.Apply(ctx, other, pid, fid, seed.Epoch, delta)
	collabCode(t, err, "papers.read_only")
	_, err = store.Open(ctx, other, pid, fid)
	collabCode(t, err, "papers.read_only")
	_, err = f.svc.SetMember(owner, pid, other.ID, RoleProjectEditor)
	require.NoError(t, err)
	barrier := mergeBarrier{e: e, ready: make(chan struct{}, 1), release: make(chan struct{})}
	racing := &collaborationStore{service: f.svc, engine: barrier}
	result := make(chan error, 1)
	go func() { _, err := racing.Apply(ctx, other, pid, fid, seed.Epoch, delta); result <- err }()
	select {
	case <-barrier.ready:
	case <-ctx.Done():
		close(barrier.release)
		t.Fatal("missing merge barrier")
	}
	_, err = f.svc.SetMember(owner, pid, other.ID, RoleProjectViewer)
	require.NoError(t, err)
	close(barrier.release)
	collabCode(t, <-result, "papers.read_only")
	unchanged, err := store.Read(ctx, owner, pid, fid, seed.Epoch)
	require.NoError(t, err)
	require.Equal(t, seed, unchanged)
	// Verify that checkpoint rollback reads the canonical accepted source, not
	// the prior Yjs checkpoint. Old updates cannot cross the restore epoch.
	snap, err := f.svc.CreateProjectSnapshot(owner, pid, "Before merge", seed.ProjectRevision)
	require.NoError(t, err)
	changed, err := store.Apply(ctx, owner, pid, fid, seed.Epoch, delta)
	require.NoError(t, err)
	restored, err := f.svc.RestoreProjectSnapshot(owner, pid, snap.ID, changed.ProjectRevision)
	require.NoError(t, err)
	_, err = store.Read(ctx, owner, pid, fid, seed.Epoch)
	collabCode(t, err, "papers.collab_epoch_stale")
	next, err := store.Open(ctx, owner, pid, fid)
	require.NoError(t, err)
	require.NotEqual(t, seed.Epoch, next.Epoch)
	require.Equal(t, seed.Checkpoint.Text, next.Checkpoint.Text)
	require.Equal(t, restored.Project.HeadRevision, next.ProjectRevision)
	_, err = store.Apply(ctx, owner, pid, fid, seed.Epoch, delta)
	collabCode(t, err, "papers.collab_epoch_stale")
	saved, err := f.svc.SaveFile(owner, pid, fid, "ordinary save", next.FileRevision)
	require.NoError(t, err)
	_, err = store.Apply(ctx, owner, pid, fid, next.Epoch, delta)
	collabCode(t, err, "papers.collab_epoch_stale")
	newer, err := store.Open(ctx, owner, pid, fid)
	require.NoError(t, err)
	require.Equal(t, saved.Content, newer.Checkpoint.Text)
	require.NoError(t, f.svc.RemoveMember(owner, pid, other.ID))
	_, err = store.Read(ctx, other, pid, fid, newer.Epoch)
	collabCode(t, err, "papers.project_not_found")
	require.NoError(t, f.svc.DeleteProject(owner, pid))
	_, err = f.svc.RestoreProject(owner, pid)
	require.NoError(t, err)
	_, err = store.Read(ctx, owner, pid, fid, newer.Epoch)
	collabCode(t, err, "papers.collab_epoch_stale")
	newRoom, err := store.Open(ctx, owner, pid, fid)
	require.NoError(t, err)
	require.NotEqual(t, newer.Epoch, newRoom.Epoch)
	require.Equal(t, newer.Checkpoint.Text, newRoom.Checkpoint.Text)
}

type failingCollabEngine struct{}

func (failingCollabEngine) Run(context.Context, paperscrdt.Request) (paperscrdt.Checkpoint, error) {
	return paperscrdt.Checkpoint{}, errors.New("private untrusted content")
}

func TestDurableCollabRejectsCorruptionAndRollsBackFailedCommit(t *testing.T) {
	e := testCollabEngine(t)
	f := newCompileHTTPFixture(t, false)
	owner := authctx.CurrentUser{ID: f.owner.UUID, Role: authctx.RoleUser}
	pid, fid := f.project.ID, f.project.MainFileID
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := &collaborationStore{service: f.svc, engine: e}
	seed, err := store.Open(ctx, owner, pid, fid)
	require.NoError(t, err)
	delta := peerUpdate(t, seed.Checkpoint.State, 0, 0, "new ")
	fail := &collaborationStore{service: f.svc, engine: failingCollabEngine{}}
	_, err = fail.Apply(ctx, owner, pid, fid, seed.Epoch, delta)
	collabCode(t, err, "papers.collab_unavailable")
	require.NotContains(t, err.Error(), "private untrusted content")
	_, err = store.Apply(ctx, owner, pid, fid, seed.Epoch, []byte{255, 255, 255})
	collabCode(t, err, "papers.collab_invalid_update")
	// Force the immutable-version insertion to fail after the canonical file
	// update has been attempted; verify that the whole transaction rolls back.
	current, err := f.svc.GetFile(owner, pid, fid)
	require.NoError(t, err)
	fake := fileVersion(current, owner.ID)
	fake.ID = uuid.New()
	fake.Revision++
	require.NoError(t, f.db.Create(&fake).Error)
	_, err = store.Apply(ctx, owner, pid, fid, seed.Epoch, delta)
	collabCode(t, err, "papers.collab_storage_failed")
	require.NotContains(t, err.Error(), current.Content)
	persisted, err := store.Read(ctx, owner, pid, fid, seed.Epoch)
	require.NoError(t, err)
	require.Equal(t, seed, persisted)
	unchanged, err := f.svc.GetFile(owner, pid, fid)
	require.NoError(t, err)
	require.Equal(t, current.Revision, unchanged.Revision)
	require.Equal(t, current.Content, unchanged.Content)
	require.NoError(t, f.db.Model(&model.PaperCollaborativeDocument{}).Where("file_id=?", fid).Update("state", []byte{1, 2, 3}).Error)
	_, err = store.Read(ctx, owner, pid, fid, seed.Epoch)
	collabCode(t, err, "papers.collab_checkpoint_invalid")
	_, err = store.Open(ctx, owner, pid, fid)
	collabCode(t, err, "papers.collab_checkpoint_invalid")
}
