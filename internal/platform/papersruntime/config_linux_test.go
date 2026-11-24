//go:build linux

package papersruntime

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func fixtureAPI(t *testing.T) APIConfig {
	t.Helper()
	public, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	return APIConfig{
		Reader: StorageCredential{AccessKey: "papers-api-reader-fixture", SecretKey: strings.Repeat("x", 32)},
		Trust:  TrustConfig{EnvironmentDigest: "sha256:" + strings.Repeat("a", 64), SignerID: "test-signer", PublicKey: base64.StdEncoding.EncodeToString(public), RetentionDays: 1},
	}
}
func TestProductionCredentialsRejectForeignRolesUnknownFieldsAndPermissions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CREDENTIALS_DIRECTORY", dir)
	path := filepath.Join(dir, "papers-api.json")
	c := fixtureAPI(t)
	write := func(value any) {
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, raw, 0600))
		require.NoError(t, os.Chmod(path, 0600))
	}
	write(c)
	got, err := loadAPI()
	require.NoError(t, err)
	require.Equal(t, c, got)
	bad := c
	bad.Reader.AccessKey = "papers-snapshot-writer-fixture"
	write(bad)
	_, err = loadAPI()
	require.ErrorIs(t, err, ErrConfig)
	write(map[string]any{"reader": c.Reader, "trust": c.Trust, "database_password": strings.Repeat("x", 32)})
	_, err = loadAPI()
	require.ErrorIs(t, err, ErrConfig)
	write(c)
	require.NoError(t, os.Chmod(path, 0644))
	_, err = loadAPI()
	require.ErrorIs(t, err, ErrConfig)
	require.NoError(t, os.Remove(path))
	target := filepath.Join(dir, "other")
	require.NoError(t, os.WriteFile(target, []byte("{}"), 0600))
	require.NoError(t, os.Symlink(target, path))
	_, err = loadAPI()
	require.ErrorIs(t, err, ErrConfig)
}
func TestProductionRuntimeDefaultOffAndUnknownRoleDenied(t *testing.T) {
	t.Setenv("PAPERS_COMPILE_ENABLE", "")
	t.Setenv("PAPERS_SERVICE_ENABLE", "")
	svc, err := API(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, svc)
	require.ErrorIs(t, Run(context.Background(), "worker"), ErrConfig)
	t.Setenv("PAPERS_SERVICE_ENABLE", "1")
	require.ErrorIs(t, Run(context.Background(), "arbitrary-command"), ErrConfig)
	t.Setenv("PAPERS_COMPILE_ENABLE", "1")
	svc, err = API(context.Background(), nil)
	require.ErrorIs(t, err, ErrConfig)
	require.NotNil(t, svc)
}
