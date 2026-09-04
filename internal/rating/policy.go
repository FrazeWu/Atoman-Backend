package rating

import "math"

const (
	MinimumScore       = 1
	MaximumScore       = 10
	PublicMinimumCount = 5
	ScoreRangeMessage  = "score must be between 1 and 10"
)

func ValidScore(score int) bool {
	return score >= MinimumScore && score <= MaximumScore
}

func RoundAverage(score float64) float64 {
	return math.Round(score*10) / 10
}
