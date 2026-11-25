//go:build linux

package paperscrdt

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	socketTimeout     = 8 * time.Second
	maxSocketRequests = 2
)

type Runner interface {
	Run(context.Context, Request) (Checkpoint, error)
}

type socketReply struct {
	Checkpoint *Checkpoint `json:"checkpoint,omitempty"`
	Error      string      `json:"error,omitempty"`
}

// Frames are bounded before allocation and support exactly one request and
// reply. No command, filesystem path, database ID or credential crosses IPC.
func readFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, ErrUnavailable
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > maxJSON {
		return nil, ErrInvalid
	}
	raw := make([]byte, int(n))
	if _, err := io.ReadFull(r, raw); err != nil {
		return nil, ErrUnavailable
	}
	return raw, nil
}
func writeFrame(w io.Writer, v any) error {
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 || len(raw) > maxJSON {
		return ErrInvalid
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
	for _, data := range [][]byte{header[:], raw} {
		n, err := w.Write(data)
		if err != nil || n != len(data) {
			return ErrUnavailable
		}
	}
	return nil
}
func decodeFrame(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ErrInvalid
	}
	return nil
}
func peerUID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, ErrUnavailable
	}
	var cred *syscall.Ucred
	var inner error
	err = raw.Control(func(fd uintptr) {
		cred, inner = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil || inner != nil || cred == nil {
		return 0, ErrUnavailable
	}
	return cred.Uid, nil
}

// UnixClient only trusts a root-owned, filesystem Unix socket created by
// systemd. SO_PEERCRED verifies the listener's creator, not the dynamic
// service user that accepted the inherited descriptor.
type UnixClient struct {
	path        string
	listenerUID uint32
}

func NewUnixClient(path string) (*UnixClient, error) { return newUnixClient(path, 0) }
func newUnixClient(path string, uid uint32) (*UnixClient, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 99 || filepath.Ext(path) != ".sock" || strings.ContainsRune(path, 0) {
		return nil, ErrUnavailable
	}
	return &UnixClient{path: path, listenerUID: uid}, nil
}
func (c *UnixClient) Run(ctx context.Context, req Request) (Checkpoint, error) {
	var out Checkpoint
	if err := validateRequest(req); err != nil {
		return out, err
	}
	if c == nil || ctx == nil {
		return out, ErrUnavailable
	}
	// Check file ownership before connection; check the actual listener
	// credentials after connection, so a replaced pathname cannot be trusted.
	st, err := os.Lstat(c.path)
	if err != nil || st.Mode()&os.ModeSocket == 0 {
		return out, ErrUnavailable
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != c.listenerUID || st.Mode().Perm()&0007 != 0 {
		return out, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, socketTimeout)
	defer cancel()
	d := net.Dialer{}
	raw, err := d.DialContext(ctx, "unix", c.path)
	if err != nil {
		return out, ErrUnavailable
	}
	defer raw.Close()
	conn, ok := raw.(*net.UnixConn)
	if !ok {
		return out, ErrUnavailable
	}
	uid, err := peerUID(conn)
	if err != nil || uid != c.listenerUID {
		return out, ErrUnavailable
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return out, ErrUnavailable
	}
	if err := writeFrame(conn, req); err != nil {
		return out, ErrUnavailable
	}
	replyRaw, err := readFrame(conn)
	if err != nil {
		return out, ErrUnavailable
	}
	var reply socketReply
	if decodeFrame(replyRaw, &reply) != nil {
		return out, ErrUnavailable
	}
	if reply.Error != "" {
		if reply.Checkpoint != nil {
			return out, ErrUnavailable
		}
		if reply.Error == "invalid" {
			return out, ErrInvalid
		}
		return out, ErrUnavailable
	}
	if reply.Checkpoint == nil || !ValidCheckpoint(*reply.Checkpoint) {
		return out, ErrUnavailable
	}
	out = *reply.Checkpoint
	if (req.Op == "seed" || req.Op == "read") && out.Text != req.Text {
		return Checkpoint{}, ErrUnavailable
	}
	return out, nil
}

type SocketServer struct {
	runner     Runner
	allowedUID uint32
	gate       chan struct{}
}

func NewSocketServer(runner Runner, allowedUID uint32) (*SocketServer, error) {
	if runner == nil {
		return nil, ErrUnavailable
	}
	return &SocketServer{runner: runner, allowedUID: allowedUID, gate: make(chan struct{}, maxSocketRequests)}, nil
}

// Serve never reads unbounded queues: reject unauthorized or excess peers
// before allocating a frame. Stop closes the listener, cancels active engine
// operations and drains only the bounded in-flight work.
func (s *SocketServer) Serve(ctx context.Context, listener *net.UnixListener) error {
	if s == nil || ctx == nil || listener == nil {
		return ErrUnavailable
	}
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer func() { cancel(); _ = listener.Close(); stop(); wg.Wait() }()
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return ErrUnavailable
		}
		uid, err := peerUID(conn)
		if err != nil || uid != s.allowedUID {
			_ = conn.Close()
			continue
		}
		select {
		case s.gate <- struct{}{}:
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-s.gate }()
				defer conn.Close()
				s.serveOne(ctx, conn)
			}()
		default:
			// A busy server does not allocate a reader goroutine or queue a request.
			_ = conn.Close()
		}
	}
}
func (s *SocketServer) serveOne(ctx context.Context, conn *net.UnixConn) {
	ctx, cancel := context.WithTimeout(ctx, socketTimeout)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return
	}
	raw, err := readFrame(conn)
	var req Request
	if err != nil || decodeFrame(raw, &req) != nil || validateRequest(req) != nil {
		_ = writeFrame(conn, socketReply{Error: "invalid"})
		return
	}
	result, err := s.runner.Run(ctx, req)
	if err != nil {
		code := "unavailable"
		if errors.Is(err, ErrInvalid) {
			code = "invalid"
		}
		_ = writeFrame(conn, socketReply{Error: code})
		return
	}
	if !ValidCheckpoint(result) || ((req.Op == "seed" || req.Op == "read") && result.Text != req.Text) {
		_ = writeFrame(conn, socketReply{Error: "unavailable"})
		return
	}
	_ = writeFrame(conn, socketReply{Checkpoint: &result})
}

var _ Runner = (*UnixClient)(nil)
var _ Runner = (*Engine)(nil)
