//go:build linux

package paperspubgate

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// UnixServer accepts only AF_UNIX clients. UID authorization happens with
// kernel SO_PEERCRED, not with a forged HTTP header or remote IP.
type UnixServer struct {
	listener *net.UnixListener
	server   *http.Server
	path     string
}

func NewUnixServer(g *Gateway, socketPath string, publisherGID int) (*UnixServer, error) {
	if g == nil || !filepath.IsAbs(socketPath) || publisherGID < 0 {
		return nil, errors.New("invalid Unix gateway")
	}
	parent := filepath.Dir(socketPath)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != filepath.Clean(parent) {
		return nil, errors.New("symlinked Unix socket parent")
	}
	s, err := os.Lstat(parent)
	if err != nil {
		return nil, err
	}
	owner, ok := s.Sys().(*syscall.Stat_t)
	if !s.IsDir() || s.Mode()&os.ModeSymlink != 0 || s.Mode().Perm()&0027 != 0 ||
		!ok || owner.Uid != uint32(os.Geteuid()) || owner.Gid != uint32(publisherGID) {
		return nil, errors.New("socket parent must be owned and non-writable by publishers")
	}
	// Avoid unlinking any existing endpoint, including a symlink.
	if _, err := os.Lstat(socketPath); err == nil {
		return nil, errors.New("socket already exists")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	addr := &net.UnixAddr{Name: socketPath, Net: "unix"}
	l, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*UnixServer, error) { l.Close(); os.Remove(socketPath); return nil, err }
	if err = os.Chown(socketPath, os.Getuid(), publisherGID); err != nil {
		return fail(err)
	}
	if err = os.Chmod(socketPath, 0660); err != nil {
		return fail(err)
	}
	srv := &http.Server{Handler: g, ReadHeaderTimeout: 10 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			uc, ok := c.(*net.UnixConn)
			if !ok {
				return ctx
			}
			sc, err := uc.SyscallConn()
			if err != nil {
				return ctx
			}
			var peer *unix.Ucred
			err = sc.Control(func(fd uintptr) {
				peer, err = unix.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
			})
			if err != nil || peer == nil {
				return ctx
			}
			return gatewayContext(ctx, peer.Uid)
		}}
	return &UnixServer{listener: l, server: srv, path: socketPath}, nil
}
func (s *UnixServer) Serve() error {
	if s == nil {
		return errors.New("nil gateway server")
	}
	err := s.server.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func (s *UnixServer) Close() error {
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := s.server.Shutdown(ctx)
	s.listener.Close()
	// The socket lives in an operator-controlled private directory.
	os.Remove(s.path)
	return err
}
