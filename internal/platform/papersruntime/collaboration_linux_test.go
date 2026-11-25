//go:build linux

package papersruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"atoman/internal/modules/papers"
	"github.com/stretchr/testify/require"
)

func TestCollaborationApprovalSchemaAndFileSafety(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, collaborationCredential)
	require.ErrorIs(t, readCollaborationApproval(dir), ErrConfig)
	cases := []struct {
		body  string
		valid bool
	}{
		{`{"approved":true,"protocol":"papers-crdt-v1"}`, true},
		{`{"approved":false,"protocol":"papers-crdt-v1"}`, false},
		{`{"approved":true,"protocol":"other"}`, false},
		{`{"approved":true,"protocol":"papers-crdt-v1","socket":"/tmp/forged.sock"}`, false},
		{`{"approved":true,"protocol":"papers-crdt-v1"}{}`, false},
		{`{}`, false},
	}
	for _, c := range cases {
		require.NoError(t, os.WriteFile(path, []byte(c.body), 0600))
		err := readCollaborationApproval(dir)
		if c.valid {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, ErrConfig)
		}
	}
	require.NoError(t, os.WriteFile(path, []byte(cases[0].body), 0600))
	require.NoError(t, os.Chmod(path, 0666))
	require.ErrorIs(t, readCollaborationApproval(dir), ErrConfig)
	require.NoError(t, os.Remove(path))
	target := filepath.Join(dir, "target")
	require.NoError(t, os.WriteFile(target, []byte(cases[0].body), 0600))
	require.NoError(t, os.Symlink(target, path))
	require.ErrorIs(t, readCollaborationApproval(dir), ErrConfig)
}
func TestCollaborationDefaultOffAndIndependentFailure(t *testing.T) {
	t.Setenv("PAPERS_COMPILE_ENABLE", "")
	t.Setenv("PAPERS_COLLABORATION_ENABLE", "")
	require.NoError(t, configureApprovedCollaboration(nil, nil))
	svc, done, err := APIWithWorker(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, svc)
	select {
	case <-done:
	default:
		t.Fatal("unexpected worker")
	}
	// Even a syntactically valid local credential must not substitute for
	// a root-owned systemd credential mount.
	dir := t.TempDir()
	t.Setenv("CREDENTIALS_DIRECTORY", dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, collaborationCredential), []byte(`{"approved":true,"protocol":"papers-crdt-v1"}`), 0600))
	t.Setenv("PAPERS_COLLABORATION_ENABLE", "1")
	require.ErrorIs(t, configureApprovedCollaboration(context.Background(), papers.NewService(nil)), ErrConfig)
	svc, done, err = APIWithWorker(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, svc)
	select {
	case <-done:
	default:
		t.Fatal("unexpected worker")
	}
	t.Setenv("PAPERS_COMPILE_ENABLE", "1")
	svc, _, err = APIWithWorker(context.Background(), nil)
	require.ErrorIs(t, err, ErrConfig)
	require.NotNil(t, svc)
}
