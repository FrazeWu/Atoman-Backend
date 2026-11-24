//go:build linux

package paperslauncher

import (
	"context"
	"errors"
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
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestLeaseBridgeUnprivilegedHelper(t *testing.T) {
	path := os.Getenv("PAPERS_TEST_LEASE_BRIDGE")
	if path == "" {
		t.Skip("child-only fixture")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done, err := StartLeaseBridge(ctx, path, func(_ context.Context, c papersqueue.Claim) (papersqueue.Claim, error) {
		if c.Epoch != 1 {
			return papersqueue.Claim{}, errors.New("stale")
		}
		c.Job.MainFile = "authoritative.tex"
		return c, nil
	})
	require.NoError(t, err)
	// Even the owner of the socket directory cannot query the verifier
	// through the socket as an unprivileged peer.
	conn, err := net.DialTimeout("unix", path, time.Second)
	require.NoError(t, err)
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	_, _ = conn.Write([]byte("{}\n"))
	data, _ := io.ReadAll(conn)
	require.Empty(t, data)
	_ = conn.Close()
	_, _ = io.ReadAll(os.Stdin)
	cancel()
	<-done
}
func TestEmbeddedBridgeRootPeerAndCanonicalLease(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("isolated root/nonroot socket fixture")
	}
	base, err := os.MkdirTemp("/var/tmp", "papers-lease-bridge-test.")
	require.NoError(t, err)
	defer os.RemoveAll(base)
	require.NoError(t, os.Chmod(base, 0711))
	executable, err := os.Executable()
	require.NoError(t, err)
	raw, err := os.ReadFile(executable)
	require.NoError(t, err)
	helper := filepath.Join(base, "fixture")
	require.NoError(t, os.WriteFile(helper, raw, 0755))
	require.NoError(t, os.Chmod(helper, 0755))
	dir := filepath.Join(base, "socket")
	require.NoError(t, os.Mkdir(dir, 0700))
	const childUID uint32 = 65534
	require.NoError(t, os.Chown(dir, int(childUID), int(childUID)))
	socket := filepath.Join(dir, "lease.sock")
	cmd := exec.Command(helper, "-test.run=^TestLeaseBridgeUnprivilegedHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), "PAPERS_TEST_LEASE_BRIDGE="+socket)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: childUID, Gid: childUID}}
	input, err := cmd.StdinPipe()
	require.NoError(t, err)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	waited := false
	defer func() {
		if !waited {
			_ = input.Close()
			_ = cmd.Wait()
		}
	}()
	require.Eventually(t, func() bool { st, err := os.Lstat(socket); return err == nil && st.Mode()&os.ModeSocket != 0 }, 5*time.Second, 20*time.Millisecond)
	c := papersqueue.Claim{Kind: papersqueue.ClaimRun, Job: model.PaperCompileJob{ID: uuid.New(), MainFile: "untrusted.tex"}, WorkerID: "fixture", Token: uuid.New(), Epoch: 1, SandboxID: "pc-abcdef1234567890abcd1234"}
	verify := RemoteLeaseVerifier(socket, childUID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	actual, err := verify(ctx, c)
	require.NoError(t, err)
	require.Equal(t, "authoritative.tex", actual.Job.MainFile)
	c.Epoch = 2
	_, err = verify(ctx, c)
	require.Error(t, err)
	_, err = RemoteLeaseVerifier(socket, childUID-1)(ctx, c)
	require.Error(t, err)
	require.NoError(t, input.Close())
	err = cmd.Wait()
	waited = true
	require.NoError(t, err)
}
