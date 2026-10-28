//go:build linux

package paperslauncher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/papersqueue"
	"atoman/internal/platform/paperssandbox"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type mockExecutor struct {
	run  func(context.Context, papersqueue.Claim) (paperssandbox.Result, error)
	reap func(context.Context, papersqueue.Claim) error
}

func (m mockExecutor) Run(ctx context.Context, c papersqueue.Claim) (paperssandbox.Result, error) {
	if m.run != nil {
		return m.run(ctx, c)
	}
	return paperssandbox.Result{JobID: c.SandboxID, Status: paperssandbox.Completed,
		UnderlyingStatus: paperssandbox.Completed, CleanupVerified: true}, nil
}
func (m mockExecutor) Reap(ctx context.Context, c papersqueue.Claim) error {
	if m.reap != nil {
		return m.reap(ctx, c)
	}
	return nil
}
func testClaim() papersqueue.Claim {
	id := uuid.New()
	token := uuid.New()
	return papersqueue.Claim{Kind: papersqueue.ClaimRun, Job: model.PaperCompileJob{ID: id,
		ProjectID: uuid.New(), OwnerID: uuid.New(), MainFile: "main.tex",
		SnapshotDigest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		EnvironmentDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}, WorkerID: "test-ud-worker", Token: token, Epoch: 1, SandboxID: "pc-01234567890123456789abcd"}
}
func TestWireRejectsUnknownOperationAndUnknownFields(t *testing.T) {
	claim := testClaim()
	raw, err := json.Marshal(request{Operation: "shell", Claim: claim})
	require.NoError(t, err)
	_, err = decodeRequest(raw)
	require.ErrorIs(t, err, ErrRejected)
	raw, err = json.Marshal(request{Operation: "run", Claim: claim})
	require.NoError(t, err)
	raw = append([]byte(`{"operation":"run","claim":`), append(raw[len(`{"operation":"run","claim":`):len(raw)-1], []byte(`,"arbitraryCommand":"sh"}`)...)...)
	_, err = decodeRequest(raw)
	require.ErrorIs(t, err, ErrRejected)
}
func TestServerConfigRejectsRootWorkerAndInvalidDirectory(t *testing.T) {
	_, err := NewServer(Config{AllowedUID: 0, AllowedGID: 65534, Executor: mockExecutor{},
		Verify:             func(_ context.Context, c papersqueue.Claim) (papersqueue.Claim, error) { return c, nil },
		LeaseCheckInterval: time.Second})
	require.ErrorIs(t, err, ErrRejected)
	if os.Geteuid() != 0 {
		t.Skip("Unix credential ownership tests require root")
	}
	parent := t.TempDir()
	_, err = Listen(filepath.Join(parent, "launcher.sock"), 65534)
	require.ErrorIs(t, err, ErrRejected)
}
func TestLauncherHelperProcess(t *testing.T) {
	if os.Getenv("P0_LAUNCHER_HELPER") != "1" {
		return
	}
	socket := os.Getenv("P0_LAUNCHER_SOCKET")
	var claim papersqueue.Claim
	if err := json.Unmarshal([]byte(os.Getenv("P0_LAUNCHER_CLAIM")), &claim); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(9)
	}
	client, err := NewClient(socket)
	if err != nil {
		os.Exit(10)
	}
	switch os.Getenv("P0_LAUNCHER_ACTION") {
	case "run":
		result, err := client.Run(context.Background(), claim)
		if err != nil || !result.CleanupVerified {
			fmt.Fprintln(os.Stderr, "client run", err)
			os.Exit(11)
		}
	case "disappear":
		conn, err := net.Dial("unix", socket)
		if err != nil {
			os.Exit(12)
		}
		raw, _ := json.Marshal(request{Operation: "run", Claim: claim})
		if _, err = conn.Write(append(raw, '\n')); err != nil {
			os.Exit(13)
		}
		time.Sleep(200 * time.Millisecond)
		conn.Close()
	default:
		os.Exit(14)
	}
	os.Exit(0)
}
func spawnUnprivileged(t *testing.T, bin, socket string, claim papersqueue.Claim, action string) error {
	t.Helper()
	raw, err := json.Marshal(claim)
	require.NoError(t, err)
	cmd := exec.Command(bin, "-test.run=^TestLauncherHelperProcess$")
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{
		Uid: 65534, Gid: 65534, NoSetGroups: true,
	}}
	cmd.Env = append(os.Environ(), "P0_LAUNCHER_HELPER=1", "P0_LAUNCHER_SOCKET="+socket,
		"P0_LAUNCHER_CLAIM="+string(raw), "P0_LAUNCHER_ACTION="+action)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: output %s", err, string(output))
	}
	return nil
}
func setupAuthorizedSocket(t *testing.T) (string, string, func()) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("peer credential integration requires root test run")
	}
	base, err := os.MkdirTemp(os.TempDir(), "atoman-papers-p0-unix-test.")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(base)) })
	require.NoError(t, os.Chown(base, 0, 65534))
	require.NoError(t, os.Chmod(base, 0710))
	for _, name := range []string{"uds", "bin"} {
		path := filepath.Join(base, name)
		require.NoError(t, os.Mkdir(path, 0700))
		require.NoError(t, os.Chown(path, 0, 65534))
		require.NoError(t, os.Chmod(path, 0710))
	}
	// The test binary may live in a root-only Go build cache directory. Copy
	// it under this single disposable root-owned, group-traversable test dir.
	source, err := os.Open(os.Args[0])
	require.NoError(t, err)
	defer source.Close()
	dest := filepath.Join(base, "bin", "test-helper")
	target, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
	require.NoError(t, err)
	_, err = io.Copy(target, source)
	require.NoError(t, err)
	require.NoError(t, target.Close())
	require.NoError(t, os.Chmod(dest, 0755))
	socket := filepath.Join(base, "uds", "launcher.sock")
	return socket, dest, func() {}
}
func TestRealUnixCredentialsAndClientRoundTrip(t *testing.T) {
	socket, bin, _ := setupAuthorizedSocket(t)
	claim := testClaim()
	called := make(chan papersqueue.Claim, 1)
	srv, err := NewServer(Config{AllowedUID: 65534, AllowedGID: 65534, Executor: mockExecutor{
		run: func(_ context.Context, c papersqueue.Claim) (paperssandbox.Result, error) {
			called <- c
			return paperssandbox.Result{JobID: c.SandboxID, Status: paperssandbox.Completed,
				UnderlyingStatus: paperssandbox.Completed, CleanupVerified: true}, nil
		},
	}, Verify: func(_ context.Context, c papersqueue.Claim) (papersqueue.Claim, error) {
		c.Job.MainFile = "trusted-from-postgres.tex"
		return c, nil
	}, LeaseCheckInterval: time.Second})
	require.NoError(t, err)
	listener, err := Listen(socket, 65534)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, listener) }()
	// A root-side client can connect to the filesystem but the server must
	// reject it after SO_PEERCRED, even when it has a valid-looking claim.
	rootClient, _ := NewClient(socket)
	_, err = rootClient.Run(context.Background(), claim)
	require.Error(t, err)
	require.NoError(t, spawnUnprivileged(t, bin, socket, claim, "run"))
	select {
	case actual := <-called:
		require.Equal(t, "trusted-from-postgres.tex", actual.Job.MainFile)
	case <-time.After(4 * time.Second):
		t.Fatal("root launcher did not run authorized client request")
	}
	cancel()
	require.NoError(t, <-done)
}
func TestClientDisconnectCancelsRootExecution(t *testing.T) {
	socket, bin, _ := setupAuthorizedSocket(t)
	claim := testClaim()
	observed := make(chan struct{}, 1)
	srv, err := NewServer(Config{AllowedUID: 65534, AllowedGID: 65534,
		Executor: mockExecutor{run: func(ctx context.Context, c papersqueue.Claim) (paperssandbox.Result, error) {
			select {
			case <-ctx.Done():
				observed <- struct{}{}
				return paperssandbox.Result{
					JobID: c.SandboxID, Status: paperssandbox.Canceled, CleanupVerified: true}, nil
			case <-time.After(5 * time.Second):
				return paperssandbox.Result{}, fmt.Errorf("worker disconnect was not observed")
			}
		}},
		Verify:             func(_ context.Context, c papersqueue.Claim) (papersqueue.Claim, error) { return c, nil },
		LeaseCheckInterval: time.Second,
	})
	require.NoError(t, err)
	listener, err := Listen(socket, 65534)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, listener) }()
	require.NoError(t, spawnUnprivileged(t, bin, socket, claim, "disappear"))
	select {
	case <-observed:
	case <-time.After(3 * time.Second):
		t.Fatal("launcher did not cancel after Worker disconnected")
	}
	cancel()
	require.NoError(t, <-done)
}
