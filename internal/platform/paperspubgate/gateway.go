//go:build linux

// Package paperspubgate is a narrowly scoped S3 publication boundary.
// Publishers use a local Unix socket; they NEVER receive MinIO write keys.
package paperspubgate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"atoman/internal/platform/papersassets"
)

const prefix = "papers/immutable/v1/"

var keyRE = regexp.MustCompile("^(snapshots|environments|trusted-releases)/([0-9a-f]{64})\\.json$|^trusted-archives/([0-9a-f]{64})\\.tar$|^_probes/([0-9a-f]{32})$")

type Backend interface {
	PutIfAbsent(context.Context, string, string, []byte, string) error
	Read(context.Context, string, string, int64) ([]byte, error)
	PutFileIfAbsent(context.Context, string, string, *os.File, int64) error
	OpenArchive(context.Context, string, string) (io.ReadCloser, int64, error)
}

type Gateway struct {
	backend       Backend
	bucket        string
	tmp           string
	authorizedUID uint32
	slots         chan struct{}
}

func New(backend Backend, bucket, tmp string, authorizedUID uint32) (*Gateway, error) {
	if backend == nil || !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`).MatchString(bucket) ||
		bucket == "public" || strings.Contains(bucket, "..") || !filepath.IsAbs(tmp) {
		return nil, errors.New("invalid private gateway configuration")
	}
	resolved, err := filepath.EvalSymlinks(tmp)
	if err != nil || resolved != filepath.Clean(tmp) {
		return nil, errors.New("symlinked gateway staging path")
	}
	s, err := os.Lstat(tmp)
	if err != nil {
		return nil, err
	}
	owner, ok := s.Sys().(*syscall.Stat_t)
	if !s.IsDir() || s.Mode()&os.ModeSymlink != 0 || s.Mode().Perm()&0077 != 0 ||
		!ok || owner.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("gateway staging must be private and owned by its process")
	}
	return &Gateway{backend: backend, bucket: bucket, tmp: tmp, authorizedUID: authorizedUID, slots: make(chan struct{}, 1)}, nil
}
func classify(key string) (kind, digest string, maxBytes int64, err error) {
	if !strings.HasPrefix(key, prefix) {
		return "", "", 0, errors.New("unexpected object prefix")
	}
	m := keyRE.FindStringSubmatch(strings.TrimPrefix(key, prefix))
	if m == nil {
		return "", "", 0, errors.New("unapproved object key")
	}
	if m[1] != "" {
		kind = m[1]
		digest = m[2]
		maxBytes = papersassets.MaxSnapshotBytes + 65536
		if kind == "trusted-releases" {
			maxBytes = 8192
		}
	} else if m[3] != "" {
		kind = "trusted-archives"
		digest = m[3]
		maxBytes = papersassets.MaxTrustedArchiveBytes
	} else {
		kind = "_probes"
		digest = m[4]
		maxBytes = 256
	}
	return
}
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	peer, ok := r.Context().Value(peerContextKey{}).(uint32)
	if !ok || peer != g.authorizedUID {
		http.Error(w, "publisher not authorized", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPut {
		http.Error(w, "operation not permitted", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.RawQuery != "" || r.URL.Fragment != "" || strings.Contains(r.URL.EscapedPath(), "%") ||
		!strings.HasPrefix(r.URL.Path, "/v1/objects/") ||
		r.Header.Get("If-None-Match") != "*" {
		http.Error(w, "conditional-only publication required", http.StatusPreconditionRequired)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/v1/objects/")
	kind, digest, maxBytes, err := classify(key)
	if err != nil {
		http.Error(w, "unapproved key", http.StatusForbidden)
		return
	}
	if r.ContentLength < 1 || r.ContentLength > maxBytes {
		http.Error(w, "invalid length", http.StatusRequestEntityTooLarge)
		return
	}
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		http.Error(w, "gateway busy", http.StatusTooManyRequests)
		return
	}
	if r.Context().Err() != nil {
		return
	}
	f, err := os.CreateTemp(g.tmp, "pending-*.part")
	if err != nil {
		http.Error(w, "staging unavailable", http.StatusServiceUnavailable)
		return
	}
	defer func() { f.Close(); os.Remove(f.Name()) }()
	hash := sha256.New()
	count, err := io.CopyN(io.MultiWriter(f, hash), r.Body, r.ContentLength)
	if err != nil || count != r.ContentLength {
		http.Error(w, "incomplete request", http.StatusBadRequest)
		return
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if kind == "_probes" {
		payload := make([]byte, count)
		if _, err = f.ReadAt(payload, 0); err != nil || !strings.HasPrefix(string(payload), "papers-conditional-probe/v1/") {
			http.Error(w, "invalid probe", http.StatusUnprocessableEntity)
			return
		}
	} else if kind == "trusted-releases" {
		raw := make([]byte, count)
		if _, err = f.ReadAt(raw, 0); err != nil {
			http.Error(w, "invalid release", 422)
			return
		}
		var release papersassets.SignedArchiveRelease
		if json.Unmarshal(raw, &release) != nil || release.Claim.EnvironmentDigest != "sha256:"+digest ||
			len(release.Signature) == 0 || release.Claim.Version != 1 || release.Claim.Environment.Validate() != nil {
			http.Error(w, "invalid signed release envelope", 422)
			return
		}
	} else if actual != digest {
		http.Error(w, "content digest mismatch", http.StatusUnprocessableEntity)
		return
	}
	if err := f.Sync(); err != nil {
		http.Error(w, "staging sync failure", 502)
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		http.Error(w, "staging failure", 502)
		return
	}
	// The only backend mutation is S3 PutObject with If-None-Match pre-signed.
	if kind == "trusted-archives" {
		err = g.backend.PutFileIfAbsent(r.Context(), g.bucket, key, f, count)
	} else {
		raw, readErr := io.ReadAll(f)
		if readErr != nil {
			http.Error(w, "staging read failure", 502)
			return
		}
		ct := "application/json"
		if kind == "_probes" {
			ct = "application/octet-stream"
		}
		err = g.backend.PutIfAbsent(r.Context(), g.bucket, key, raw, ct)
	}
	if errors.Is(err, papersassets.ErrAlreadyExists) {
		w.WriteHeader(http.StatusPreconditionFailed)
		return
	}
	if err != nil {
		http.Error(w, "object store conditional publish failure", http.StatusBadGateway)
		return
	}
	// No "success" without an authenticated full readback (never trust ETag).
	var src io.ReadCloser
	var remoteLen int64
	if kind == "trusted-archives" {
		src, remoteLen, err = g.backend.OpenArchive(r.Context(), g.bucket, key)
	} else {
		var raw []byte
		raw, err = g.backend.Read(r.Context(), g.bucket, key, maxBytes)
		if err == nil {
			remoteLen = int64(len(raw))
			src = io.NopCloser(strings.NewReader(string(raw)))
		}
	}
	if err != nil || src == nil {
		http.Error(w, "read-after-write failed", 502)
		return
	}
	defer src.Close()
	if remoteLen != count {
		http.Error(w, "read-after-write length mismatch", 502)
		return
	}
	digestOfRead := sha256.New()
	n, err := io.Copy(digestOfRead, io.LimitReader(src, count+1))
	if err != nil || n != count || hex.EncodeToString(digestOfRead.Sum(nil)) != actual {
		http.Error(w, "read-after-write digest mismatch", 502)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

type peerContextKey struct{}

func gatewayContext(ctx context.Context, uid uint32) context.Context {
	return context.WithValue(ctx, peerContextKey{}, uid)
}
