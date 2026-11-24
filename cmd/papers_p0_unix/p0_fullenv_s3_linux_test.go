//go:build linux

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func setP0TestReader(t *testing.T) (string, func()) {
	t.Helper()
	root := t.TempDir()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "trusted-publisher.pub"),
		[]byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0400))
	require.NoError(t, os.Mkdir(filepath.Join(root, "trusted-cache"), 0700))
	t.Setenv("PAPERS_P0_FULLENV_CONFIRM", "1")
	t.Setenv("PAPERS_P0_FULLENV_ENDPOINT", "http://127.0.0.1:32118")
	t.Setenv("PAPERS_P0_FULLENV_BUCKET", "papers-private-p0-valid")
	t.Setenv("PAPERS_P0_FULLENV_READER_USER", "papersp0readtest")
	t.Setenv("PAPERS_P0_FULLENV_READER_SECRET", "P0Only_1234567890123456789012345678")
	for _, key := range []string{
		"PAPERS_P0_IAM_ADMIN_SECRET", "PAPERS_P0_FULLENV_PUBLISHER_SECRET",
		"PAPERS_P0_IAM_PUBLISHER_SECRET", "PAPERS_P0_S3_TEST_SECRET",
	} {
		t.Setenv(key, "")
	}
	return root, func() {}
}
func TestP0LauncherS3ReaderCanBeConstructedWithoutPublisherCredentials(t *testing.T) {
	root, _ := setP0TestReader(t)
	reader, cache, err := fullenvAssets(root)
	require.NoError(t, err)
	require.NotNil(t, reader)
	require.NotNil(t, cache)
}
func TestP0LauncherS3RefusesPublisherOrAdminSecrets(t *testing.T) {
	root, _ := setP0TestReader(t)
	for _, key := range []string{
		"PAPERS_P0_IAM_ADMIN_SECRET", "PAPERS_P0_FULLENV_PUBLISHER_SECRET",
		"PAPERS_P0_IAM_PUBLISHER_SECRET", "PAPERS_P0_S3_TEST_SECRET",
	} {
		t.Setenv(key, "P0Only_NotForLauncher")
		_, _, err := fullenvAssets(root)
		require.ErrorContains(t, err, "forbidden")
		t.Setenv(key, "")
	}
}
func TestP0LauncherS3RefusesNonDisposableEndpointsAndBuckets(t *testing.T) {
	root, _ := setP0TestReader(t)
	for _, url := range []string{
		"https://s3.amazonaws.com", "http://127.0.0.1:9100",
		"http://192.168.1.50:9000", "http://127.0.0.1:9000",
		"http://localhost:32118",
	} {
		t.Setenv("PAPERS_P0_FULLENV_ENDPOINT", url)
		_, _, err := fullenvAssets(root)
		require.Error(t, err)
	}
	t.Setenv("PAPERS_P0_FULLENV_ENDPOINT", "http://127.0.0.1:32118")
	t.Setenv("PAPERS_P0_FULLENV_BUCKET", "production-media")
	_, _, err := fullenvAssets(root)
	require.Error(t, err)
}
func TestP0LauncherS3RejectsSymlinkedPublisherKey(t *testing.T) {
	root, _ := setP0TestReader(t)
	pubPath := filepath.Join(root, "trusted-publisher.pub")
	outside := filepath.Join(t.TempDir(), "pub")
	raw, err := os.ReadFile(pubPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(outside, raw, 0400))
	require.NoError(t, os.Remove(pubPath))
	require.NoError(t, os.Symlink(outside, pubPath))
	_, _, err = fullenvAssets(root)
	require.Error(t, err)
}
