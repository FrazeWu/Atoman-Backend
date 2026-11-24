//go:build linux

// Package paperslauncher is a P0-only Unix-socket privilege boundary.
// The Launcher is root-owned; the database Worker is a separate unprivileged
// process. No network listener or generic shell command is exposed.
package paperslauncher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"atoman/internal/platform/papersqueue"
	"atoman/internal/platform/paperssandbox"
)

const maxFrame = 16 << 10

var (
	ErrRejected = errors.New("papers launcher: request rejected")
	ErrBusy     = errors.New("papers launcher: executor is busy")
	ErrAuth     = errors.New("papers launcher: unauthorized peer")
)

// Executor is implemented by the root-only pinned gVisor staging adapter.
type Executor interface {
	Run(context.Context, papersqueue.Claim) (paperssandbox.Result, error)
	Reap(context.Context, papersqueue.Claim) error
}

// LeaseVerifier MUST rebuild the claim from the authoritative database,
// verifying token/epoch/status/expires-at, rather than trusting socket input.
type LeaseVerifier func(context.Context, papersqueue.Claim) (papersqueue.Claim, error)

type Config struct {
	AllowedUID         uint32
	AllowedGID         uint32
	Executor           Executor
	Verify             LeaseVerifier
	LeaseCheckInterval time.Duration
}

type Server struct {
	cfg  Config
	gate chan struct{}
	wg   sync.WaitGroup
}

func NewServer(cfg Config) (*Server, error) {
	if cfg.AllowedUID == 0 || cfg.AllowedGID == 0 || cfg.Executor == nil || cfg.Verify == nil ||
		cfg.LeaseCheckInterval < time.Second || cfg.LeaseCheckInterval > 10*time.Second {
		return nil, ErrRejected
	}
	return &Server{cfg: cfg, gate: make(chan struct{}, 1)}, nil
}

func credentials(conn *net.UnixConn) (uint32, uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var peer *syscall.Ucred
	var inner error
	if err = raw.Control(func(fd uintptr) {
		peer, inner = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, 0, err
	}
	if inner != nil {
		return 0, 0, inner
	}
	if peer == nil {
		return 0, 0, ErrAuth
	}
	return peer.Uid, peer.Gid, nil
}

func validateSocketDir(socketPath string, group uint32) error {
	if !filepath.IsAbs(socketPath) || len(socketPath) > 99 ||
		filepath.Ext(socketPath) != ".sock" {
		return ErrRejected
	}
	dir := filepath.Dir(socketPath)
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 ||
		st.Mode().Perm() != 0710 {
		return ErrRejected
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || stat.Gid != group {
		return ErrRejected
	}
	if _, err := os.Lstat(socketPath); err == nil {
		return ErrRejected
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Listen creates a strictly scoped root-owned Unix socket. It refuses stale
// paths rather than unlinking another listener's socket.
func Listen(path string, group uint32) (*net.UnixListener, error) {
	if os.Geteuid() != 0 || group == 0 {
		return nil, ErrRejected
	}
	if err := validateSocketDir(path, group); err != nil {
		return nil, err
	}
	addr := &net.UnixAddr{Name: path, Net: "unix"}
	l, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, err
	}
	if err = os.Chown(path, 0, int(group)); err != nil {
		l.Close()
		return nil, err
	}
	if err = os.Chmod(path, 0660); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

type request struct {
	Operation string            `json:"operation"`
	Claim     papersqueue.Claim `json:"claim"`
}
type response struct {
	Result   paperssandbox.Result `json:"result"`
	Verified bool                 `json:"verified"`
	Error    string               `json:"error,omitempty"`
}

func decodeRequest(raw []byte) (request, error) {
	var req request
	if len(raw) == 0 || len(raw) > maxFrame {
		return req, ErrRejected
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, ErrRejected
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return req, ErrRejected
	}
	if req.Operation != "run" && req.Operation != "reap" {
		return req, ErrRejected
	}
	if !req.Claim.Valid() {
		return req, ErrRejected
	}
	if (req.Operation == "run" && req.Claim.Kind != papersqueue.ClaimRun) ||
		(req.Operation == "reap" && req.Claim.Kind != papersqueue.ClaimRecover) {
		return req, ErrRejected
	}
	return req, nil
}

func (s *Server) Serve(ctx context.Context, listener *net.UnixListener) error {
	if s == nil || listener == nil {
		return ErrRejected
	}
	// Only AF_UNIX, never a public TCP port.
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			listener.Close()
		case <-stop:
		}
	}()
	defer close(stop)
	defer func() { listener.Close(); s.wg.Wait() }()
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.handle(ctx, conn) }()
	}
}

