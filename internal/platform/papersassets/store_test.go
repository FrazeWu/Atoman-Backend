package papersassets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	root := filepath.Join(t.TempDir(), "registry")
	require.NoError(t, os.Mkdir(root, 0700))
	s, err := OpenStore(root)
	require.NoError(t, err)
	return s
}
func fixtureSnapshot() Snapshot {
	return Snapshot{Version: 1, ProjectID: uuid.New(), Revision: 2, Files: []SourceFile{
		{Path: "sections/test.tex", Content: "Hello from tests"},
		{Path: "main.tex", Content: "\\documentclass{article}\\begin{document}Hello\\end{document}"},
	}}
}
func testEnv() Environment {
	return Environment{Version: 1, Name: "texlive-2026-p0",
		ImageDigest:   "sha256:5613d47067c98df80d6b3206c9a731ba6e5d3eb5828fea892afd270426de4bd4",
		ArchiveDigest: HashBytes([]byte("sandbox fixture rootfs archive")),
		RunscRelease:  "20261019.0"}
}
func TestSnapshotDigestRoundTripAndImmutability(t *testing.T) {
	s := newStore(t)
	snap := fixtureSnapshot()
	digest, err := s.PublishSnapshot(snap)
	require.NoError(t, err)
	loaded, err := s.LoadSnapshot(digest)
	require.NoError(t, err)
	require.Equal(t, snap.ProjectID, loaded.ProjectID)
	require.Equal(t, int64(2), loaded.Revision)
	require.Len(t, loaded.Files, 2)
	duplicate, err := s.PublishSnapshot(snap)
	require.NoError(t, err)
	require.Equal(t, digest, duplicate)
	file, err := filePath(s.root, "snapshots", digest)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(file, 0600))
	require.NoError(t, os.WriteFile(file, []byte("tampered"), 0600))
	require.NoError(t, os.Chmod(file, 0400))
	_, err = s.LoadSnapshot(digest)
	require.ErrorIs(t, err, ErrIntegrity)
}
func TestRejectSnapshotPathsAndOversize(t *testing.T) {
	s := newStore(t)
	for _, path := range []string{"../main.tex", "/etc/evil.tex", "./main.tex", "a//x.tex", "a/../main.tex", ".latexmkrc", "main.sh", "a\\evil.tex"} {
		snap := fixtureSnapshot()
		snap.Files[0].Path = path
		_, err := s.PublishSnapshot(snap)
		require.Error(t, err, path)
	}
	snap := fixtureSnapshot()
	snap.Files[1].Content = strings.Repeat("x", MaxSnapshotBytes+1)
	_, err := s.PublishSnapshot(snap)
	require.Error(t, err)
}
func TestEnvironmentArchiveVerification(t *testing.T) {
	s := newStore(t)
	archive := filepath.Join(t.TempDir(), "rootfs.tar")
	require.NoError(t, os.WriteFile(archive, []byte("sandbox fixture rootfs archive"), 0600))
	env := testEnv()
	digest, err := s.PublishEnvironment(env)
	require.NoError(t, err)
	_, err = s.VerifyArchive(digest, archive)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(archive, []byte("corrupted"), 0600))
	_, err = s.VerifyArchive(digest, archive)
	require.True(t, errors.Is(err, ErrIntegrity))
}
func TestRejectUnapprovedEnvironmentAndStoreSymlinks(t *testing.T) {
	s := newStore(t)
	env := testEnv()
	env.ImageDigest = HashBytes([]byte("unapproved image"))
	_, err := s.PublishEnvironment(env)
	require.ErrorIs(t, err, ErrInvalid)
	root := t.TempDir()
	require.NoError(t, os.Symlink(s.root, filepath.Join(root, "linked")))
	_, err = OpenStore(filepath.Join(root, "linked"))
	require.ErrorIs(t, err, ErrInvalid)
}

func TestConcurrentImmutablePublication(t *testing.T) {
	s := newStore(t)
	snap := fixtureSnapshot()
	const workers = 12
	ids := make(chan string, workers)
	failures := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			copyOfSnapshot := snap
			copyOfSnapshot.Files = append([]SourceFile(nil), snap.Files...)
			digest, err := s.PublishSnapshot(copyOfSnapshot)
			if err != nil {
				failures <- err
				return
			}
			ids <- digest
		}()
	}
	wg.Wait()
	close(ids)
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	unique := map[string]bool{}
	for id := range ids {
		unique[id] = true
		_, err := s.LoadSnapshot(id)
		require.NoError(t, err)
	}
	require.Len(t, unique, 1)
}

func TestRejectInvalidUTF8Source(t *testing.T) {
	s := newStore(t)
	snap := fixtureSnapshot()
	snap.Files[0].Content = string([]byte{0xff, 0xfe})
	_, err := s.PublishSnapshot(snap)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestAcademicEnvironmentExactPinAndLegacyCompatibility(t *testing.T) {
	approved := AcademicEnvironment()
	require.NoError(t, approved.Validate())
	require.NoError(t, testEnv().Validate())
	for _, change := range []func(*Environment){
		func(e *Environment) { e.Version = 1 },
		func(e *Environment) { e.Name = "texlive-2026-p0" },
		func(e *Environment) { e.ImageDigest = HashBytes([]byte("different image")) },
		func(e *Environment) { e.ArchiveDigest = HashBytes([]byte("different archive")) },
		func(e *Environment) { e.RunscRelease = "unknown" },
	} {
		value := approved
		change(&value)
		require.ErrorIs(t, value.Validate(), ErrInvalid)
	}
}
