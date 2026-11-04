package service

import (
	"math/rand"
	"testing"
)

// Every emitted rolling window must have exactly the same key as a directly
// encoded window; a mismatch could drop a valid candidate on the read side.
func TestDuplicateRollingAnchorMatchesDirectUnicode(t *testing.T) {
	rng := rand.New(rand.NewSource(20261104))
	alphabet := []rune("abcXYZ0091安全大模型之研究📚🔎αβγø")
	for iteration := 0; iteration < 25000; iteration++ {
		n := rng.Intn(160)
		runes := make([]rune, n)
		for j := range runes {
			runes[j] = alphabet[rng.Intn(len(alphabet))]
		}
		for _, width := range []int{duplicateShortAnchorSize, duplicateFuzzyAnchorSize, duplicateSubstringAnchorSize} {
			pos := 0
			duplicateForEachRollingAnchor(runes, width, func(got uint64) {
				want := duplicateAnchorHash(runes, pos, width)
				if got != want {
					t.Fatalf("iter=%d width=%d pos=%d got=%d want=%d", iteration, width, pos, got, want)
				}
				pos++
			})
			expected := n - width + 1
			if expected < 0 {
				expected = 0
			}
			if pos != expected {
				t.Fatalf("iter=%d width=%d count=%d want=%d", iteration, width, pos, expected)
			}
		}
	}
}

func TestDuplicateRollingAnchorRepeatedCommonPhrases(t *testing.T) {
	for _, title := range []string{
		"Common update common update common update on cybersecurity",
		"大语言模型安全评估与动态防御研究动态防御研究动态防御研究",
		"a", "abc", "", "研究 🌸 研究 🌸 研究 🌸",
	} {
		runes := []rune(NormalizeFeedItemTitle(title))
		for _, width := range []int{4, 6, 8} {
			start := 0
			duplicateForEachRollingAnchor(runes, width, func(key uint64) {
				if key != duplicateAnchorHash(runes, start, width) {
					t.Fatal("rolling mismatch")
				}
				start++
			})
		}
	}
}
