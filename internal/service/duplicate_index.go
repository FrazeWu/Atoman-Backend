package service

import (
	"sort"
	"time"
)

// duplicateGroupIndex is a lossless candidate filter: final acceptance and
// the first matching representative are still determined by group.matches.
// For 90% similarity, at most about one character in ten may change. At
// least one of the disjoint 6-rune anchors in a >=12-rune title survives all
// accepted edit sequences; shorter titles use 4-rune anchors instead.
// An exact substring match requires >=18 runes, so 8-rune anchors every 11
// positions ensure every qualifying substring includes one whole anchor.
type duplicateGroupIndex struct {
	urls             map[string][]int
	shortTitles      map[string][]int
	fuzzyAnchors     map[string][]int
	substringAnchors map[string][]int
}

func newDuplicateGroupIndex() *duplicateGroupIndex {
	return &duplicateGroupIndex{
		urls:             make(map[string][]int),
		shortTitles:      make(map[string][]int),
		fuzzyAnchors:     make(map[string][]int),
		substringAnchors: make(map[string][]int),
	}
}

// Linear first-match scans are faster for small sets of representatives.
const duplicateIndexThreshold = 128
const duplicateShortAnchorSize = 4
const duplicateFuzzyAnchorSize = 6
const duplicateSubstringAnchorSize = 8
const duplicateSubstringAnchorStride = 11

func (index *duplicateGroupIndex) add(group *duplicateGroup, groupID int) {
	if group.normalizedURL != "" {
		index.urls[group.normalizedURL] = append(index.urls[group.normalizedURL], groupID)
	}
	runes := []rune(group.normalizedTitle)
	if len(runes) < duplicateShortAnchorSize {
		if group.normalizedTitle != "" {
			index.shortTitles[group.normalizedTitle] = append(index.shortTitles[group.normalizedTitle], groupID)
		}
		return
	}
	q := duplicateShortAnchorSize
	if len(runes) >= 12 {
		q = duplicateFuzzyAnchorSize
	}
	seen := make(map[string]bool)
	for start := 0; start+q <= len(runes); start += q {
		key := string(runes[start : start+q])
		if seen[key] {
			continue
		}
		seen[key] = true
		index.fuzzyAnchors[key] = append(index.fuzzyAnchors[key], groupID)
	}
	if len(runes) >= 18 {
		clear(seen)
		for start := 0; start+duplicateSubstringAnchorSize <= len(runes); start += duplicateSubstringAnchorStride {
			key := string(runes[start : start+duplicateSubstringAnchorSize])
			if seen[key] {
				continue
			}
			seen[key] = true
			index.substringAnchors[key] = append(index.substringAnchors[key], groupID)
		}
	}
}

func (index *duplicateGroupIndex) firstMatch(groups []*duplicateGroup, url, title string, publishedAt time.Time) int {
	candidates := make(map[int]struct{})
	if url != "" {
		for _, groupID := range index.urls[url] {
			candidates[groupID] = struct{}{}
		}
	}
	runes := []rune(title)
	if len(runes) < duplicateShortAnchorSize {
		for _, groupID := range index.shortTitles[title] {
			candidates[groupID] = struct{}{}
		}
	} else {
		seen := make(map[string]bool)
		for _, width := range [...]int{duplicateShortAnchorSize, duplicateFuzzyAnchorSize} {
			for pos := 0; pos+width <= len(runes); pos++ {
				key := string(runes[pos : pos+width])
				if seen[key] {
					continue
				}
				seen[key] = true
				for _, groupID := range index.fuzzyAnchors[key] {
					candidates[groupID] = struct{}{}
				}
			}
		}
		for pos := 0; pos+duplicateSubstringAnchorSize <= len(runes); pos++ {
			key := string(runes[pos : pos+duplicateSubstringAnchorSize])
			for _, groupID := range index.substringAnchors[key] {
				candidates[groupID] = struct{}{}
			}
		}
	}
	ordered := make([]int, 0, len(candidates))
	for groupID := range candidates {
		ordered = append(ordered, groupID)
	}
	sort.Ints(ordered)
	for _, groupID := range ordered {
		if groups[groupID].matches(url, title, publishedAt) {
			return groupID
		}
	}
	return -1
}
