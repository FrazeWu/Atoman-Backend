package papersassets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// TrustedArchiveCache stores only verified, signed, content-addressed
// archives. Caller must configure a private root-owned cache directory,
// separately from any writable project/input directory.
type TrustedArchiveCache struct {
	Registry  *TrustedArchiveRegistry
	CacheRoot string
}

func NewTrustedArchiveCache(registry *TrustedArchiveRegistry, cacheRoot string) (*TrustedArchiveCache, error) {
	if registry == nil || !filepath.IsAbs(cacheRoot) || cacheRoot == "/" || cacheRoot == "/tmp" {
		return nil, ErrInvalid
	}
	stat, err := os.Lstat(cacheRoot)
	if err != nil {
		return nil, err
	}
	if !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 || stat.Mode().Perm()&0077 != 0 {
		return nil, ErrInvalid
	}
	owner, ok := stat.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) {
		return nil, ErrInvalid
	}
	return &TrustedArchiveCache{Registry: registry, CacheRoot: cacheRoot}, nil
}
func (c *TrustedArchiveCache) cachePath(digest string) (string, error) {
	if c == nil || !digestRE.MatchString(digest) {
		return "", ErrInvalid
	}
	return filepath.Join(c.CacheRoot, strings.TrimPrefix(digest, "sha256:")+".tar"), nil
}
func verifyLocalCachedFile(ctx context.Context, file string, size int64, digest string) error {
	if ctx == nil || !digestRE.MatchString(digest) || size < 1 || size > MaxTrustedArchiveBytes {
		return ErrInvalid
	}
	st, err := os.Lstat(file)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0222 != 0 || st.Size() != size {
		return ErrIntegrity
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !os.SameFile(st, info) {
		return ErrIntegrity
	}
	hasher := sha256.New()
	n, err := io.Copy(hasher, io.LimitReader(f, size+1))
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if n != size || "sha256:"+hex.EncodeToString(hasher.Sum(nil)) != digest {
		return ErrIntegrity
	}
	return nil
}

// FetchArchive verifies the signed release BEFORE a single archive byte
// is committed to disk. A truncated, overlong, or corrupted download is
// rejected, and the partially staged temporary file is removed. Existing
// trusted cache objects are verified and never overwritten.
func (c *TrustedArchiveCache) FetchArchive(ctx context.Context, envDigest string) (string, error) {
	if c == nil || c.Registry == nil || ctx == nil {
		return "", ErrInvalid
	}
	release, err := c.Registry.LoadRelease(ctx, envDigest)
	if err != nil {
		return "", err
	}
	digest := release.Claim.Environment.ArchiveDigest
	dest, err := c.cachePath(digest)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(dest); err == nil {
		if err := verifyLocalCachedFile(ctx, dest, release.Claim.ArchiveBytes, digest); err != nil {
			return "", err
		}
		return dest, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	key, err := trustedArchiveKey(digest)
	if err != nil {
		return "", err
	}
	content, length, err := c.Registry.storage.OpenArchive(ctx, c.Registry.manifest.bucket, key)
	if err != nil {
		return "", err
	}
	defer content.Close()
	size := release.Claim.ArchiveBytes
	if length != 0 && length != size {
		return "", ErrIntegrity
	}
	tmp, err := os.CreateTemp(c.CacheRoot, ".trusted-pending-*")
	if err != nil {
		return "", err
	}
	tempPath := tmp.Name()
	defer os.Remove(tempPath)
	hasher := sha256.New()
	copied, copyErr := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(content, size+1))
	if copyErr != nil {
		tmp.Close()
		return "", copyErr
	}
	if err := ctx.Err(); err != nil {
		tmp.Close()
		return "", err
	}
	if copied != size || "sha256:"+hex.EncodeToString(hasher.Sum(nil)) != digest {
		tmp.Close()
		return "", ErrIntegrity
	}
	if err := tmp.Chmod(0400); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Link(tempPath, dest); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
		if err := verifyLocalCachedFile(ctx, dest, size, digest); err != nil {
			return "", err
		}
		return dest, nil
	}
	dir, err := os.Open(c.CacheRoot)
	if err != nil {
		return "", err
	}
	if err := dir.Sync(); err != nil {
		dir.Close()
		return "", err
	}
	if err := dir.Close(); err != nil {
		return "", err
	}
	if err := verifyLocalCachedFile(ctx, dest, size, digest); err != nil {
		return "", fmt.Errorf("trusted archive cache post-commit verification: %w", err)
	}
	return dest, nil
}

// TrustedCompileAssetReader requires the published Ed25519 release to be
// valid before using a preinstalled local archive in the root Launcher.
// It does NOT permit an untrusted user to choose an archive path.
type TrustedCompileAssetReader struct {
	Sources  *PrivateS3Registry
	Releases *TrustedArchiveRegistry
}

func (s *TrustedCompileAssetReader) LoadCompileSnapshot(ctx context.Context, digest string) (Snapshot, error) {
	if s == nil || s.Sources == nil {
		return Snapshot{}, ErrInvalid
	}
	return s.Sources.LoadSnapshot(ctx, digest)
}
func (s *TrustedCompileAssetReader) VerifyCompileArchive(ctx context.Context, digest, localPath string) (Environment, error) {
	if s == nil || s.Releases == nil || ctx == nil || !filepath.IsAbs(localPath) {
		return Environment{}, ErrInvalid
	}
	rel, err := s.Releases.LoadRelease(ctx, digest)
	if err != nil {
		return Environment{}, err
	}
	if err := verifyLocalCachedFile(ctx, localPath, rel.Claim.ArchiveBytes, rel.Claim.Environment.ArchiveDigest); err != nil {
		return Environment{}, err
	}
	return rel.Claim.Environment, nil
}

var _ CompileAssetReader = (*TrustedCompileAssetReader)(nil)
