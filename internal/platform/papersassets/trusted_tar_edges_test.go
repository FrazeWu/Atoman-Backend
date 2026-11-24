package papersassets

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func tarSequence(t *testing.T, headers ...tar.Header) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "tar-edges-*.tar")
	require.NoError(t, err)
	tw := tar.NewWriter(f)
	for _, header := range headers {
		require.NoError(t, tw.WriteHeader(&header))
		if header.Size > 0 {
			_, err = tw.Write(bytes.Repeat([]byte("x"), int(header.Size)))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	_, err = f.Seek(0, 0)
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })
	return f
}
func TestTrustedTarRejectsLinkAncestorRegardlessOfEntryOrder(t *testing.T) {
	child := tar.Header{Name: "alias/secret.txt", Size: 3, Typeflag: tar.TypeReg, Mode: 0644}
	link := tar.Header{Name: "alias", Linkname: ".", Typeflag: tar.TypeSymlink, Mode: 0777}
	cases := []struct {
		name          string
		first, second tar.Header
	}{
		{"symlink-before-child", link, child},
		{"symlink-after-child", child, link},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := tarSequence(t, c.first, c.second)
			require.ErrorIs(t, ValidateTrustedTar(context.Background(), f), ErrInvalid)
		})
	}
}
func TestTrustedTarRejectsForwardOrSymlinkHardlinks(t *testing.T) {
	regular := tar.Header{Name: "bin/target", Typeflag: tar.TypeReg, Mode: 0644, Size: 4}
	alias := tar.Header{Name: "bin/new", Typeflag: tar.TypeLink, Mode: 0644, Linkname: "bin/target"}
	symlink := tar.Header{Name: "bin/target", Typeflag: tar.TypeSymlink, Mode: 0777, Linkname: "other"}
	f := tarSequence(t, alias, regular)
	require.ErrorIs(t, ValidateTrustedTar(context.Background(), f), ErrInvalid)
	f = tarSequence(t, symlink, alias)
	require.ErrorIs(t, ValidateTrustedTar(context.Background(), f), ErrInvalid)
	f = tarSequence(t, regular, alias)
	require.NoError(t, ValidateTrustedTar(context.Background(), f))
}
func TestTrustedTarRejectsDuplicatePathsAndUnsafePermissions(t *testing.T) {
	duplicate := tar.Header{Name: "etc/setup", Typeflag: tar.TypeReg, Mode: 0644, Size: 1}
	f := tarSequence(t, duplicate, duplicate)
	require.ErrorIs(t, ValidateTrustedTar(context.Background(), f), ErrInvalid)
	f = tarSequence(t, tar.Header{Name: "bin/unsafe", Typeflag: tar.TypeReg, Mode: 04755, Size: 1})
	require.ErrorIs(t, ValidateTrustedTar(context.Background(), f), ErrInvalid)
}
func TestTrustedTarAllowsOrdinaryDirectorySuffixAndRootMarker(t *testing.T) {
	f := tarSequence(t,
		tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0755},
		tar.Header{Name: "usr/", Typeflag: tar.TypeDir, Mode: 0755},
		tar.Header{Name: "usr/texlive.tex", Typeflag: tar.TypeReg, Mode: 0644, Size: 3})
	require.NoError(t, ValidateTrustedTar(context.Background(), f))
}
