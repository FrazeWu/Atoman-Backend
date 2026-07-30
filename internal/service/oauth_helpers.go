package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strings"

	"atoman/internal/platform/apperr"
)

func randomOAuthToken(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func hashOAuthSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func validateOAuthPassword(password string, passwordConfirm string) error {
	if len(password) < 6 {
		return apperr.BadRequest("oauth.password_too_short", "Password must be at least 6 characters")
	}
	if len(password) > 72 {
		return apperr.BadRequest("oauth.password_too_long", "Password must not exceed 72 bytes")
	}
	if password != passwordConfirm {
		return apperr.BadRequest("oauth.password_mismatch", "Passwords do not match")
	}
	return nil
}

func oauthCodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func sanitizeOAuthReturnTo(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "/"
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(raw, "//") {
		return "/"
	}
	return raw
}
