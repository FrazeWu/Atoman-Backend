package papersassets

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeArchiveStore struct {
	*fakeConditional
	mu        sync.Mutex
	blobs     map[string][]byte
	noLock    bool
	badLength bool
}

func newFakeArchiveStore() *fakeArchiveStore {
	return &fakeArchiveStore{fakeConditional: newFakeConditional(), blobs: map[string][]byte{}}
}
func (f *fakeArchiveStore) LockCompliant(_ context.Context, _ string, _ int64) (bool, error) {
	return !f.noLock, nil
}
func (f *fakeArchiveStore) PutFileIfAbsent(_ context.Context, bucket, key string, file *os.File, size int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := bucket + "/" + key
	if _, exists := f.blobs[name]; exists {
		return ErrAlreadyExists
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(file, size+1))
	if err != nil {
		return err
	}
	f.blobs[name] = raw
	return nil
}
func (f *fakeArchiveStore) OpenArchive(_ context.Context, bucket, key string) (io.ReadCloser, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, ok := f.blobs[bucket+"/"+key]
	if !ok {
		return nil, 0, os.ErrNotExist
	}
	len := int64(len(raw))
	if f.badLength {
		len++
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), raw...))), len, nil
}
func fakeTrusted(t *testing.T) (*TrustedArchiveRegistry, *fakeArchiveStore, ed25519.PrivateKey) {
	t.Helper()
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	store := newFakeArchiveStore()
	base, err := NewPrivateS3Registry(store, "papers-trusted-test", "shared-public-test")
	require.NoError(t, err)
	trusted, err := NewTrustedArchiveRegistry(base, map[string]ed25519.PublicKey{"offline-publisher": pub}, 1)
	require.NoError(t, err)
	require.NoError(t, trusted.ProbePublisher(context.Background()))
	return trusted, store, private
}
func makeTrustedTarFixture(t *testing.T) (string, Environment) {
	t.Helper()
	filepath := filepath.Join(t.TempDir(), "fixture-rootfs.tar")
	f, err := os.OpenFile(filepath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	require.NoError(t, err)
	tw := tar.NewWriter(f)
	blob := bytes.Repeat([]byte("Trusted TeX environment content.\n"), 128)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "usr", Mode: 0755, Typeflag: tar.TypeDir}))
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "usr/share", Mode: 0755, Typeflag: tar.TypeDir}))
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "usr/share/texlive.tex", Mode: 0644, Size: int64(len(blob)), Typeflag: tar.TypeReg}))
	_, err = tw.Write(blob)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, f.Close())
	require.NoError(t, os.Chmod(filepath, 0400))
	hash, err := HashFile(filepath)
	require.NoError(t, err)
	env := testEnv()
	env.ArchiveDigest = hash
	return filepath, env
}

