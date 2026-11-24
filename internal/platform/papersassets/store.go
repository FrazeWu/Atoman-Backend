// Package papersassets provides a P0-only local content-addressed snapshot
// registry. Production will replace the local filesystem with authenticated,
// versioned private object storage without weakening verification.
package papersassets

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
)

var (
	ErrInvalid   = errors.New("papers assets: invalid manifest")
	ErrIntegrity = errors.New("papers assets: content digest does not match")
	digestRE     = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

const MaxSnapshotBytes = 4 << 20

type SourceFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Kind    string `json:"kind,omitempty"`
}
type Snapshot struct {
	Version   int          `json:"version"`
	ProjectID uuid.UUID    `json:"project_id"`
	Revision  int64        `json:"revision"`
	Files     []SourceFile `json:"files"`
}

func (s *Snapshot) Validate() error {
	if s.Version != 1 || s.ProjectID == uuid.Nil || s.Revision <= 0 || len(s.Files) == 0 || len(s.Files) > 64 {
		return ErrInvalid
	}
	seen := make(map[string]bool)
	dirs := make(map[string]bool)
	total := 0
	main := false
	for _, f := range s.Files {
		key := strings.ToLower(f.Path)
		if !ValidSourcePath(f.Path) || seen[key] || dirs[key] {
			return fmt.Errorf("%w: unsafe or duplicate file", ErrInvalid)
		}
		seen[key] = true
		parts := strings.Split(key, "/")
		for i := 1; i < len(parts); i++ {
			dir := strings.Join(parts[:i], "/")
			if seen[dir] {
				return ErrInvalid
			}
			dirs[dir] = true
		}
		if f.Path == "main.tex" {
			main = true
		}
		total += len(f.Content)
		if total > MaxSnapshotBytes {
			return ErrInvalid
		}
		if _, err := f.Bytes(); err != nil {
			return err
		}
	}

	if !main {
		return fmt.Errorf("%w: main.tex is required", ErrInvalid)
	}
	return nil
}

type Environment struct {
	Version       int    `json:"version"`
	Name          string `json:"name"`
	ImageDigest   string `json:"image_digest"`
	ArchiveDigest string `json:"archive_digest"`
	RunscRelease  string `json:"runsc_release"`
}

func (e Environment) Validate() error {
	if e.Version != 1 || e.Name != "texlive-2026-p0" ||
		e.ImageDigest != "sha256:5613d47067c98df80d6b3206c9a731ba6e5d3eb5828fea892afd270426de4bd4" ||
		!digestRE.MatchString(e.ArchiveDigest) || e.RunscRelease != "20261019.0" {
		return ErrInvalid
	}
	return nil
}

type Store struct{ root string }

func OpenStore(root string) (*Store, error) {
	if !filepath.IsAbs(root) {
		return nil, ErrInvalid
	}
	st, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("%w: store root must be private real directory", ErrInvalid)
	}
	for _, sub := range []string{"snapshots", "environments"} {
		dir := filepath.Join(root, sub)
		if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
		stat, err := os.Lstat(dir)
		if err != nil {
			return nil, err
		}
		if !stat.IsDir() || stat.Mode().Perm()&0077 != 0 || stat.Mode()&os.ModeSymlink != 0 {
			return nil, ErrInvalid
		}
	}
	return &Store{root: root}, nil
}

func HashBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func HashFile(file string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 {
		return "", ErrInvalid
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
func filePath(root, kind, digest string) (string, error) {
	if !digestRE.MatchString(digest) {
		return "", ErrInvalid
	}
	return filepath.Join(root, kind, strings.TrimPrefix(digest, "sha256:")+".json"), nil
}
func (s *Store) publish(kind string, raw []byte) (string, error) {
	if s == nil {
		return "", ErrInvalid
	}
	digest := HashBytes(raw)
	filename, err := filePath(s.root, kind, digest)
	if err != nil {
		return "", err
	}
	// Stage privately, fsync and link the complete immutable object into
	// the digest namespace. O_EXCL at the final pathname would expose
	// partially written bytes to concurrent snapshot consumers.
	directory := filepath.Dir(filename)
	f, err := os.CreateTemp(directory, ".publishing-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Chmod(0400); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if err = os.Link(f.Name(), filename); err != nil {
		if !os.IsExist(err) {
			return "", err
		}
		loaded, readErr := s.load(kind, digest)
		if readErr != nil {
			return "", readErr
		}
		if string(loaded) != string(raw) {
			return "", ErrIntegrity
		}
		return digest, nil
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return "", err
	}
	if err = directoryHandle.Sync(); err != nil {
		directoryHandle.Close()
		return "", err
	}
	if err = directoryHandle.Close(); err != nil {
		return "", err
	}
	return digest, nil
}
func (s *Store) load(kind, digest string) ([]byte, error) {
	if s == nil {
		return nil, ErrInvalid
	}
	filename, err := filePath(s.root, kind, digest)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxSnapshotBytes+65536 || info.Size() < 2 || info.Mode().Perm()&0222 != 0 {
		return nil, ErrInvalid
	}
	raw, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	if HashBytes(raw) != digest {
		return nil, ErrIntegrity
	}
	return raw, nil
}
func (s *Store) PublishSnapshot(snapshot Snapshot) (string, error) {
	// Do not mutate the caller's slice (which may be used concurrently).
	snapshot.Files = append([]SourceFile(nil), snapshot.Files...)
	sort.Slice(snapshot.Files, func(i, j int) bool { return snapshot.Files[i].Path < snapshot.Files[j].Path })
	if err := snapshot.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	return s.publish("snapshots", raw)
}
func (s *Store) LoadSnapshot(digest string) (Snapshot, error) {
	var snapshot Snapshot
	raw, err := s.load("snapshots", digest)
	if err != nil {
		return snapshot, err
	}
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		return Snapshot{}, err
	}
	if err = snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}
func (s *Store) PublishEnvironment(env Environment) (string, error) {
	if err := env.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	return s.publish("environments", raw)
}
func (s *Store) LoadEnvironment(digest string) (Environment, error) {
	var env Environment
	raw, err := s.load("environments", digest)
	if err != nil {
		return env, err
	}
	if err = json.Unmarshal(raw, &env); err != nil {
		return env, err
	}
	return env, env.Validate()
}

// VerifyArchive binds the environment manifest to the actual offline image
// archive before any privileged OCI bundle is created.
func (s *Store) VerifyArchive(digest, archivePath string) (Environment, error) {
	env, err := s.LoadEnvironment(digest)
	if err != nil {
		return env, err
	}
	if !filepath.IsAbs(archivePath) {
		return Environment{}, ErrInvalid
	}
	info, err := os.Lstat(archivePath)
	if err != nil {
		return Environment{}, err
	}
	if !info.Mode().IsRegular() {
		return Environment{}, ErrInvalid
	}
	actual, err := HashFile(archivePath)
	if err != nil {
		return Environment{}, err
	}
	if actual != env.ArchiveDigest {
		return Environment{}, ErrIntegrity
	}
	return env, nil
}
