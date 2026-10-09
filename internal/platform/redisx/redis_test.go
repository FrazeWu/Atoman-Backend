package redisx

import "testing"

func TestNewClientFromURLRejectsInvalidURL(t *testing.T) {
	if _, err := newClientFromURL("://invalid"); err == nil {
		t.Fatal("expected invalid Redis URL to be rejected")
	}
}

func TestKeyUsesConfiguredNamespace(t *testing.T) {
	t.Setenv("REDIS_NAMESPACE", "production")
	if got := Key("atoman:feed:recommendation:v2:articles"); got != "production:atoman:feed:recommendation:v2:articles" {
		t.Fatalf("namespaced key = %q", got)
	}
}

func TestKeyDoesNotDuplicateNamespace(t *testing.T) {
	t.Setenv("REDIS_NAMESPACE", "production")
	if got := Key("production:ratelimit:v1:abc"); got != "production:ratelimit:v1:abc" {
		t.Fatalf("namespaced key = %q", got)
	}
}