func TestTrustedSignedArchiveRoundtripAndCache(t *testing.T) {
	registry, _, private := fakeTrusted(t)
	archive, env := makeTrustedTarFixture(t)
	ctx := context.Background()
	digest, err := registry.PublishTrustedArchive(ctx, env, archive, "offline-publisher", private)
	require.NoError(t, err)
	release, err := registry.LoadRelease(ctx, digest)
	require.NoError(t, err)
	require.Equal(t, env, release.Claim.Environment)
	require.Equal(t, int64(len(mustReadTrusted(t, archive))), release.Claim.ArchiveBytes)
	cacheDir := t.TempDir()
	require.NoError(t, os.Chmod(cacheDir, 0700))
	cache, err := NewTrustedArchiveCache(registry, cacheDir)
	require.NoError(t, err)
	installed, err := cache.FetchArchive(ctx, digest)
	require.NoError(t, err)
	require.NoError(t, verifyLocalCachedFile(ctx, installed, release.Claim.ArchiveBytes, env.ArchiveDigest))
	second, err := cache.FetchArchive(ctx, digest)
	require.NoError(t, err)
	require.Equal(t, installed, second)
	info, err := os.Stat(installed)
	require.NoError(t, err)
	require.Zero(t, info.Mode().Perm()&0222)
	gate := &TrustedCompileAssetReader{Sources: registry.manifest, Releases: registry}
	checked, err := gate.VerifyCompileArchive(ctx, digest, installed)
	require.NoError(t, err)
	require.Equal(t, env, checked)
	require.NoError(t, os.Chmod(installed, 0600))
	require.NoError(t, os.WriteFile(installed, []byte("corrupt"), 0600))
	_, err = cache.FetchArchive(ctx, digest)
	require.ErrorIs(t, err, ErrIntegrity)
	_, err = gate.VerifyCompileArchive(ctx, digest, installed)
	require.ErrorIs(t, err, ErrIntegrity)
}
func mustReadTrusted(t *testing.T, p string) []byte {
	t.Helper()
	raw, err := os.ReadFile(p)
	require.NoError(t, err)
	return raw
}
func TestSignedReleaseRejectsTamperAndUnknownPublisher(t *testing.T) {
	registry, store, private := fakeTrusted(t)
	file, env := makeTrustedTarFixture(t)
	digest, err := registry.PublishTrustedArchive(context.Background(), env, file, "offline-publisher", private)
	require.NoError(t, err)
	release, err := registry.LoadRelease(context.Background(), digest)
	require.NoError(t, err)
	release.Claim.ArchiveBytes += 1
	require.ErrorIs(t, registry.VerifyRelease(release, digest), ErrUntrustedPublisher)
	release.Claim.ArchiveBytes -= 1
	release.Claim.PublisherKeyID = "evil-publisher"
	require.ErrorIs(t, registry.VerifyRelease(release, digest), ErrUntrustedPublisher)
	release.Claim.PublisherKeyID = "offline-publisher"
	release.Signature = strings.Repeat("A", len(release.Signature))
	require.ErrorIs(t, registry.VerifyRelease(release, digest), ErrUntrustedPublisher)
	store.fakeConditional.mu.Lock()
	manifestKey, _ := trustedReleaseKey(digest)
	store.fakeConditional.objects[registry.manifest.bucket+"/"+manifestKey] = []byte("tampered")
	store.fakeConditional.mu.Unlock()
	_, err = registry.LoadRelease(context.Background(), digest)
	require.ErrorIs(t, err, ErrIntegrity)
}
func TestTrustedArchiveTamperedBinaryNeverPublished(t *testing.T) {
	registry, store, private := fakeTrusted(t)
	file, env := makeTrustedTarFixture(t)
	src := mustReadTrusted(t, file)
	key, err := trustedArchiveKey(env.ArchiveDigest)
	require.NoError(t, err)
	store.mu.Lock()
	store.blobs[registry.manifest.bucket+"/"+key] = []byte("tampered")
	store.mu.Unlock()
	_, err = registry.PublishTrustedArchive(context.Background(), env, file, "offline-publisher", private)
	require.ErrorIs(t, err, ErrIntegrity)
	_, err = registry.LoadRelease(context.Background(), HashBytes(mustMarshalEnv(t, env)))
	require.Error(t, err)
	require.Equal(t, src, mustReadTrusted(t, file))
}
func mustMarshalEnv(t *testing.T, e Environment) []byte {
	t.Helper()
	raw, err := json.Marshal(e)
	require.NoError(t, err)
	return raw
}
func TestTrustedArchiveUnsignedOrDisabledLockRejected(t *testing.T) {
	registry, store, private := fakeTrusted(t)
	f, env := makeTrustedTarFixture(t)
	store.noLock = true
	require.ErrorIs(t, registry.ProbePublisher(context.Background()), ErrConditionalUnsupported)
	_, err := registry.PublishTrustedArchive(context.Background(), env, f, "offline-publisher", private)
	require.ErrorIs(t, err, ErrConditionalUnsupported)
	store.noLock = false
	require.NoError(t, registry.ProbePublisher(context.Background()))
	_, unknown, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, err = registry.PublishTrustedArchive(context.Background(), env, f, "offline-publisher", unknown)
	require.ErrorIs(t, err, ErrUntrustedPublisher)
}
func TestTrustedTarRejectsTraversalDevicesAndEscapeLinks(t *testing.T) {
	scenarios := []struct {
		name string
		hdr  tar.Header
	}{
		{"traversal", tar.Header{Name: "../escape", Typeflag: tar.TypeReg, Size: 16, Mode: 0644}},
		{"absolute", tar.Header{Name: "/etc/shadow", Typeflag: tar.TypeReg, Size: 16, Mode: 0644}},
		{"device", tar.Header{Name: "dev/sda", Typeflag: tar.TypeChar, Mode: 0644}},
		{"symlink-escape", tar.Header{Name: "bin/evil", Linkname: "../../etc/passwd", Typeflag: tar.TypeSymlink, Mode: 0777}},
		{"hardlink-escape", tar.Header{Name: "share/evil", Linkname: "../evil", Typeflag: tar.TypeLink, Mode: 0644}},
		{"abs-link", tar.Header{Name: "bin/evil", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink, Mode: 0777}},
	}
	for _, tc := range scenarios {
		t.Run(tc.name, func(t *testing.T) {
			f, err := os.CreateTemp(t.TempDir(), "bad-*.tar")
			require.NoError(t, err)
			writer := tar.NewWriter(f)
			require.NoError(t, writer.WriteHeader(&tc.hdr))
			if tc.hdr.Size > 0 {
				_, err = writer.Write(bytes.Repeat([]byte("x"), int(tc.hdr.Size)))
				require.NoError(t, err)
			}
			require.NoError(t, writer.Close())
			_, err = f.Seek(0, io.SeekStart)
			require.NoError(t, err)
			require.ErrorIs(t, ValidateTrustedTar(context.Background(), f), ErrInvalid)
			require.NoError(t, f.Close())
		})
	}
}
func TestTrustedArchiveConcurrentSameRelease(t *testing.T) {
	registry, _, private := fakeTrusted(t)
	archive, env := makeTrustedTarFixture(t)
	const workers = 12
	var wg sync.WaitGroup
	results := make(chan string, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := registry.PublishTrustedArchive(context.Background(), env, archive, "offline-publisher", private)
			if err != nil {
				errs <- err
				return
			}
			results <- id
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var expected string
	for id := range results {
		if expected == "" {
			expected = id
		}
		require.Equal(t, expected, id)
	}
	require.NotEmpty(t, expected)
}
func TestTrustedArchiveCacheRejectsUnsafeRoot(t *testing.T) {
	registry, _, _ := fakeTrusted(t)
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0777))
	_, err := NewTrustedArchiveCache(registry, dir)
	require.ErrorIs(t, err, ErrInvalid)
	safe := t.TempDir()
	link := filepath.Join(safe, "linked")
	require.NoError(t, os.Symlink(dir, link))
	_, err = NewTrustedArchiveCache(registry, link)
	require.ErrorIs(t, err, ErrInvalid)
}

