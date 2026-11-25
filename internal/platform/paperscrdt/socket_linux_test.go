//go:build linux

package paperscrdt

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type runnerFunc func(context.Context, Request) (Checkpoint, error)

func (f runnerFunc) Run(ctx context.Context, r Request) (Checkpoint, error) { return f(ctx, r) }

func startSocket(t *testing.T, runner Runner, uid uint32) (*UnixClient, string) {
	t.Helper()
	// Keep Linux sockaddr_un within its pathname limit, even on long test names.
	dir, err := os.MkdirTemp("/tmp", "crdt-sock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "worker.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0660); err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	s, err := NewSocketServer(runner, uid)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("server did not stop")
		}
	})
	client, err := newUnixClient(path, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	return client, path
}
func TestSocketRejectsOversizedFramesAndUnknownFields(t *testing.T) {
	for _, n := range []uint32{0, maxJSON + 1, 0xffffffff} {
		var h [4]byte
		binary.BigEndian.PutUint32(h[:], n)
		if _, err := readFrame(bytes.NewReader(h[:])); !errors.Is(err, ErrInvalid) {
			t.Fatalf("size %d: %v", n, err)
		}
	}
	var req Request
	for _, raw := range []string{`{"op":"seed","text":"","path":"/tmp/file"}`, `{"op":"seed"} {}`} {
		if err := decodeFrame([]byte(raw), &req); !errors.Is(err, ErrInvalid) {
			t.Fatal("unknown fields or trailing JSON accepted")
		}
	}
}
func TestSocketAuthenticatesPeerAndRejectsInvalidRequest(t *testing.T) {
	var calls atomic.Int32
	runner := runnerFunc(func(_ context.Context, r Request) (Checkpoint, error) {
		calls.Add(1)
		return Checkpoint{Text: r.Text, State: []byte{1}, Vector: []byte{2}}, nil
	})
	client, path := startSocket(t, runner, uint32(os.Geteuid()))
	result, err := client.Run(context.Background(), Request{Op: "seed", Text: "fixture"})
	if err != nil || result.Text != "fixture" {
		t.Fatalf("valid seed failed: %v", err)
	}
	if _, err = client.Run(context.Background(), Request{Op: "seed", State: []byte{1}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid seed accepted")
	}
	wrong, err := newUnixClient(path, uint32(os.Geteuid())+1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = wrong.Run(context.Background(), Request{Op: "seed"}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("untrusted listener accepted")
	}
	rejected, _ := startSocket(t, runner, uint32(os.Geteuid())+1)
	if _, err = rejected.Run(context.Background(), Request{Op: "seed"}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unauthorized caller accepted")
	}
	if calls.Load() != 1 {
		t.Fatalf("unexpected engine calls: %d", calls.Load())
	}
	if err = os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Run(context.Background(), Request{Op: "seed"}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("world accessible socket accepted")
	}
}
func TestSocketAdmissionAndCancellation(t *testing.T) {
	started := make(chan struct{}, 2)
	runner := runnerFunc(func(ctx context.Context, r Request) (Checkpoint, error) {
		started <- struct{}{}
		<-ctx.Done()
		return Checkpoint{}, ctx.Err()
	})
	client, _ := startSocket(t, runner, uint32(os.Geteuid()))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := client.Run(ctx, Request{Op: "seed"}); done <- err }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("request not admitted")
		}
	}
	begin := time.Now()
	if _, err := client.Run(context.Background(), Request{Op: "seed"}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("excess request not rejected")
	}
	if time.Since(begin) > 2*time.Second {
		t.Fatal("busy request queued")
	}
	cancel()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if !errors.Is(err, ErrUnavailable) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancelled caller blocked")
		}
	}
}
func TestSocketSanitizesRuntimeFailureAndChecksReply(t *testing.T) {
	runner := runnerFunc(func(_ context.Context, r Request) (Checkpoint, error) {
		switch r.Text {
		case "bad":
			return Checkpoint{}, ErrInvalid
		case "untrusted":
			return Checkpoint{}, errors.New("synthetic secret")
		default:
			return Checkpoint{Text: "wrong", State: []byte{1}, Vector: []byte{1}}, nil
		}
	})
	client, _ := startSocket(t, runner, uint32(os.Geteuid()))
	for _, r := range []struct {
		text string
		want error
	}{{"bad", ErrInvalid}, {"untrusted", ErrUnavailable}, {"mismatch", ErrUnavailable}} {
		if _, err := client.Run(context.Background(), Request{Op: "seed", Text: r.text}); !errors.Is(err, r.want) {
			t.Fatalf("unexpected result for %s: %v", r.text, err)
		}
	}
}
func TestIsolatedSocketRuntime(t *testing.T) {
	path := os.Getenv("PAPERS_CRDT_TEST_SOCKET")
	runtime := os.Getenv("PAPERS_CRDT_TEST_RUNTIME")
	if path == "" || runtime == "" {
		t.Skip("run scripts/test-papers-crdt-isolation.sh")
	}
	client, err := NewUnixClient(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	text := "isolated " + string(rune(0x4e2d)) + string(rune(0x6587))
	seeded, err := client.Run(ctx, Request{Op: "seed", Text: text})
	if err != nil || seeded.Text != text {
		t.Fatalf("isolated seed failed: %v", err)
	}
	peerInput, err := json.Marshal(map[string]any{"state": seeded.State, "position": 0, "remove": 0, "insert": "peer: "})
	if err != nil {
		t.Fatal(err)
	}
	peer := exec.CommandContext(ctx, "/usr/bin/node", filepath.Join(runtime, "test-peer.mjs"))
	peer.Stdin = bytes.NewReader(peerInput)
	raw, err := peer.Output()
	if err != nil {
		t.Fatal(err)
	}
	update, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	merged, err := client.Run(ctx, Request{Op: "merge", Text: text, State: seeded.State, Update: update})
	if err != nil || merged.Text != "peer: "+text {
		t.Fatalf("isolated merge failed: %v", err)
	}
	read, err := client.Run(ctx, Request{Op: "read", Text: merged.Text, State: merged.State})
	if err != nil || read.Text != merged.Text || !bytes.Equal(read.State, merged.State) {
		t.Fatalf("isolated read failed: %v", err)
	}
	if _, err = client.Run(ctx, Request{Op: "merge", Text: merged.Text, State: merged.State, Update: []byte{255, 255, 255}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("malformed update was not rejected")
	}
}
