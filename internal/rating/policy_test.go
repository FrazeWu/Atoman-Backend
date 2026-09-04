package rating

import "testing"

func TestTenPointRatingPolicy(t *testing.T) {
	for _, score := range []int{1, 5, 9, 10} {
		if !ValidScore(score) {
			t.Fatalf("score %d should be valid", score)
		}
	}
	for _, score := range []int{0, 11} {
		if ValidScore(score) {
			t.Fatalf("score %d should be invalid", score)
		}
	}
	if PublicMinimumCount != 5 {
		t.Fatalf("public minimum count = %d, want 5", PublicMinimumCount)
	}
}

func TestRoundAverageKeepsOneDecimal(t *testing.T) {
	if got := RoundAverage(8.666); got != 8.7 {
		t.Fatalf("RoundAverage(8.666) = %v, want 8.7", got)
	}
}
