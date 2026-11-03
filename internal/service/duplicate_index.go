package service

import (
	"sort"
	"time"
)

// duplicateGroupIndex narrows matching candidates without changing the
// first-matching-group rule in AnnotateDuplicateFeedItems. Only representative
// titles and URLs are indexed, because matches compare those same fields.
//
// A 90% Levenshtein match can change at most floor(len(groupTitle)/9)
// runes. One more disjoint 4-rune anchor must therefore survive any allowed
// edit sequence. Substring matches require at least 18 runes; an anchor every
// 15 positions ensures that every such substring contains a stored anchor.
// The original group.matches method remains the final authority.
type duplicateGroupIndex struct {
	urls        map[string][]int
	shortTitles map[string][]int
	anchors     map[string][]int
}

func newDuplicateGroupIndex() *duplicateGroupIndex {
	return &duplicateGroupIndex{
		urls:        make(map[string][]int),
		shortTitles: make(map[string][]int),
		anchors:     make(map[string][]int),
	}
}

// Below this size the linear first-match scan is cheaper than building a map.
const duplicateIndexThreshold = 1024

const duplicateAnchorSize = 4
const duplicateSubstringAnchorStride = 15

func (index *duplicateGroupIndex) add(group *duplicateGroup, groupID int) {
	if group.normalizedURL != "" {
		index.urls[group.normalizedURL] = append(index.urls[group.normalizedURL], groupID)
	}
	runes := []rune(group.normalizedTitle)
	if len(runes) < duplicateAnchorSize {
		if group.normalizedTitle != "" {
			index.shortTitles[group.normalizedTitle] = append(index.shortTitles[group.normalizedTitle], groupID)
		}
		return
	}
	seen := make(map[string]bool)
	addAnchor := func(start int) {
		if start+duplicateAnchorSize > len(runes) {
			return
		}
		key := string(runes[start : start+duplicateAnchorSize])
		if seen[key] {
			return
		}
		seen[key] = true
		index.anchors[key] = append(index.anchors[key], groupID)
	}
	for i := 0; i <= len(runes)/9; i++ {
		addAnchor(i * duplicateAnchorSize)
	}
	for start := 0; start+duplicateAnchorSize <= len(runes); start += duplicateSubstringAnchorStride {
		addAnchor(start)
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
	if len(runes) < duplicateAnchorSize {
		for _, groupID := range index.shortTitles[title] {
			candidates[groupID] = struct{}{}
		}
	} else {
		seen := make(map[string]bool)
		for pos := 0; pos+duplicateAnchorSize <= len(runes); pos++ {
			key := string(runes[pos : pos+duplicateAnchorSize])
			if seen[key] {
				continue
			}
			seen[key] = true
			for _, groupID := range index.anchors[key] {
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
