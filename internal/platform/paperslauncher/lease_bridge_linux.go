//go:build linux

package paperslauncher

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"atoman/internal/platform/papersqueue"
)

// LeaseBridge exposes only the authoritative queue's ResolveLease method.
// The application is the sole database client; the root launcher has no
// database credentials. This endpoint is never registered on the public API.
type leaseReply struct {
	Claim papersqueue.Claim `json:"claim"`
	OK    bool              `json:"ok"`
}

func strictJSON(raw []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return ErrRejected
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return ErrRejected
	}
	return nil
}

func validBridgeDirectory(path string, owner uint32) error {
	if !filepath.IsAbs(path) || filepath.Ext(path) != ".sock" || len(path) > 99 {
		return ErrRejected
	}
	dir := filepath.Dir(path)
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || real != dir {
		return ErrRejected
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.IsDir() || st.Mode().Perm() != 0700 || stat.Uid != owner {
		return ErrRejected
	}
	return nil
}

// StartLeaseBridge listens in the app's private systemd runtime directory.
// Peer credentials, not a caller-supplied token or header, identify root.
// The small connection pool and deadline bound each authorization check.
func StartLeaseBridge(ctx context.Context, path string, verify LeaseVerifier) (<-chan struct{}, error) {
	if verify == nil || ctx == nil || os.Geteuid() == 0 {
		return nil, ErrRejected
	}
	if err := validBridgeDirectory(path, uint32(os.Geteuid())); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return nil, ErrRejected
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer listener.Close()
		go func() {
			select {
			case <-ctx.Done():
				listener.Close()
			case <-done:
			}
		}()
		slots := make(chan struct{}, 8)
		var wg sync.WaitGroup
		defer wg.Wait()
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			select {
			case slots <- struct{}{}:
			default:
				conn.Close()
				continue
			}
			wg.Add(1)
			go func(c *net.UnixConn) {
				defer wg.Done()
				defer func() { <-slots }()
				defer c.Close()
				u, g, err := credentials(c)
				if err != nil || u != 0 || g != 0 {
					return
				}
				if c.SetDeadline(time.Now().Add(4*time.Second)) != nil {
					return
				}
				raw, err := bufio.NewReaderSize(c, maxFrame).ReadSlice('\n')
				if err != nil {
					return
				}
				var claim papersqueue.Claim
				if strictJSON(raw, &claim) != nil || !claim.Valid() {
					_ = json.NewEncoder(c).Encode(leaseReply{})
					return
				}
				bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
				canonical, err := verify(bounded, claim)
				if err != nil || !canonical.Valid() {
					_ = json.NewEncoder(c).Encode(leaseReply{})
					return
				}
				_ = json.NewEncoder(c).Encode(leaseReply{Claim: canonical, OK: true})
			}(conn)
		}
	}()
	return done, nil
}

// RemoteLeaseVerifier is used only by the root launcher, with an immutable
// local socket address and expected UID. Neither SQL nor filesystem paths
// supplied by clients can be sent to the application.
func RemoteLeaseVerifier(path string, appUID uint32) LeaseVerifier {
	return func(ctx context.Context, claim papersqueue.Claim) (papersqueue.Claim, error) {
		if appUID == 0 || os.Geteuid() != 0 || !claim.Valid() {
			return papersqueue.Claim{}, ErrRejected
		}
		if err := validBridgeDirectory(path, appUID); err != nil {
			return papersqueue.Claim{}, err
		}
		d := net.Dialer{Timeout: 2 * time.Second}
		c, err := d.DialContext(ctx, "unix", path)
		if err != nil {
			return papersqueue.Claim{}, err
		}
		defer c.Close()
		u, _, err := credentials(c.(*net.UnixConn))
		if err != nil || u != appUID {
			return papersqueue.Claim{}, ErrAuth
		}
		deadline := time.Now().Add(4 * time.Second)
		if until, ok := ctx.Deadline(); ok && until.Before(deadline) {
			deadline = until
		}
		if err = c.SetDeadline(deadline); err != nil {
			return papersqueue.Claim{}, err
		}
		raw, err := json.Marshal(claim)
		if err != nil || len(raw) >= maxFrame-1 {
			return papersqueue.Claim{}, ErrRejected
		}
		if _, err = c.Write(append(raw, '\n')); err != nil {
			return papersqueue.Claim{}, err
		}
		data, err := bufio.NewReaderSize(c, maxFrame).ReadSlice('\n')
		if err != nil {
			return papersqueue.Claim{}, err
		}
		var reply leaseReply
		if strictJSON(data, &reply) != nil || !reply.OK || !reply.Claim.Valid() {
			return papersqueue.Claim{}, ErrRejected
		}
		got := reply.Claim
		if got.Kind != claim.Kind || got.Job.ID != claim.Job.ID || got.WorkerID != claim.WorkerID || got.Token != claim.Token || got.Epoch != claim.Epoch || got.SandboxID != claim.SandboxID {
			return papersqueue.Claim{}, ErrRejected
		}
		return got, nil
	}
}

func CheckLauncher(ctx context.Context, path string) error {
	c, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", path)
	if err != nil {
		return err
	}
	defer c.Close()
	u, g, err := credentials(c.(*net.UnixConn))
	if err != nil || u != 0 || g != 0 {
		return ErrAuth
	}
	return nil
}
