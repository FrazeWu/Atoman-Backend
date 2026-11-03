package feed

import (
	"crypto/sha256"
	"encoding/hex"
)

// Hash only narrows candidates. Exact URL equality and title/window checks
// remain authoritative, so a hypothetical hash collision cannot hide matches.
func feedDedupURLHash(normalizedURL string) string {
	if normalizedURL == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(normalizedURL))
	return hex.EncodeToString(sum[:])
}
