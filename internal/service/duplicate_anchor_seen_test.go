package service

import "testing"

func TestDuplicateAnchorSeenInlineOverflowAndReset(t *testing.T) {
	var seen duplicateAnchorSeen
	for i := uint64(0); i < 1024; i++ {
		if !seen.remember(i) {
			t.Fatalf("first occurrence of %d was dropped", i)
		}
		if seen.remember(i) {
			t.Fatalf("duplicate occurrence of %d was admitted", i)
		}
	}
	for i := uint64(1024); i > 0; i-- {
		if seen.remember(i - 1) {
			t.Fatalf("previous key %d was forgotten", i-1)
		}
	}
	seen = duplicateAnchorSeen{}
	for i := uint64(0); i < 1024; i++ {
		if !seen.remember(i) {
			t.Fatalf("reset lost %d", i)
		}
	}
}
