package comment

import (
	"math"
	"time"
)

func HotScore(rootLikes, childLikes, childCount int, age time.Duration) float64 {
	numerator := float64(rootLikes*3 + childLikes + childCount*2)
	hours := math.Max(0, age.Hours())
	return numerator / math.Pow(hours+2, 1.2)
}
