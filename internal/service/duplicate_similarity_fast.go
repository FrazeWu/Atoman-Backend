package service

import "math"

// titlesSimilarAboveThreshold only needs to answer the duplicate threshold
// question. The original full-distance implementation is retained for
// differential testing. This bounded algorithm has identical acceptance
// semantics and avoids O(n*m) work for clearly unrelated long headlines.
func titlesSimilarAboveThreshold(a, b string, threshold float64) bool {
	if a == b {
		return true
	}
	arunes, brunes := []rune(a), []rune(b)
	if len(arunes) == 0 || len(brunes) == 0 {
		return false
	}
	maxLen := len(arunes)
	if len(brunes) > maxLen {
		maxLen = len(brunes)
	}
	// Round upward to avoid excluding an integer edit count accepted by the
	// original floating-point 1 - distance/maxLen >= threshold comparison.
	maxDistance := int(math.Ceil((1 - threshold) * float64(maxLen)))
	if delta := len(arunes) - len(brunes); delta > maxDistance || -delta > maxDistance {
		return false
	}
	d := levenshteinDistanceAtMost(arunes, brunes, maxDistance)
	if d > maxDistance {
		return false
	}
	return 1-float64(d)/float64(maxLen) >= threshold
}

// Returns maxDistance+1 when the true distance exceeds maxDistance.
// The common <=256-rune headline case uses stack arrays to avoid garbage
// allocations for dynamic-programming rows.
func levenshteinDistanceAtMost(a, b []rune, maxDistance int) int {
	if len(a) == 0 {
		if len(b) > maxDistance {
			return maxDistance + 1
		}
		return len(b)
	}
	if len(b) == 0 {
		if len(a) > maxDistance {
			return maxDistance + 1
		}
		return len(a)
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	width := len(b) + 1
	var previousStack, currentStack [257]int
	var previous, current []int
	if width <= len(previousStack) {
		previous, current = previousStack[:width], currentStack[:width]
	} else {
		previous, current = make([]int, width), make([]int, width)
	}
	unreachable := maxDistance + 1
	for col := range previous {
		if col <= maxDistance {
			previous[col] = col
		} else {
			previous[col] = unreachable
		}
	}
	for row := 1; row <= len(a); row++ {
		begin := row - maxDistance
		if begin < 1 {
			begin = 1
		}
		end := row + maxDistance
		if end > len(b) {
			end = len(b)
		}
		if begin == 1 {
			current[0] = row
		} else {
			current[begin-1] = unreachable
		}
		best := unreachable
		for col := begin; col <= end; col++ {
			cost := 0
			if a[row-1] != b[col-1] {
				cost = 1
			}
			deleteCost := previous[col] + 1
			insertCost := current[col-1] + 1
			replaceCost := previous[col-1] + cost
			value := deleteCost
			if insertCost < value {
				value = insertCost
			}
			if replaceCost < value {
				value = replaceCost
			}
			current[col] = value
			if value < best {
				best = value
			}
		}
		if end+1 < width {
			current[end+1] = unreachable
		}
		if best > maxDistance {
			return unreachable
		}
		previous, current = current, previous
	}
	if previous[len(b)] > maxDistance {
		return unreachable
	}
	return previous[len(b)]
}