var _ = errors.Is

func TestTrustedArchiveDownloaderRejectsCorruptOrOverlongRemote(t *testing.T) {
	for _, mode := range []string{"tamper", "wrong_length", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			registry, store, private := fakeTrusted(t)
			file, env := makeTrustedTarFixture(t)
			digest, err := registry.PublishTrustedArchive(context.Background(), env, file, "offline-publisher", private)
			require.NoError(t, err)
			archiveKey, err := trustedArchiveKey(env.ArchiveDigest)
			require.NoError(t, err)
			store.mu.Lock()
			switch mode {
			case "tamper":
				store.blobs[registry.manifest.bucket+"/"+archiveKey] = bytes.Repeat([]byte("x"), len(store.blobs[registry.manifest.bucket+"/"+archiveKey]))
			case "wrong_length":
				store.badLength = true
			case "truncated":
				store.blobs[registry.manifest.bucket+"/"+archiveKey] = []byte("partial")
			}
			store.mu.Unlock()
			cacheRoot := t.TempDir()
			require.NoError(t, os.Chmod(cacheRoot, 0700))
			cache, err := NewTrustedArchiveCache(registry, cacheRoot)
			require.NoError(t, err)
			_, err = cache.FetchArchive(context.Background(), digest)
			require.ErrorIs(t, err, ErrIntegrity)
			entries, err := os.ReadDir(cacheRoot)
			require.NoError(t, err)
			require.Empty(t, entries, "invalid remote objects cannot leave trusted files behind")
		})
	}
}
func TestTrustedArchiveCacheRefusesSymlinkAsExistingObject(t *testing.T) {
	reg, _, private := fakeTrusted(t)
	source, env := makeTrustedTarFixture(t)
	digest, err := reg.PublishTrustedArchive(context.Background(), env, source, "offline-publisher", private)
	require.NoError(t, err)
	cacheRoot := t.TempDir()
	require.NoError(t, os.Chmod(cacheRoot, 0700))
	cache, err := NewTrustedArchiveCache(reg, cacheRoot)
	require.NoError(t, err)
	path, err := cache.cachePath(env.ArchiveDigest)
	require.NoError(t, err)
	require.NoError(t, os.Symlink(source, path))
	_, err = cache.FetchArchive(context.Background(), digest)
	require.ErrorIs(t, err, ErrIntegrity)
	info, err := os.Lstat(path)
	require.NoError(t, err)
	require.True(t, info.Mode()&os.ModeSymlink != 0)
}
func TestTrustedArchiveRejectsUnsignedSourceAndWorldWritable(t *testing.T) {
	reg, _, private := fakeTrusted(t)
	source, env := makeTrustedTarFixture(t)
	env.ArchiveDigest = HashBytes([]byte("wrong archive"))
	_, err := reg.PublishTrustedArchive(context.Background(), env, source, "offline-publisher", private)
	require.ErrorIs(t, err, ErrIntegrity)
	hash, err := HashFile(source)
	require.NoError(t, err)
	env.ArchiveDigest = hash
	require.NoError(t, os.Chmod(source, 0666))
	_, err = reg.PublishTrustedArchive(context.Background(), env, source, "offline-publisher", private)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestAWSImmutablePolicyContainsNoGrantAndRejectsUnsafeBucket(t *testing.T) {
	raw, err := AWSImmutablePapersPolicyJSON("papers-private-prod")
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"Effect": "Allow"`)
	for _, term := range []string{"DenyNonConditionalWrites", "s3:if-none-match", "DenyAllObjectAndVersionDeletes", "s3:DeleteObjectVersion", "DenyInsecureTransport", "aws:SecureTransport"} {
		require.Contains(t, string(raw), term)
	}
	for _, bucket := range []string{"public", "atoman-public", "../../../etc", "", "a", "papers/evil"} {
		_, err = AWSImmutablePapersPolicyJSON(bucket)
		require.ErrorIs(t, err, ErrInvalid)
	}
}

func makeLargeTrustedTarFixture(t *testing.T) (string, Environment) {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "large-rootfs.tar")
	f, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	require.NoError(t, err)
	tw := tar.NewWriter(f)
	const size = 5 << 20
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "usr/share/texlive/large-fixture.bin", Size: size, Typeflag: tar.TypeReg, Mode: 0644,
	}))
	_, err = io.CopyN(tw, bytes.NewReader(bytes.Repeat([]byte("A"), size)), size)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, f.Close())
	require.NoError(t, os.Chmod(filename, 0400))
	digest, err := HashFile(filename)
	require.NoError(t, err)
	env := testEnv()
	env.ArchiveDigest = digest
	return filename, env
}
