//go:build linux

package papersworker

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"atoman/internal/platform/papersassets"
	"atoman/internal/platform/papersqueue"
	"atoman/internal/platform/paperssandbox"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func liveQuotaTest(t *testing.T) {
	t.Helper()
	if os.Getenv("PAPERS_P0_QUOTA_CONFIRM") != "1" || os.Geteuid() != 0 {
		t.Skip("explicit disposable root quota test required")
	}
}

func TestBoundedOutputKernelLimitsAndVerifiedUnmount(t *testing.T) {
	liveQuotaTest(t)
	root := filepath.Join(t.TempDir(), "pc-abcdef1234567890abcd1234")
	require.NoError(t, os.Mkdir(root, 0700))
	require.NoError(t, os.Mkdir(filepath.Join(root, "output"), 0700))
	require.NoError(t, mountBoundedOutput(root))
	t.Cleanup(func() { require.NoError(t, removeBoundedWorkspace(root)) })
	require.NoError(t, validateQuota(root))
	output := filepath.Join(root, "output")
	var info unix.Statfs_t
	require.NoError(t, unix.Statfs(output, &info))
	require.LessOrEqual(t, uint64(info.Bsize)*info.Blocks, OutputQuotaBytes)
	require.LessOrEqual(t, info.Files, OutputQuotaInodes)
	// Fill aggregate bytes across multiple files. No host filesystem writes
	// beyond the disposable tmpfs, and no unsafe malicious TeX fixture.
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = 1
	}
	for _, name := range []string{"first", "second"} {
		f, err := os.Create(filepath.Join(output, name))
		require.NoError(t, err)
		for i := 0; i < 64; i++ {
			_, err = f.Write(data)
			require.NoError(t, err)
		}
		require.NoError(t, f.Close())
	}
	err := os.WriteFile(filepath.Join(output, "overflow"), []byte("x"), 0600)
	require.ErrorIs(t, err, unix.ENOSPC)
	require.NoError(t, os.Remove(filepath.Join(output, "first")))
	require.NoError(t, os.Remove(filepath.Join(output, "second")))
	require.NoError(t, os.Remove(filepath.Join(output, "overflow")))
	count := 0
	for ; count < int(OutputQuotaInodes)+1; count++ {
		name := filepath.Join(output, uuid.NewString())
		err = os.WriteFile(name, nil, 0600)
		if errors.Is(err, unix.ENOSPC) {
			break
		}
		require.NoError(t, err)
	}
	require.Greater(t, count, 0)
	require.Less(t, count, int(OutputQuotaInodes))
	require.NoError(t, removeBoundedWorkspace(root))
	require.NoDirExists(t, root)
	mounts, err := mountsUnder(root)
	require.NoError(t, err)
	require.Empty(t, mounts)
	t.Log("KERNEL_OUTPUT_BYTES_AND_INODES_LIMITS_VERIFIED")
}

type quotaPDFPublisher struct {
	called bool
	fail   bool
}

func (p *quotaPDFPublisher) PublishPDF(_ context.Context, id uuid.UUID, epoch int64, f *os.File) (papersassets.PDFRef, error) {
	p.called = true
	if p.fail {
		return papersassets.PDFRef{}, errors.New("fixture publication error")
	}
	raw, err := io.ReadAll(io.NewSectionReader(f, 0, 50<<20))
	if err != nil {
		return papersassets.PDFRef{}, err
	}
	return papersassets.PDFRef{JobID: id, Epoch: epoch, Digest: papersassets.HashBytes(raw), Size: int64(len(raw))}, nil
}

func TestBoundedExecutorRemovesWorkspaceOnPublicationSuccessAndFailure(t *testing.T) {
	liveQuotaTest(t)
	for _, fail := range []bool{false, true} {
		f := newFixture(t)
		cfg := f.exec.cfg
		pub := &quotaPDFPublisher{fail: fail}
		cfg.PDFPublisher = pub
		e, err := New(cfg)
		require.NoError(t, err)
		result, err := e.Run(context.Background(), f.claim)
		require.True(t, pub.called)
		require.True(t, result.CleanupVerified)
		if fail {
			require.Error(t, err)
			require.Equal(t, paperssandbox.MissingArtifact, result.Status)
		} else {
			require.NoError(t, err)
			require.Equal(t, paperssandbox.Completed, result.Status)
			require.NotEmpty(t, result.PDFDigest)
		}
		root := filepath.Join(cfg.WorkRoot, f.claim.SandboxID)
		require.NoDirExists(t, root)
		claim := f.claim
		claim.Kind = papersqueue.ClaimRecover
		require.NoError(t, e.Reap(context.Background(), claim), "recovery is idempotent after a pre-commit crash")
	}
}
