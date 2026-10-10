package music

import "testing"

func TestNormalizeMusicSourcesRejectsUnsafeAndOversizedSources(t *testing.T) {
	if _, _, err := normalizeMusicSources([]Source{{URL: "javascript:alert(1)"}}, ""); err == nil {
		t.Fatal("expected unsafe source URL to be rejected")
	}
	if _, _, err := normalizeMusicSources([]Source{{URL: "https://example.com"}}, ""); err != nil {
		t.Fatalf("expected https source URL to be accepted: %v", err)
	}
	tooMany := make([]Source, maxMusicSources+1)
	if _, _, err := normalizeMusicSources(tooMany, ""); err == nil {
		t.Fatal("expected too many sources to be rejected")
	}
}
