package mail

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMailCredentialVaultAuthenticatesOwnerAndAccount(t *testing.T) {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	vault, err := NewMailCredentialVault(base64.StdEncoding.EncodeToString(key))
	require.NoError(t, err)
	userID, otherID := uuid.New(), uuid.New()
	secret := "example-secret-jmap-user-key-only"
	ciphertext, nonce, err := vault.Seal(userID, "mail-provider-1", secret)
	require.NoError(t, err)
	require.NotContains(t, string(ciphertext), secret)
	require.Len(t, nonce, 12)
	plain, err := vault.Open(userID, "mail-provider-1", ciphertext, nonce)
	require.NoError(t, err)
	require.Equal(t, secret, plain)
	_, err = vault.Open(otherID, "mail-provider-1", ciphertext, nonce)
	require.Error(t, err)
	_, err = vault.Open(userID, "mail-provider-2", ciphertext, nonce)
	require.Error(t, err)
	corrupted := append([]byte(nil), ciphertext...)
	corrupted[len(corrupted)-1] ^= 0x01
	_, err = vault.Open(userID, "mail-provider-1", corrupted, nonce)
	require.Error(t, err)
	_, err = vault.Open(userID, "mail-provider-1", ciphertext, nonce[:len(nonce)-1])
	require.Error(t, err)
}

func TestMailCredentialVaultFailsClosed(t *testing.T) {
	for _, input := range []string{"", "not-base64", base64.StdEncoding.EncodeToString(make([]byte, 16))} {
		_, err := NewMailCredentialVault(input)
		require.Error(t, err)
	}
	secret := base64.StdEncoding.EncodeToString(make([]byte, 32))
	vault, err := NewMailCredentialVault(secret)
	require.NoError(t, err)
	_, _, err = vault.Seal(uuid.Nil, "account", "x")
	require.Error(t, err)
	_, _, err = vault.Seal(uuid.New(), "", "x")
	require.Error(t, err)
	_, _, err = vault.Seal(uuid.New(), "account", "")
	require.Error(t, err)
	_, _, err = vault.Seal(uuid.New(), "account", strings.Repeat("s", 5000))
	require.Error(t, err)
}
