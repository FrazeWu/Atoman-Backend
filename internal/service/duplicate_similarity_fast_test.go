package service

import (
	"math/rand"
	"strings"
	"testing"
)

func TestFastDuplicateSimilarityAgreesWithOriginal(t *testing.T) {
	cases := [][2]string{
		{"", ""}, {"a", "ab"}, {"abcdefghij", "abcdefghik"},
		{"abcdefghij", "xbcdefghij"}, {"a long common heading", "a long common heading!"},
		{"大语言模型安全评测", "大语言模型攻击评测"},
		{strings.Repeat("a", 120), strings.Repeat("a", 117) + "bbb"},
		{strings.Repeat("a", 260), strings.Repeat("a", 261)},
	}
	rng := rand.New(rand.NewSource(20261103))
	alphabet := []rune("abcdefghijxy029 中文安全模型")
	for i := 0; i < 5000; i++ {
		length := rng.Intn(80) + 1
		arunes, brunes := make([]rune, length), make([]rune, length)
		for j := range arunes {
			arunes[j] = alphabet[rng.Intn(len(alphabet))]
		}
		copy(brunes, arunes)
		changes := rng.Intn(length/3 + 1)
		for j := 0; j < changes; j++ {
			brunes[rng.Intn(len(brunes))] = alphabet[rng.Intn(len(alphabet))]
		}
		if rng.Intn(3) == 0 && len(brunes) > 2 {
			brunes = brunes[:len(brunes)-rng.Intn(3)]
		}
		cases = append(cases, [2]string{string(arunes), string(brunes)})
		if rng.Intn(4) == 0 {
			cases = append(cases, [2]string{string(brunes), string(arunes)})
		}
	}
	for i, tc := range cases {
		expected := titleSimilarity(tc[0], tc[1]) >= duplicateTitleSimilarityThreshold
		actual := titlesSimilarAboveThreshold(tc[0], tc[1], duplicateTitleSimilarityThreshold)
		if expected != actual {
			t.Fatalf("example %d: expected=%v got=%v for %q and %q", i, expected, actual, tc[0], tc[1])
		}
	}
}
