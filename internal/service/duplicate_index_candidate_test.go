package service

import (
	"math/rand"
	"testing"
	"time"
)

// A candidate index may over-include groups; it must never omit a group that
// the exact original matches predicate accepts.
func TestDuplicateIndexNeverMissesMatchingRepresentative(t *testing.T) {
	rng := rand.New(rand.NewSource(20261103))
	alphabet := []rune("abcdefghijklmnopqrstuvxyz0123456789大模型安全系统")
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 30000; i++ {
		length := 1 + rng.Intn(110)
		left := make([]rune, length)
		for j := range left {
			left[j] = alphabet[rng.Intn(len(alphabet))]
		}
		right := append([]rune(nil), left...)
		for changes := rng.Intn(length/7 + 2); changes > 0; changes-- {
			if len(right) < 2 {
				break
			}
			at := rng.Intn(len(right))
			switch rng.Intn(3) {
			case 0:
				right[at] = alphabet[rng.Intn(len(alphabet))]
			case 1:
				right = append(right[:at], right[at+1:]...)
			case 2:
				right = append(right[:at], append([]rune{alphabet[rng.Intn(len(alphabet))]}, right[at:]...)...)
			}
		}
		if len(left) >= 18 && rng.Intn(4) == 0 {
			start := rng.Intn(len(left) - 17)
			if rng.Intn(2) == 0 {
				right = append([]rune(nil), left[start:]...)
			} else {
				right = append([]rune(nil), left[start:start+18]...)
			}
		}
		lhs, rhs := NormalizeFeedItemTitle(string(left)), NormalizeFeedItemTitle(string(right))
		group := &duplicateGroup{normalizedTitle: lhs, publishedAt: now}
		index := newDuplicateGroupIndex()
		index.add(group, 0)
		expected := group.matches("", rhs, now)
		matched := index.firstMatch([]*duplicateGroup{group}, "", rhs, now) == 0
		if expected && !matched {
			t.Fatalf("candidate index dropped a valid match at case %d: %q and %q", i, lhs, rhs)
		}
	}
}
