//go:build linux

package paperslauncher

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"atoman/internal/platform/papersqueue"
	"atoman/internal/platform/paperssandbox"
)

// Client implements the ordinary, non-root Worker side of SandboxExecutor.
// No sandbox filesystem, Docker, runsc or systemd credentials are available.
type Client struct{ Socket string }

func NewClient(socket string) (*Client, error) {
	if socket == "" || socket[0] != '/' || len(socket) > 99 {
		return nil, ErrRejected
	}
	return &Client{Socket: socket}, nil
}

// transportUnknown is never an attestation of privileged cleanup. Every
// disconnect, timeout, malformed reply, or pre-execution rejection leaves the
// original DB job fenced for recovery rather than pretending cleanup failed.
func transportUnknown(err error) error {
	return fmt.Errorf("%w: %v", papersqueue.ErrExecutionUncertain, err)
}

func (c *Client) call(ctx context.Context, op string, claim papersqueue.Claim) (response, error) {
	var res response
	if c == nil || (op != "run" && op != "reap") || !claim.Valid() {
		return res, ErrRejected
	}
	requestRaw, err := json.Marshal(request{Operation: op, Claim: claim})
	if err != nil {
		return res, err
	}
	if len(requestRaw) >= maxFrame-2 {
		return res, ErrRejected
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return res, transportUnknown(err)
	}
	defer conn.Close()
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return res, transportUnknown(ErrAuth)
	}
	peerUID, _, err := credentials(unixConn)
	if err != nil {
		return res, transportUnknown(err)
	}
	if peerUID != 0 {
		return res, transportUnknown(ErrAuth)
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	defer close(done)
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return res, transportUnknown(err)
	}
	requestRaw = append(requestRaw, '\n')
	if _, err = conn.Write(requestRaw); err != nil {
		return res, transportUnknown(err)
	}
	_ = conn.SetWriteDeadline(time.Time{})
	if err = conn.SetReadDeadline(time.Now().Add(6 * time.Minute)); err != nil {
		return res, transportUnknown(err)
	}
	limit := json.NewDecoder(&readLimited{conn: conn, max: 600 << 10})
	if err = limit.Decode(&res); err != nil {
		return response{}, transportUnknown(fmt.Errorf("launcher response unavailable: %w", err))
	}
	if res.Error != "" {
		if res.Error == "sandbox_operation_failed" {
			// The Launcher returned a structured execution result. The queue will
			// independently require CleanupVerified before updating any job state.
			return res, fmt.Errorf("privileged launcher: %s", res.Error)
		}
		return response{}, transportUnknown(fmt.Errorf("launcher rejected request: %s", res.Error))
	}
	if !res.Verified {
		return res, transportUnknown(ErrRejected)
	}
	return res, nil
}

// readLimited refuses unbounded responses, even from a root-owned daemon.
type readLimited struct {
	conn net.Conn
	max  int
}

func (r *readLimited) Read(p []byte) (int, error) {
	if r.max <= 0 {
		return 0, ErrRejected
	}
	if len(p) > r.max {
		p = p[:r.max]
	}
	n, err := r.conn.Read(p)
	r.max -= n
	return n, err
}

func (c *Client) Run(ctx context.Context, claim papersqueue.Claim) (paperssandbox.Result, error) {
	reply, err := c.call(ctx, "run", claim)
	if reply.Result.JobID != "" && reply.Result.JobID != claim.SandboxID {
		return paperssandbox.Result{}, papersqueue.ErrExecutionUncertain
	}
	if reply.Result.JobID == "" {
		reply.Result.JobID = claim.SandboxID
	}
	return reply.Result, err
}
func (c *Client) Reap(ctx context.Context, claim papersqueue.Claim) error {
	reply, err := c.call(ctx, "reap", claim)
	if err != nil {
		return err
	}
	if !reply.Verified {
		return ErrRejected
	}
	return nil
}

var _ papersqueue.SandboxExecutor = (*Client)(nil)
