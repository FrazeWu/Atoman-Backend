package comment

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHotScoreUsesFixedFormula(t *testing.T) {
	want := float64(4*3+7+5*2) / math.Pow(26, 1.2)
	require.InDelta(t, want, HotScore(4, 7, 5, 24*time.Hour), 0.000001)
}

func TestHotScoreTreatsNegativeAgeAsZero(t *testing.T) {
	require.Equal(t, HotScore(2, 3, 4, 0), HotScore(2, 3, 4, -time.Hour))
}
