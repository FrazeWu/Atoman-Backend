package papersassets

import (
	"archive/tar"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
)

const trustedArchiveDomain = "atoman-papers/trusted-tex-archive/v1\x00"
const minTrustedArchiveBytes int64 = 1024
const maxSignedReleaseBytes int64 = 8192
const trustedArchiveKeyPrefix = privateRegistryPrefix + "trusted-archives/"
const trustedReleaseKeyPrefix = privateRegistryPrefix + "trusted-releases/"

var publisherKeyIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{2,31}$`)
var ErrUntrustedPublisher = errors.New("papers assets: unsigned or untrusted archive release")

// ArchiveClaim signs the EXACT pinned TeX environment, archive digest,
// byte length, and allowed publisher identity. Its JSON field order is fixed.
type ArchiveClaim struct {
	Version           int         `json:"version"`
	EnvironmentDigest string      `json:"environment_digest"`
	Environment       Environment `json:"environment"`
	ArchiveBytes      int64       `json:"archive_bytes"`
	PublisherKeyID    string      `json:"publisher_key_id"`
}
type SignedArchiveRelease struct {
	Claim     ArchiveClaim `json:"claim"`
	Signature string       `json:"signature"`
}

// TrustedArchiveRegistry handles only offline approved TeX rootfs archives.
// The publisher's private signing key MUST be held outside the root Launcher.
// Reader keys are pinned by the operator, never read from the S3 manifest.
type TrustedArchiveRegistry struct {
	manifest     *PrivateS3Registry
	storage      TrustedArchiveTransport
	trusted      map[string]ed25519.PublicKey
	minLockDays  int64
	publishReady atomic.Bool
}

func NewTrustedArchiveRegistry(manifest *PrivateS3Registry, trustRoots map[string]ed25519.PublicKey, minRetentionDays int64) (*TrustedArchiveRegistry, error) {
	if manifest == nil || manifest.client == nil || minRetentionDays < 1 || minRetentionDays > 36500 || len(trustRoots) == 0 || len(trustRoots) > 8 {
		return nil, ErrInvalid
	}
	transport, ok := manifest.client.(TrustedArchiveTransport)
	if !ok {
		return nil, ErrInvalid
	}
	roots := make(map[string]ed25519.PublicKey, len(trustRoots))
	for id, pub := range trustRoots {
		if !publisherKeyIDRE.MatchString(id) || len(pub) != ed25519.PublicKeySize {
			return nil, ErrInvalid
		}
		roots[id] = append(ed25519.PublicKey(nil), pub...)
	}
	return &TrustedArchiveRegistry{manifest: manifest, storage: transport, trusted: roots, minLockDays: minRetentionDays}, nil
}

// ProbePublisher requires a proven private/versioned bucket, valid atomic
// If-None-Match behavior, and minimum COMPLIANCE-mode default Object Lock.
// This is a *necessary* but not sufficient production policy check.
func (r *TrustedArchiveRegistry) ProbePublisher(ctx context.Context) error {
	if r == nil || ctx == nil {
		return ErrInvalid
	}
	r.publishReady.Store(false)
	if err := r.manifest.Probe(ctx); err != nil {
		return err
	}
	locked, err := r.storage.LockCompliant(ctx, r.manifest.bucket, r.minLockDays)
	if err != nil || !locked {
		return fmt.Errorf("%w: COMPLIANCE Object Lock not verified: %v", ErrConditionalUnsupported, err)
	}
	r.publishReady.Store(true)
	return nil
}
func (c ArchiveClaim) validate() error {
	if c.Version != 1 || !digestRE.MatchString(c.EnvironmentDigest) ||
		c.Environment.Validate() != nil || !publisherKeyIDRE.MatchString(c.PublisherKeyID) ||
		c.ArchiveBytes < minTrustedArchiveBytes || c.ArchiveBytes > MaxTrustedArchiveBytes {
		return ErrInvalid
	}
	envRaw, err := json.Marshal(c.Environment)
	if err != nil {
		return err
	}
	if HashBytes(envRaw) != c.EnvironmentDigest {
		return ErrIntegrity
	}
	return nil
}
func (c ArchiveClaim) signingBytes() ([]byte, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	return append([]byte(trustedArchiveDomain), body...), nil
}
func SignArchiveRelease(claim ArchiveClaim, pubID string, private ed25519.PrivateKey) (SignedArchiveRelease, error) {
	if len(private) != ed25519.PrivateKeySize || pubID != claim.PublisherKeyID {
		return SignedArchiveRelease{}, ErrInvalid
	}
	message, err := claim.signingBytes()
	if err != nil {
		return SignedArchiveRelease{}, err
	}
	return SignedArchiveRelease{Claim: claim, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(private, message))}, nil
}
func (r *TrustedArchiveRegistry) VerifyRelease(release SignedArchiveRelease, expectedDigest string) error {
	if r == nil || !digestRE.MatchString(expectedDigest) {
		return ErrInvalid
	}
	if release.Claim.EnvironmentDigest != expectedDigest {
		return ErrIntegrity
	}
	message, err := release.Claim.signingBytes()
	if err != nil {
		return err
	}
	pinned, ok := r.trusted[release.Claim.PublisherKeyID]
	if !ok {
		return ErrUntrustedPublisher
	}
	sig, err := base64.StdEncoding.DecodeString(release.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(pinned, message, sig) {
		return ErrUntrustedPublisher
	}
	return nil
}

func trustedArchiveKey(digest string) (string, error) {
	if !digestRE.MatchString(digest) {
		return "", ErrInvalid
	}
	return trustedArchiveKeyPrefix + strings.TrimPrefix(digest, "sha256:") + ".tar", nil
}
func trustedReleaseKey(digest string) (string, error) {
	if !digestRE.MatchString(digest) {
		return "", ErrInvalid
	}
	return trustedReleaseKeyPrefix + strings.TrimPrefix(digest, "sha256:") + ".json", nil
}

// ValidateTrustedTar rejects archive traversal, host device nodes,
// and symlink/hardlink targets that escape the rootfs before publication.
// An object passing this validation still needs an approved TeX image build
// provenance; it is NOT an arbitrary user-upload extraction API.
func ValidateTrustedTar(ctx context.Context, f *os.File) error {
	if ctx == nil || f == nil {
		return ErrInvalid
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	reader := tar.NewReader(f)
	var expanded int64
	var entries int
	// Never permit a later archive entry to traverse a symlink created by
	// an earlier entry (or vice versa). GNU tar extraction as root otherwise
	// can write outside the unpacking directory despite safe-looking names.
	seen := make(map[string]byte)
	symlinks := make(map[string]struct{})
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: malformed rootfs TAR: %v", ErrInvalid, err)
		}
		entries++
		if entries > 500000 {
			return ErrInvalid
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if hdr.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
			if name == "" || name == "." {
				continue
			}
		}
		if len(name) > 4096 || name == "" || name == "." || path.IsAbs(name) ||
			path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") ||
			strings.ContainsRune(name, 0) || strings.ContainsRune(name, '\\') {
			return ErrInvalid
		}
		if _, ok := seen[name]; ok {
			return ErrInvalid
		}
		if hdr.Size < 0 || hdr.Size > MaxTrustedArchiveBytes {
			return ErrInvalid
		}
		if expanded > MaxTrustedArchiveBytes-hdr.Size {
			return ErrInvalid
		}
		expanded += hdr.Size
		for ancestor := path.Dir(name); ancestor != "."; ancestor = path.Dir(ancestor) {
			if _, ok := symlinks[ancestor]; ok {
				return ErrInvalid
			}
			if ancestor == "/" {
				return ErrInvalid
			}
		}
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeRegA, tar.TypeDir:
			// Other software can still do undesirable things with world-writable
			// paths or suid bits. These are not needed for the approved P0 profile.
			if hdr.Mode&06000 != 0 {
				return ErrInvalid
			}
		case tar.TypeSymlink, tar.TypeLink:
			link := hdr.Linkname
			if len(link) > 4096 || link == "" || path.IsAbs(link) ||
				strings.ContainsRune(link, '\\') || strings.ContainsRune(link, 0) {
				return ErrInvalid
			}
			var resolved string
			if hdr.Typeflag == tar.TypeSymlink {
				resolved = path.Clean(path.Join(path.Dir(name), link))
			} else {
				resolved = path.Clean(link)
				targetType, exists := seen[resolved]
				if !exists || (targetType != tar.TypeReg && targetType != tar.TypeRegA) {
					// Hardlink must target an already-published regular file, never a
					// directory, a forward reference or an existing symbolic link.
					return ErrInvalid
				}
			}
			if resolved == ".." || strings.HasPrefix(resolved, "../") || path.IsAbs(resolved) {
				return ErrInvalid
			}
			if hdr.Typeflag == tar.TypeSymlink {
				symlinks[name] = struct{}{}
			}
		default:
			return ErrInvalid
		}
		seen[name] = hdr.Typeflag
	}
	// O(entries * path-depth), independent of archive entry ordering:
	// a symlink cannot act as the parent of ANY other archived path.
	for name := range seen {
		for ancestor := path.Dir(name); ancestor != "."; ancestor = path.Dir(ancestor) {
			if _, ok := symlinks[ancestor]; ok {
				return ErrInvalid
			}
		}
	}
	return nil
}
func verifyTrustedSource(ctx context.Context, sourcePath, digest string) (*os.File, int64, error) {
	if ctx == nil || !filepath.IsAbs(sourcePath) || !digestRE.MatchString(digest) {
		return nil, 0, ErrInvalid
	}
	before, err := os.Lstat(sourcePath)
	if err != nil {
		return nil, 0, err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0022 != 0 ||
		before.Size() < minTrustedArchiveBytes || before.Size() > MaxTrustedArchiveBytes {
		return nil, 0, ErrInvalid
	}
	f, err := os.Open(sourcePath)
	if err != nil {
		return nil, 0, err
	}
	stat, err := f.Stat()
	if err != nil || !os.SameFile(before, stat) {
		f.Close()
		return nil, 0, ErrInvalid
	}
	if err := ValidateTrustedTar(ctx, f); err != nil {
		f.Close()
		return nil, 0, err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, 0, err
	}
	hash := sha256.New()
	n, err := io.Copy(hash, f)
	if err != nil || n != stat.Size() {
		f.Close()
		return nil, 0, ErrIntegrity
	}
	if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != digest {
		f.Close()
		return nil, 0, ErrIntegrity
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, stat.Size(), nil
}
func verifyArchiveStream(ctx context.Context, rc io.ReadCloser, claimedLength, size int64, digest string) error {
	if rc == nil || ctx == nil || size < 1 || size > MaxTrustedArchiveBytes {
		return ErrInvalid
	}
	defer rc.Close()
	if claimedLength != 0 && claimedLength != size {
		return ErrIntegrity
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(rc, size+1))
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if n != size || "sha256:"+hex.EncodeToString(h.Sum(nil)) != digest {
		return ErrIntegrity
	}
	return nil
}
func (r *TrustedArchiveRegistry) verifyRemoteArchive(ctx context.Context, digest string, size int64) error {
	key, err := trustedArchiveKey(digest)
	if err != nil {
		return err
	}
	body, length, err := r.storage.OpenArchive(ctx, r.manifest.bucket, key)
	if err != nil {
		return err
	}
	return verifyArchiveStream(ctx, body, length, size, digest)
}

// PublishTrustedArchive is an OPERATOR-ONLY offline release operation.
// A trusted publisher signs the verified source; root Launcher only receives
// a pinned public key. No user-controlled path is ever passed here.
func (r *TrustedArchiveRegistry) PublishTrustedArchive(ctx context.Context, environment Environment, sourcePath, keyID string, private ed25519.PrivateKey) (string, error) {
	if r == nil || ctx == nil || !r.publishReady.Load() {
		return "", ErrConditionalUnsupported
	}
	if err := environment.Validate(); err != nil {
		return "", err
	}
	trusted, ok := r.trusted[keyID]
	if !ok || len(private) != ed25519.PrivateKeySize ||
		!ed25519.PublicKey(private.Public().(ed25519.PublicKey)).Equal(trusted) {
		return "", ErrUntrustedPublisher
	}
	envRaw, err := json.Marshal(environment)
	if err != nil {
		return "", err
	}
	envDigest := HashBytes(envRaw)
	file, size, err := verifyTrustedSource(ctx, sourcePath, environment.ArchiveDigest)
	if err != nil {
		return "", err
	}
	defer file.Close()
	key, err := trustedArchiveKey(environment.ArchiveDigest)
	if err != nil {
		return "", err
	}
	putErr := r.storage.PutFileIfAbsent(ctx, r.manifest.bucket, key, file, size)
	if putErr != nil && !errors.Is(putErr, ErrAlreadyExists) && !retryableConditionalError(putErr) {
		return "", putErr
	}
	if err := r.verifyRemoteArchive(ctx, environment.ArchiveDigest, size); err != nil {
		return "", errors.Join(putErr, err)
	}
	claim := ArchiveClaim{Version: 1, EnvironmentDigest: envDigest, Environment: environment, ArchiveBytes: size, PublisherKeyID: keyID}
	release, err := SignArchiveRelease(claim, keyID, private)
	if err != nil {
		return "", err
	}
	bytes, err := json.Marshal(release)
	if err != nil {
		return "", err
	}
	manifestKey, err := trustedReleaseKey(envDigest)
	if err != nil {
		return "", err
	}
	signedPutErr := r.storage.PutIfAbsent(ctx, r.manifest.bucket, manifestKey, bytes, "application/json")
	if signedPutErr != nil && !errors.Is(signedPutErr, ErrAlreadyExists) && !retryableConditionalError(signedPutErr) {
		return "", signedPutErr
	}
	observed, err := r.LoadRelease(ctx, envDigest)
	if err != nil {
		return "", errors.Join(signedPutErr, err)
	}
	current, err := json.Marshal(observed)
	if err != nil || string(current) != string(bytes) {
		return "", ErrIntegrity
	}
	return envDigest, nil
}
func (r *TrustedArchiveRegistry) LoadRelease(ctx context.Context, envDigest string) (SignedArchiveRelease, error) {
	if r == nil || ctx == nil {
		return SignedArchiveRelease{}, ErrInvalid
	}
	key, err := trustedReleaseKey(envDigest)
	if err != nil {
		return SignedArchiveRelease{}, err
	}
	raw, err := r.storage.Read(ctx, r.manifest.bucket, key, maxSignedReleaseBytes)
	if err != nil {
		return SignedArchiveRelease{}, err
	}
	var result SignedArchiveRelease
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, ErrIntegrity
	}
	if err := r.VerifyRelease(result, envDigest); err != nil {
		return SignedArchiveRelease{}, err
	}
	return result, nil
}
