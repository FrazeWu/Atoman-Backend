package papersassets

import (
	"context"
	"os"
	"path/filepath"
)

// CompileAssetReader is the root Launcher boundary to immutable, previously
// authorized project sources and the approved offline TeX environment.
// It cannot publish, delete, enumerate arbitrary keys, or issue public URLs.
type CompileAssetReader interface {
	LoadCompileSnapshot(context.Context, string) (Snapshot, error)
	VerifyCompileArchive(context.Context, string, string) (Environment, error)
}

// Local content-addressed manifests remain supported in disposable P0 tests.
func (s *Store) LoadCompileSnapshot(ctx context.Context, digest string) (Snapshot, error) {
	if ctx == nil {
		return Snapshot{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	return s.LoadSnapshot(digest)
}
func (s *Store) VerifyCompileArchive(ctx context.Context, digest, archivePath string) (Environment, error) {
	if ctx == nil {
		return Environment{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Environment{}, err
	}
	return s.VerifyArchive(digest, archivePath)
}

// The private S3 registry validates manifest bytes before allowing any local
// archive extraction. Actual binary TeX bundle distribution is a separate
// trusted, signed environment-build pipeline not part of this module.
func (r *PrivateS3Registry) LoadCompileSnapshot(ctx context.Context, digest string) (Snapshot, error) {
	return r.LoadSnapshot(ctx, digest)
}
func (r *PrivateS3Registry) VerifyCompileArchive(ctx context.Context, digest, archivePath string) (Environment, error) {
	env, err := r.LoadEnvironment(ctx, digest)
	if err != nil {
		return Environment{}, err
	}
	if !filepath.IsAbs(archivePath) {
		return Environment{}, ErrInvalid
	}
	st, err := os.Lstat(archivePath)
	if err != nil {
		return Environment{}, err
	}
	if !st.Mode().IsRegular() {
		return Environment{}, ErrInvalid
	}
	actual, err := HashFile(archivePath)
	if err != nil {
		return Environment{}, err
	}
	if err := ctx.Err(); err != nil {
		return Environment{}, err
	}
	if actual != env.ArchiveDigest {
		return Environment{}, ErrIntegrity
	}
	return env, nil
}

var (
	_ CompileAssetReader = (*Store)(nil)
	_ CompileAssetReader = (*PrivateS3Registry)(nil)
)
