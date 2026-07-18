package oauthprovider

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

type oidcIdentityClaims struct {
	Issuer            string
	Subject           string
	Email             string
	EmailVerified     bool
	Name              string
	Picture           string
	Nonce             string
	TenantID          string
	PreferredUsername string
}

type idTokenVerifier interface {
	Verify(context.Context, string) (oidcIdentityClaims, error)
}

type remoteIDTokenVerifier struct {
	verifier *oidc.IDTokenVerifier
}

func newRemoteIDTokenVerifier(clientID string, jwksURL string) idTokenVerifier {
	keySet := oidc.NewRemoteKeySet(context.Background(), jwksURL)
	return &remoteIDTokenVerifier{verifier: oidc.NewVerifier("", keySet, &oidc.Config{
		ClientID:        clientID,
		SkipIssuerCheck: true,
	})}
}

func (v *remoteIDTokenVerifier) Verify(ctx context.Context, rawToken string) (oidcIdentityClaims, error) {
	token, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return oidcIdentityClaims{}, err
	}
	var raw struct {
		Issuer            string          `json:"iss"`
		Subject           string          `json:"sub"`
		Email             string          `json:"email"`
		EmailVerified     json.RawMessage `json:"email_verified"`
		Name              string          `json:"name"`
		Picture           string          `json:"picture"`
		Nonce             string          `json:"nonce"`
		TenantID          string          `json:"tid"`
		PreferredUsername string          `json:"preferred_username"`
	}
	if err := token.Claims(&raw); err != nil {
		return oidcIdentityClaims{}, err
	}
	verified, err := parseVerifiedClaim(raw.EmailVerified)
	if err != nil {
		return oidcIdentityClaims{}, err
	}
	return oidcIdentityClaims{
		Issuer: raw.Issuer, Subject: raw.Subject, Email: raw.Email,
		EmailVerified: verified, Name: raw.Name, Picture: raw.Picture,
		Nonce: raw.Nonce, TenantID: raw.TenantID,
		PreferredUsername: raw.PreferredUsername,
	}, nil
}

func parseVerifiedClaim(raw json.RawMessage) (bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return false, nil
	}
	var boolean bool
	if err := json.Unmarshal(raw, &boolean); err == nil {
		return boolean, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "true":
			return true, nil
		case "false", "":
			return false, nil
		}
	}
	return false, errors.New("invalid email_verified claim")
}

func validateOIDCNonce(nonce string, expectedHash string) error {
	if nonce == "" || expectedHash == "" {
		return errors.New("missing oidc nonce")
	}
	sum := sha256.Sum256([]byte(nonce))
	actual := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(actual), []byte(expectedHash)) != 1 {
		return errors.New("oidc nonce mismatch")
	}
	return nil
}

func requireIssuer(actual string, allowed ...string) error {
	for _, issuer := range allowed {
		if actual == issuer {
			return nil
		}
	}
	return fmt.Errorf("unexpected oidc issuer %q", actual)
}
