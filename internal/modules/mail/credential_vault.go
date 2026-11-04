package mail

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"strings"

	"github.com/google/uuid"
)

// MailCredentialVault encrypts user-scoped Stalwart bearer credentials at
// rest. The encryption key is a distinct server-only secret; it must not be
// the Atoman web session or Cloudflare R2 secret, and is never logged.
type MailCredentialVault struct{ aead cipher.AEAD }

func NewMailCredentialVault(keyBase64 string) (*MailCredentialVault, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(keyBase64))
	if err != nil || len(key) != 32 {
		return nil, errors.New("Mail credential encryption key must be 32 bytes, base64 encoded")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("Mail credential encryption initialization failed")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("Mail credential encryption initialization failed")
	}
	return &MailCredentialVault{aead: aead}, nil
}

func mailCredentialAAD(userID uuid.UUID, accountID string) []byte {
	return []byte("atoman-mail-key/v1/" + userID.String() + "/" + accountID)
}

func (v *MailCredentialVault) Seal(userID uuid.UUID, accountID string, apiKey string) (ciphertext, nonce []byte, err error) {
	if v == nil || v.aead == nil || userID == uuid.Nil || accountID == "" || apiKey == "" || len(apiKey) > 4096 {
		return nil, nil, errors.New("Invalid mail credential encryption input")
	}
	nonce = make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, errors.New("Mail credential random nonce unavailable")
	}
	ciphertext = v.aead.Seal(nil, nonce, []byte(apiKey), mailCredentialAAD(userID, accountID))
	return ciphertext, nonce, nil
}

func (v *MailCredentialVault) Open(userID uuid.UUID, accountID string, ciphertext, nonce []byte) (string, error) {
	if v == nil || v.aead == nil || userID == uuid.Nil || accountID == "" || len(nonce) != v.aead.NonceSize() || len(ciphertext) < v.aead.Overhead() || len(ciphertext) > 8192 {
		return "", errors.New("Invalid stored mail credential")
	}
	plaintext, err := v.aead.Open(nil, nonce, ciphertext, mailCredentialAAD(userID, accountID))
	if err != nil {
		return "", errors.New("Mail credential authentication failed")
	}
	if len(plaintext) == 0 || len(plaintext) > 4096 {
		return "", errors.New("Invalid decrypted mail credential")
	}
	return string(plaintext), nil
}
