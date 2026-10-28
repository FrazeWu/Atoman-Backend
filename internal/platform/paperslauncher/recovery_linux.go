//go:build linux

package paperslauncher

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ListenRecoverStale recovers only an orphaned, root-owned Unix socket
// after a Launcher crash. It must never unlink a live listener, a symlink,
// a regular file, or a socket outside its dedicated root-owned 0710
// directory. No unprivileged user can write in that directory.
func ListenRecoverStale(path string, group uint32) (*net.UnixListener, error) {
	if os.Geteuid() != 0 || group == 0 {
		return nil, ErrRejected
	}
	dirErr := validateSocketDir(path, group)
	if dirErr == nil {
		return Listen(path, group)
	}
	// Before removing a path, verify the directory and the exact stale
	// socket's identity, not merely the filename or errno from Listen.
	st, err := os.Lstat(path)
	if err != nil {
		return nil, ErrRejected
	}
	if st.Mode()&os.ModeSocket == 0 || st.Mode()&os.ModeSymlink != 0 ||
		st.Mode().Perm() != 0660 {
		return nil, ErrRejected
	}
	ident, ok := st.Sys().(*syscall.Stat_t)
	if !ok || ident.Uid != 0 || ident.Gid != group {
		return nil, ErrRejected
	}

	// The directory must belong to root and be group-execute-only.
	parent, err := os.Lstat(parentDir(path))
	if err != nil {
		return nil, err
	}
	gid, ok := parent.Sys().(*syscall.Stat_t)
	if !ok || !parent.IsDir() || parent.Mode().Perm() != 0710 ||
		parent.Mode()&os.ModeSymlink != 0 || gid.Uid != 0 || gid.Gid != group {
		return nil, ErrRejected
	}
	conn, err := net.DialTimeout("unix", path, 300*time.Millisecond)
	if err == nil {
		conn.Close()
		return nil, ErrRejected
	}
	var op *net.OpError
	if !errors.As(err, &op) || !errors.Is(err, syscall.ECONNREFUSED) {
		return nil, ErrRejected
	}
	// Recheck before unlink. Only root may create entries in the protected
	// directory, so a different uid cannot swap this inode.
	again, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	fresh, ok := again.Sys().(*syscall.Stat_t)
	if !ok || fresh.Dev != ident.Dev || fresh.Ino != ident.Ino ||
		again.Mode() != st.Mode() {
		return nil, ErrRejected
	}
	if err := os.Remove(path); err != nil {
		return nil, err
	}
	return Listen(path, group)
}

func parentDir(path string) string {
	return filepath.Dir(path)
}
