//go:build linux

package paperspubgate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"atoman/internal/platform/papersassets"
	"github.com/stretchr/testify/require"
)

type memoryBackend struct {
	mu          sync.Mutex
	objects     map[string][]byte
	corruptRead bool
}

func (m *memoryBackend) PutIfAbsent(_ context.Context, _, key string, blob []byte, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.objects[key]; exists {
		return papersassets.ErrAlreadyExists
	}
	m.objects[key] = bytes.Clone(blob)
	return nil
}
func (m *memoryBackend) Read(_ context.Context, _, key string, _ int64) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := bytes.Clone(m.objects[key])
	if m.corruptRead && len(out) > 0 {
		out[0] ^= 1
	}
	return out, nil
}
func (m *memoryBackend) PutFileIfAbsent(ctx context.Context, bucket, key string, f *os.File, size int64) error {
	data, err := io.ReadAll(io.LimitReader(f, size+1))
	if err != nil {
		return err
	}
	return m.PutIfAbsent(ctx, bucket, key, data, "application/x-tar")
}
func (m *memoryBackend) OpenArchive(ctx context.Context, bucket, key string) (io.ReadCloser, int64, error) {
	data, err := m.Read(ctx, bucket, key, 4<<30)
	if err != nil {
		return nil, 0, err
	}
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
}
func testSocket(t *testing.T, authorizedUID uint32) (*Gateway, *memoryBackend, *http.Client) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	tmp := filepath.Join(root, "staging")
	require.NoError(t, os.Mkdir(tmp, 0700))
	backend := &memoryBackend{objects: map[string][]byte{}}
	gw, err := New(backend, "papers-private-p0-unit", tmp, authorizedUID)
	require.NoError(t, err)
	socket := filepath.Join(root, "gateway.sock")
	srv, err := NewUnixServer(gw, socket, os.Getgid())
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- srv.Serve() }()
	t.Cleanup(func() {
		require.NoError(t, srv.Close())
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(3 * time.Second):
			t.Error("Unix gateway did not stop")
		}
		entries, err := os.ReadDir(tmp)
		require.NoError(t, err)
		require.Empty(t, entries)
	})
	transport := &http.Transport{DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}}
	return gw, backend, &http.Client{Transport: transport}
}
func gatewayPut(t *testing.T, client *http.Client, key string, raw []byte, conditional bool) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, "http://gateway/v1/objects/"+key, bytes.NewReader(raw))
	require.NoError(t, err)
	if conditional {
		req.Header.Set("If-None-Match", "*")
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}
func shaKey(kind string, b []byte, ext string) string {
	d := sha256.Sum256(b)
	return prefix + kind + "/" + hex.EncodeToString(d[:]) + ext
}
func TestSocketMandatoryPeerAuthAndImmutableWrites(t *testing.T) {
	gw, backend, client := testSocket(t, uint32(os.Getuid()))
	blob := []byte(`{"some":"private-manifest"}`)
	key := shaKey("snapshots", blob, ".json")
	// A raw Handler cannot use a spoofed HTTP header to bypass SO_PEERCRED.
	req := httptest.NewRequest("PUT", "/v1/objects/"+key, bytes.NewReader(blob))
	req.Header.Set("If-None-Match", "*")
	req.Header.Set("X-Peer-Uid", "0")
	recorder := httptest.NewRecorder()
	gw.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Equal(t, http.StatusPreconditionRequired, gatewayPut(t, client, key, blob, false))
	require.Equal(t, http.StatusCreated, gatewayPut(t, client, key, blob, true))
	require.Equal(t, http.StatusPreconditionFailed, gatewayPut(t, client, key, blob, true))
	require.Equal(t, http.StatusUnprocessableEntity, gatewayPut(t, client, key, []byte("blind-replace"), true))
	probeKey := prefix + "_probes/" + strings.Repeat("a", 32)
	require.Equal(t, http.StatusCreated, gatewayPut(t, client, probeKey, []byte("papers-conditional-probe/v1/first/test"), true))
	require.Equal(t, http.StatusPreconditionFailed, gatewayPut(t, client, probeKey, []byte("papers-conditional-probe/v1/second/test"), true))
	backend.mu.Lock()
	require.Equal(t, blob, backend.objects[key])
	backend.mu.Unlock()
	require.Equal(t, http.StatusForbidden, gatewayPut(t, client, prefix+"snapshots/../../escape", blob, true))
	require.Equal(t, http.StatusUnprocessableEntity, gatewayPut(t, client, shaKey("snapshots", blob, ".json"), []byte("different"), true))
	req, err := http.NewRequest("DELETE", "http://gateway/v1/objects/"+key, nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}
func TestGatewayRejectsWrongUIDAndFalseReadback(t *testing.T) {
	_, _, client := testSocket(t, uint32(os.Getuid()+1))
	data := []byte("restricted")
	require.Equal(t, http.StatusForbidden, gatewayPut(t, client, shaKey("snapshots", data, ".json"), data, true))
	_, backend, allowed := testSocket(t, uint32(os.Getuid()))
	backend.corruptRead = true
	require.Equal(t, http.StatusBadGateway, gatewayPut(t, allowed, shaKey("snapshots", data, ".json"), data, true))
}
func TestGatewayStreamsArchiveAndLimitsKeys(t *testing.T) {
	_, backend, client := testSocket(t, uint32(os.Getuid()))
	blob := bytes.Repeat([]byte("texlive-signed-archive"), 30000)
	key := shaKey("trusted-archives", blob, ".tar")
	require.Equal(t, http.StatusCreated, gatewayPut(t, client, key, blob, true))
	backend.mu.Lock()
	require.Len(t, backend.objects[key], len(blob))
	backend.mu.Unlock()
	// The caller cannot choose arbitrary keys, buckets, or delete a version.
	require.Equal(t, http.StatusForbidden, gatewayPut(t, client, prefix+"other/"+strings.Repeat("a", 64)+".json", []byte("small"), true))
	require.Equal(t, http.StatusForbidden, gatewayPut(t, client, "public/objects", []byte("small"), true))
}

func TestGatewayRejectsUnsafeLocalPaths(t *testing.T) {
	root := t.TempDir()
	backend := &memoryBackend{objects: map[string][]byte{}}
	staging := filepath.Join(root, "staging")
	require.NoError(t, os.Mkdir(staging, 0770))
	require.NoError(t, os.Chmod(staging, 0770))
	_, err := New(backend, "papers-private-p0-unit", staging, uint32(os.Getuid()))
	require.Error(t, err)
	require.NoError(t, os.Chmod(staging, 0700))
	linked := filepath.Join(root, "linked")
	require.NoError(t, os.Symlink(staging, linked))
	_, err = New(backend, "papers-private-p0-unit", linked, uint32(os.Getuid()))
	require.Error(t, err)
	_, err = New(backend, "../../public", staging, uint32(os.Getuid()))
	require.Error(t, err)
	gateway, err := New(backend, "papers-private-p0-unit", staging, uint32(os.Getuid()))
	require.NoError(t, err)
	socketRoot := filepath.Join(root, "uds")
	require.NoError(t, os.Mkdir(socketRoot, 0770))
	require.NoError(t, os.Chmod(socketRoot, 0770))
	_, err = NewUnixServer(gateway, filepath.Join(socketRoot, "publisher.sock"), os.Getgid())
	require.Error(t, err, "a publisher-writable socket directory allows endpoint substitution")
}

func TestStagingHeadroomRequiresTwoGiBReserve(t *testing.T) {
	require.False(t, hasStagingHeadroom(stagingReserveBytes, 1))
	require.False(t, hasStagingHeadroom(stagingReserveBytes+31, 32))
	require.True(t, hasStagingHeadroom(stagingReserveBytes+32, 32))
	require.False(t, hasStagingHeadroom(^uint64(0), 0))
	available, err := stagingAvailableBytes(t.TempDir())
	require.NoError(t, err)
	require.Greater(t, available, uint64(0))
}

type blockedPublicationBackend struct {
	*memoryBackend
	entered chan struct{}
	release chan struct{}
}

func (b *blockedPublicationBackend) PutIfAbsent(ctx context.Context, bucket, key string, raw []byte, contentType string) error {
	select {
	case b.entered <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-b.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return b.memoryBackend.PutIfAbsent(ctx, bucket, key, raw, contentType)
}

func TestGatewayConcurrentPublishIsBoundedWithoutExtraStaging(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "stage")
	require.NoError(t, os.Mkdir(staging, 0700))
	b := &blockedPublicationBackend{
		memoryBackend: &memoryBackend{objects: map[string][]byte{}},
		entered:       make(chan struct{}, 1),
		release:       make(chan struct{}),
	}
	gw, err := New(b, "papers-private-p0-unit", staging, uint32(os.Getuid()))
	require.NoError(t, err)
	firstData := []byte(`{"id":"first"}`)
	first := httptest.NewRequest(http.MethodPut, "/v1/objects/"+shaKey("snapshots", firstData, ".json"), bytes.NewReader(firstData))
	first.Header.Set("If-None-Match", "*")
	first = first.WithContext(gatewayContext(first.Context(), uint32(os.Getuid())))
	firstRecorder := httptest.NewRecorder()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		gw.ServeHTTP(firstRecorder, first)
	}()
	var once sync.Once
	release := func() { once.Do(func() { close(b.release) }) }
	defer release()
	select {
	case <-b.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first publication did not enter the backend")
	}
	secondData := []byte(`{"id":"second"}`)
	second := httptest.NewRequest(http.MethodPut, "/v1/objects/"+shaKey("snapshots", secondData, ".json"), bytes.NewReader(secondData))
	second.Header.Set("If-None-Match", "*")
	second = second.WithContext(gatewayContext(second.Context(), uint32(os.Getuid())))
	secondRecorder := httptest.NewRecorder()
	gw.ServeHTTP(secondRecorder, second)
	require.Equal(t, http.StatusTooManyRequests, secondRecorder.Code)
	entries, err := os.ReadDir(staging)
	require.NoError(t, err)
	require.Len(t, entries, 1, "a busy gateway must not stage a second upload")
	release()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("gateway failed to complete an accepted upload")
	}
	require.Equal(t, http.StatusCreated, firstRecorder.Code)
	entries, err = os.ReadDir(staging)
	require.NoError(t, err)
	require.Empty(t, entries, "accepted uploads must release staging after completion")
}

func TestGatewayFailsClosedOnUnavailableStagingCapacity(t *testing.T) {
	gw, backend, client := testSocket(t, uint32(os.Getuid()))
	data := []byte(`{"too":"full"}`)
	key := shaKey("snapshots", data, ".json")
	gw.available = func(string) (uint64, error) { return stagingReserveBytes + uint64(len(data)) - 1, nil }
	require.Equal(t, http.StatusInsufficientStorage, gatewayPut(t, client, key, data, true))
	gw.available = func(string) (uint64, error) { return 0, errors.New("statfs failed") }
	require.Equal(t, http.StatusServiceUnavailable, gatewayPut(t, client, key, data, true))
	backend.mu.Lock()
	require.Empty(t, backend.objects, "no object may be published when staging cannot be reserved")
	backend.mu.Unlock()
}
