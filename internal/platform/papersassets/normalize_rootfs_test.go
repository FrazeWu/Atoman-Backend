package papersassets

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func makeExportTar(t *testing.T, entries ...tar.Header) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "docker-export-*.tar")
	require.NoError(t, err)
	writer := tar.NewWriter(f)
	for _, entry := range entries {
		require.NoError(t, writer.WriteHeader(&entry))
		if entry.Size > 0 {
			_, err = io.CopyN(writer, strings.NewReader(strings.Repeat("A", int(entry.Size))), entry.Size)
			require.NoError(t, err)
		}
	}
	require.NoError(t, writer.Close())
	require.NoError(t, f.Close())
	require.NoError(t, os.Chmod(f.Name(), 0400))
	return f.Name()
}
func TestNormalizeDockerRootfsConvertsAbsoluteSymlinks(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned Docker export only")
	}
	source := makeExportTar(t,
		tar.Header{Name: "etc/", Typeflag: tar.TypeDir, Mode: 0755},
		tar.Header{Name: "etc/alternatives/", Typeflag: tar.TypeDir, Mode: 0755},
		tar.Header{Name: "etc/alternatives/awk", Typeflag: tar.TypeSymlink, Mode: 0777, Linkname: "/usr/bin/mawk"},
		tar.Header{Name: "usr/", Typeflag: tar.TypeDir, Mode: 0755},
		tar.Header{Name: "usr/bin/", Typeflag: tar.TypeDir, Mode: 0755},
		tar.Header{Name: "usr/bin/mawk", Typeflag: tar.TypeReg, Mode: 0755, Size: 16},
	)
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	destination := filepath.Join(root, "canonical.tar")
	result, err := NormalizeTrustedDockerExport(context.Background(), source, destination)
	require.NoError(t, err)
	require.Equal(t, 1, result.AbsoluteLinksRewritten)
	f, err := os.Open(destination)
	require.NoError(t, err)
	defer f.Close()
	reader := tar.NewReader(f)
	found := false
	for {
		hdr, err := reader.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if hdr.Name == "etc/alternatives/awk" {
			found = true
			require.Equal(t, "../../usr/bin/mawk", hdr.Linkname)
		}
	}
	require.True(t, found)
	require.NoError(t, os.Chmod(destination, 0600))
	_, err = NormalizeTrustedDockerExport(context.Background(), source, destination)
	require.Error(t, err, "never overwrite a published normalized rootfs")
}
func TestNormalizeDockerRootfsRejectsUnsafeSource(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned source only")
	}
	input := makeExportTar(t,
		tar.Header{Name: "bin/", Typeflag: tar.TypeDir, Mode: 0755},
		tar.Header{Name: "../outside", Typeflag: tar.TypeReg, Mode: 0644, Size: 3},
	)
	dest := filepath.Join(t.TempDir(), "reject.tar")
	_, err := NormalizeTrustedDockerExport(context.Background(), input, dest)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = os.Lstat(dest)
	require.ErrorIs(t, err, os.ErrNotExist)
	// A source symlink is not an authorized operator Docker export.
	link := filepath.Join(t.TempDir(), "linked.tar")
	require.NoError(t, os.Symlink(input, link))
	_, err = NormalizeTrustedDockerExport(context.Background(), link, dest)
	require.ErrorIs(t, err, ErrInvalid)
}
func TestNormalizeDockerRootfsStripsPrivilegedBits(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("operator-approved Docker export normalization requires root")
	}
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	src := makeExportTar(t,
		tar.Header{Name: "bin/", Typeflag: tar.TypeDir, Mode: 0755},
		tar.Header{Name: "bin/unsafe-helper", Typeflag: tar.TypeReg, Mode: 04755, Size: 16},
	)
	dest := filepath.Join(root, "safe.tar")
	stats, err := NormalizeTrustedDockerExport(context.Background(), src, dest)
	require.NoError(t, err)
	require.Equal(t, 1, stats.PrivilegeBitsStripped)
	f, err := os.Open(dest)
	require.NoError(t, err)
	defer f.Close()
	tarReader := tar.NewReader(f)
	_, err = tarReader.Next()
	require.NoError(t, err)
	helper, err := tarReader.Next()
	require.NoError(t, err)
	require.Equal(t, int64(0755), helper.Mode)
}
