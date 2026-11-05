package mail

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"github.com/google/uuid"
	"io"
	"strings"
)

// MailOutboxVault uses its own AES-256-GCM key, never the R2 token or site password.
type MailOutboxVault struct{ aead cipher.AEAD }

func NewMailOutboxVault(k string) (*MailOutboxVault, error) {
	key, e := base64.StdEncoding.DecodeString(strings.TrimSpace(k))
	if e != nil || len(key) != 32 {
		return nil, errors.New("Mail pending send key must be a separate 32-byte base64 secret")
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, errors.New("Pending send encryption initialization failed")
	}
	aead, e := cipher.NewGCM(block)
	if e != nil {
		return nil, errors.New("Pending send encryption initialization failed")
	}
	return &MailOutboxVault{aead: aead}, nil
}
func outboxAAD(u, id uuid.UUID) []byte {
	return []byte("atoman-mail-pending/v1/" + u.String() + "/" + id.String())
}
func (v *MailOutboxVault) Seal(u, id uuid.UUID, raw []byte) ([]byte, error) {
	if v == nil || v.aead == nil || u == uuid.Nil || id == uuid.Nil || len(raw) == 0 || len(raw) > maxMailSnapshotBytes {
		return nil, errors.New("Invalid pending mail snapshot")
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, e := io.ReadFull(rand.Reader, nonce); e != nil {
		return nil, errors.New("Cannot generate pending mail nonce")
	}
	return v.aead.Seal(append([]byte{1}, nonce...), nonce, raw, outboxAAD(u, id)), nil
}
func (v *MailOutboxVault) Open(u, id uuid.UUID, blob []byte) ([]byte, error) {
	if v == nil || v.aead == nil || u == uuid.Nil || id == uuid.Nil || len(blob) < 1+v.aead.NonceSize()+v.aead.Overhead() || len(blob) > maxMailSnapshotBytes+1024 || blob[0] != 1 {
		return nil, errors.New("Invalid pending mail ciphertext")
	}
	n := v.aead.NonceSize()
	raw, e := v.aead.Open(nil, blob[1:1+n], blob[1+n:], outboxAAD(u, id))
	if e != nil {
		return nil, errors.New("Pending mail authentication failed")
	}
	return raw, nil
}
