package papersworker

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/papersassets"
	"atoman/internal/platform/papersqueue"
	"atoman/internal/platform/paperssandbox"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fixture struct {
	exec    *Executor
	store   *papersassets.Store
	archive string
	claim   papersqueue.Claim
	root    string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("P0 privileged sandbox staging runs only on root-owned test machines")
	}
	root := t.TempDir()
	for _, dir := range []string{"assets", "runs"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, dir), 0700))
	}
	store, err := papersassets.OpenStore(filepath.Join(root, "assets"))
	require.NoError(t, err)
	archive := filepath.Join(root, "pinned-rootfs.tar")
	file, err := os.Create(archive)
	require.NoError(t, err)
	tw := tar.NewWriter(file)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "usr", Mode: 0755, Typeflag: tar.TypeDir}))
	require.NoError(t, tw.Close())
	require.NoError(t, file.Close())
	archiveDigest, err := papersassets.HashFile(archive)
	require.NoError(t, err)
	env := papersassets.Environment{
		Version: 1, Name: "texlive-2026-p0",
		ImageDigest:   "sha256:5613d47067c98df80d6b3206c9a731ba6e5d3eb5828fea892afd270426de4bd4",
		ArchiveDigest: archiveDigest, RunscRelease: "20261019.0",
	}
	envDigest, err := store.PublishEnvironment(env)
	require.NoError(t, err)
	project := uuid.New()
	snapDigest, err := store.PublishSnapshot(papersassets.Snapshot{
		Version: 1, ProjectID: project, Revision: 2,
		Files: []papersassets.SourceFile{{Path: "main.tex", Content: "\\documentclass{article}\\begin{document}Test\\end{document}"}},
	})
	require.NoError(t, err)
	id := "pc-abcdef1234567890abcd1234"
	claim := papersqueue.Claim{
		Kind: papersqueue.ClaimRun, Job: model.PaperCompileJob{
			ID: uuid.New(), ProjectID: project, SourceRevision: 2,
			SnapshotDigest: snapDigest, EnvironmentDigest: envDigest, MainFile: "main.tex",
		}, WorkerID: "test-worker", Token: uuid.New(), Epoch: 1, SandboxID: id,
	}
	cmd := func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "/mock/systemd-run":
			output := filepath.Join(root, "runs", id, "output", "main.pdf")
			if err := os.WriteFile(output, []byte("%PDF-1.7\nMock PDF"), 0600); err != nil {
				return nil, err
			}
			return []byte(fmt.Sprintf(`SENTRY_CGROUP=/sys/fs/cgroup/system.slice/atomanp0-%s.scope
SENTRY_memory.max=805306368
SENTRY_memory.swap.max=0
SENTRY_pids.max=256
SENTRY_cpu.max=100000 100000
SENTRY_MEMORY_EVENTS_EARLY=max 0,oom 0,oom_kill 0,
SENTRY_PIDS_EVENTS_EARLY=max 0,
CGROUP_RUNSC_EXIT=0
`, id)), nil
		case "/mock/runsc":
			return []byte("ID PID STATUS\n"), nil
		case "/mock/systemctl":
			return []byte("inactive\n"), errors.New("inactive")
		}
		return nil, fmt.Errorf("unexpected command %s args %v", name, args)
	}
	tarBinary, err := exec.LookPath("tar")
	require.NoError(t, err)
	limits := paperssandbox.Limits{MemoryMaxBytes: 768 << 20, MemorySwapMaxBytes: 0, PIDsMax: 256, CPUQuotaMicros: 100000, CPUPeriodMicros: 100000}
	executor, err := New(Config{
		WorkRoot: filepath.Join(root, "runs"), Store: store, ArchivePath: archive,
		RunscBinary: "/mock/runsc", ScopeHelper: "/mock/helper", SystemdRunBinary: "/mock/systemd-run",
		SystemctlBinary: "/mock/systemctl", MountpointBinary: "/mock/mountpoint",
		FindmntBinary: "/mock/findmnt", UmountBinary: "/mock/umount",
		TarBinary: tarBinary, Limits: limits, Timeout: time.Second,
		WrapperMemory: "1024M", WrapperCPUQuota: "100%", WrapperTasksMax: 256, Command: cmd,
	})
	require.NoError(t, err)
	return &fixture{exec: executor, store: store, archive: archive, claim: claim, root: root}
}
func TestVerifiedExecutorStagesTrustedSnapshotAndCompiles(t *testing.T) {
	f := newFixture(t)
	result, err := f.exec.Run(context.Background(), f.claim)
	require.NoError(t, err)
	require.Equal(t, paperssandbox.Completed, result.Status)
	require.True(t, result.CleanupVerified)
	path := filepath.Join(f.root, "runs", f.claim.SandboxID, "bundle", "rootfs", "inputs", "main.tex")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(raw), "Test")
	for _, check := range []struct {
		path string
		mode os.FileMode
	}{
		{filepath.Join(f.root, "runs", f.claim.SandboxID), 0710},
		{filepath.Join(f.root, "runs", f.claim.SandboxID, "bundle"), 0710},
		{filepath.Join(f.root, "runs", f.claim.SandboxID, "bundle", "rootfs"), 0755},
		{filepath.Join(f.root, "runs", f.claim.SandboxID, "bundle", "rootfs", "inputs"), 0755},
		{path, 0644},
	} {
		info, err := os.Stat(check.path)
		require.NoError(t, err)
		require.Equal(t, check.mode, info.Mode().Perm(), "wrong sandbox host mode %s", check.path)
	}
	recovery := f.claim
	recovery.Kind = papersqueue.ClaimRecover
	require.NoError(t, f.exec.Reap(context.Background(), recovery))
}
func TestVerifiedExecutorRejectsTamperedSnapshot(t *testing.T) {
	f := newFixture(t)
	digest := strings.TrimPrefix(f.claim.Job.SnapshotDigest, "sha256:")
	file := filepath.Join(f.root, "assets", "snapshots", digest+".json")
	require.NoError(t, os.Chmod(file, 0600))
	require.NoError(t, os.WriteFile(file, []byte("tampered"), 0600))
	require.NoError(t, os.Chmod(file, 0400))
	result, err := f.exec.Run(context.Background(), f.claim)
	require.ErrorIs(t, err, papersassets.ErrIntegrity)
	require.False(t, result.CleanupVerified)
	_, err = os.Stat(filepath.Join(f.root, "runs", f.claim.SandboxID))
	require.True(t, os.IsNotExist(err))
}
func TestVerifiedExecutorRejectsTamperedEnvironmentArchive(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, os.WriteFile(f.archive, []byte("replacement"), 0600))
	result, err := f.exec.Run(context.Background(), f.claim)
	require.ErrorIs(t, err, papersassets.ErrIntegrity)
	require.False(t, result.CleanupVerified)
}
func TestVerifiedExecutorRejectsSnapshotFromAnotherProject(t *testing.T) {
	f := newFixture(t)
	f.claim.Job.ProjectID = uuid.New()
	result, err := f.exec.Run(context.Background(), f.claim)
	require.ErrorIs(t, err, ErrInvalid)
	require.False(t, result.CleanupVerified)
}

func TestP0ExecutorRejectsUnsafeTraversalPermissions(t *testing.T) {
	f := newFixture(t)
	config := f.exec.cfg
	runRoot := config.WorkRoot
	require.NoError(t, os.Chmod(runRoot, 0711))
	_, err := New(config)
	require.ErrorIs(t, err, ErrInvalid)
	require.NoError(t, os.Chmod(runRoot, 0710))
	require.NoError(t, os.Chown(runRoot, 0, 0))
	_, err = New(config)
	require.ErrorIs(t, err, ErrInvalid, "group execute must be limited to GID 65534")
	require.NoError(t, os.Chown(runRoot, 0, 65534))
	_, err = New(config)
	require.NoError(t, err)
}
