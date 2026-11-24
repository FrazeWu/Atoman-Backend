//go:build linux

package paperslauncher

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStaleSocketRecoveryNeverStealsLiveListener(t *testing.T) {
	socket, _, _ := setupAuthorizedSocket(t)
	first, err := Listen(socket, 65534)
	require.NoError(t, err)
	defer first.Close()
	_, err = ListenRecoverStale(socket, 65534)
	require.ErrorIs(t, err, ErrRejected)
	_, err = os.Lstat(socket)
	require.NoError(t, err, "live socket must survive a competing restart")
}

func TestStaleSocketRecoveryRequiresRootOwnedSocket(t *testing.T) {
	socket, _, _ := setupAuthorizedSocket(t)
	require.NoError(t, os.Symlink("/dev/null", socket))
	_, err := ListenRecoverStale(socket, 65534)
	require.ErrorIs(t, err, ErrRejected)
	_, err = os.Lstat(socket)
	require.NoError(t, err)
	require.NoError(t, os.Remove(socket))
	require.NoError(t, os.WriteFile(socket, []byte("not a socket"), 0600))
	_, err = ListenRecoverStale(socket, 65534)
	require.ErrorIs(t, err, ErrRejected)
	data, err := os.ReadFile(socket)
	require.NoError(t, err)
	require.Equal(t, "not a socket", string(data))
}
func TestStaleSocketRecoveryAfterCrashingOwner(t *testing.T) {
	socket, _, _ := setupAuthorizedSocket(t)
	listener, err := Listen(socket, 65534)
	require.NoError(t, err)
	listener.SetUnlinkOnClose(false) // emulate SIGKILL leaving the pathname
	require.NoError(t, listener.Close())
	_, err = os.Lstat(socket)
	require.NoError(t, err)
	recovered, err := ListenRecoverStale(socket, 65534)
	require.NoError(t, err)
	defer recovered.Close()
	require.Equal(t, "unix", recovered.Addr().Network())
	conn, err := net.Dial("unix", socket)
	require.NoError(t, err)
	conn.Close()
}
func TestStaleSocketRecoveryRejectsUntrustedOwner(t *testing.T) {
	socket, _, _ := setupAuthorizedSocket(t)
	listener, err := Listen(socket, 65534)
	require.NoError(t, err)
	listener.SetUnlinkOnClose(false)
	require.NoError(t, listener.Close())
	require.NoError(t, os.Chown(socket, 65534, 65534))
	_, err = ListenRecoverStale(socket, 65534)
	require.ErrorIs(t, err, ErrRejected)
	// Test owns this path; do not leave a dangling socket.
	require.NoError(t, os.Remove(socket))
}
func TestStaleSocketRecoveryRejectsWorldWritableDirectory(t *testing.T) {
	socket, _, _ := setupAuthorizedSocket(t)
	dir := filepath.Dir(socket)
	require.NoError(t, os.Chmod(dir, 0777))
	_, err := ListenRecoverStale(socket, 65534)
	require.ErrorIs(t, err, ErrRejected)
}
