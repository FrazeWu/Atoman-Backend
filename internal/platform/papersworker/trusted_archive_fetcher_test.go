package papersworker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"atoman/internal/platform/papersassets"
	"atoman/internal/platform/paperssandbox"
	"github.com/stretchr/testify/require"
)

type stubTrustedArchiveFetcher struct {
	path  string
	err   error
	got   string
	calls int
}

func (f *stubTrustedArchiveFetcher) FetchArchive(_ context.Context, envDigest string) (string, error) {
	f.calls++
	f.got = envDigest
	return f.path, f.err
}

func TestP0WorkerFetchesVerifiedArchiveByDBBoundDigest(t *testing.T) {
	f := newFixture(t)
	loader := &stubTrustedArchiveFetcher{path: f.archive}
	// A successful run proves the worker used the signed-cache return path
	// instead of silently falling back to its invalid local archive path.
	f.exec.cfg.ArchivePath = filepath.Join(f.root, "missing-p0-rootfs.tar")
	f.exec.cfg.ArchiveFetcher = loader
	result, err := f.exec.Run(context.Background(), f.claim)
	require.NoError(t, err)
	require.Equal(t, paperssandbox.Completed, result.Status)
	require.True(t, result.CleanupVerified)
	require.Equal(t, f.claim.Job.EnvironmentDigest, loader.got)
	require.Equal(t, 1, loader.calls)
}
func TestP0WorkerDoesNotFallBackWhenSignedCacheFails(t *testing.T) {
	f := newFixture(t)
	denied := errors.New("archive signature rejected")
	loader := &stubTrustedArchiveFetcher{err: denied}
	f.exec.cfg.ArchiveFetcher = loader
	result, err := f.exec.Run(context.Background(), f.claim)
	require.ErrorIs(t, err, denied)
	require.False(t, result.CleanupVerified)
	_, statErr := os.Stat(filepath.Join(f.root, "runs", f.claim.SandboxID))
	require.ErrorIs(t, statErr, os.ErrNotExist)
	require.Equal(t, 1, loader.calls)
}
func TestP0WorkerRejectsUnverifiedSignedCachePath(t *testing.T) {
	f := newFixture(t)
	loader := &stubTrustedArchiveFetcher{path: "relative/unverified.tar"}
	f.exec.cfg.ArchiveFetcher = loader
	result, err := f.exec.Run(context.Background(), f.claim)
	require.ErrorIs(t, err, ErrInvalid)
	require.False(t, result.CleanupVerified)
	loader.path = filepath.Join(t.TempDir(), "bad.tar")
	require.NoError(t, os.WriteFile(loader.path, []byte("wrong hash"), 0600))
	result, err = f.exec.Run(context.Background(), f.claim)
	require.ErrorIs(t, err, papersassets.ErrIntegrity)
	require.False(t, result.CleanupVerified)
}
