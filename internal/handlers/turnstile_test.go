package handlers

import (
	"testing"

	"github.com/gin-gonic/gin"
)

func TestVerifyTurnstileSkipsWhenNotConfiguredOutsideProduction(t *testing.T) {
	t.Setenv("ENV", "development")
	t.Setenv("GIN_MODE", gin.DebugMode)
	t.Setenv("TURNSTILE_SECRET_KEY", "")

	err := verifyTurnstileToken("missing-token", "")
	if err != nil {
		t.Fatalf("expected development without secret to skip Turnstile, got %v", err)
	}
}

func TestVerifyTurnstileRequiresSecretInProduction(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("GIN_MODE", gin.ReleaseMode)
	t.Setenv("TURNSTILE_SECRET_KEY", "")

	err := verifyTurnstileToken("token", "203.0.113.10")
	if err == nil {
		t.Fatal("expected production without secret to fail")
	}
}

func TestVerifyTurnstileRequiresTokenInProduction(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("GIN_MODE", gin.ReleaseMode)
	t.Setenv("TURNSTILE_SECRET_KEY", "secret")

	err := verifyTurnstileToken("", "203.0.113.10")
	if err == nil {
		t.Fatal("expected production without token to fail")
	}
}