func (s *Server) handle(parent context.Context, conn *net.UnixConn) {
	defer conn.Close()
	uid, gid, err := credentials(conn)
	if err != nil || uid != s.cfg.AllowedUID || gid != s.cfg.AllowedGID {
		return // leak no details to unauthorized users
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return
	}
	// One newline-framed JSON request; ReadSlice caps memory even for a client
	// that never sends a newline.
	reader := newFrameReader(conn)
	frame, err := reader.ReadSlice('\n')
	if err != nil {
		return
	}
	req, err := decodeRequest(bytes.TrimSpace(frame))
	if err != nil {
		send(conn, response{Error: "invalid_request"})
		return
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return
	}
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	default:
		send(conn, response{Error: "launcher_busy"})
		return
	}
	verifyCtx, stopVerify := context.WithTimeout(parent, 5*time.Second)
	claim, err := s.cfg.Verify(verifyCtx, req.Claim)
	stopVerify()
	if err != nil {
		send(conn, response{Error: "lease_rejected"})
		return
	}
	// All immutable input identifiers come from the DB-verified claim. No
	// caller-supplied command, executable, image path or mount is accepted.
	running, cancel := context.WithCancel(parent)
	defer cancel()
	done := make(chan struct{})
	go func() {
		one := make([]byte, 1)
		// A dead Worker closes the socket: stop the sandbox and perform
		// Supervisor's independent cleanup, not a silent continuation.
		_, _ = reader.Read(one)
		cancel()
	}()
	go func() {
		tick := time.NewTicker(s.cfg.LeaseCheckInterval)
		defer tick.Stop()
		for {
			select {
			case <-running.Done():
				return
			case <-done:
				return
			case <-tick.C:
				checkCtx, stop := context.WithTimeout(context.Background(), s.cfg.LeaseCheckInterval)
				_, err := s.cfg.Verify(checkCtx, claim)
				stop()
				if err != nil {
					// Do not log lease tokens, request bodies or provider error strings.
					category := "rejected"
					if errors.Is(err, context.DeadlineExceeded) {
						category = "deadline"
					}
					var timeout net.Error
					if errors.As(err, &timeout) && timeout.Timeout() {
						category = "timeout"
					}
					log.Printf("papers lease check failed job=%s category=%s", claim.Job.ID, category)
					cancel()
					return
				}
			}
		}
	}()
	var reply response
	if req.Operation == "run" {
		reply.Result, err = s.cfg.Executor.Run(running, claim)
		reply.Verified = err == nil
	} else {
		// Cleanup should remain independent of the socket if a recovering
		// Worker disappears; otherwise an old sandbox could be left active.
		cleanCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		err = s.cfg.Executor.Reap(cleanCtx, claim)
		stop()
		reply.Verified = err == nil
	}
	if req.Operation == "run" {
		log.Printf("papers compile finished job=%s status=%s cleanup=%t sentry=%t limits=%t exit_known=%t exit=%d", claim.Job.ID, reply.Result.Status, reply.Result.CleanupVerified, reply.Result.SentryObserved, reply.Result.LimitsVerified, reply.Result.ExitKnown, reply.Result.ExitCode)
	}
	if err != nil {
		reply.Error = "sandbox_operation_failed"
	}
	close(done)
	// Do not Wait on read goroutine before responding; it is waiting for the
	// Worker to close after it receives our result.
	send(conn, reply)
	cancel()
	// The socket closes on return, unblocking its watcher.
}

func send(conn *net.UnixConn, reply response) {
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = json.NewEncoder(conn).Encode(reply)
}

// readSlice is minimal fixed-frame buffering, independent of wire JSON size.
type frameReader struct {
	conn   *net.UnixConn
	buffer []byte
}

func newFrameReader(conn *net.UnixConn) *frameReader { return &frameReader{conn: conn} }
func (r *frameReader) ReadSlice(delim byte) ([]byte, error) {
	one := make([]byte, 1)
	for len(r.buffer) < maxFrame {
		n, err := r.conn.Read(one)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
		r.buffer = append(r.buffer, one[0])
		if one[0] == delim {
			return r.buffer, nil
		}
	}
	return nil, ErrRejected
}
func (r *frameReader) Read(b []byte) (int, error) {
	return r.conn.Read(b)
}
