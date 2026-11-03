package service

import (
	"sort"
	"time"
)

// duplicateGroupIndex is a LOSSLESS candidate index. The original group.matches
// still determines correctness and first matching group priority. Posting lists
// for very frequent title anchors are partitioned by UTC day, so a headline
// sharing boilerplate with thousands of years-old articles does not cause a
// quadratic candidate scan. URL matches NEVER have a time limit.
type duplicateGroupIndex struct {
	urls             map[string][]int
	shortTitles      map[string][]int
	fuzzyAnchors     map[string][]int
	substringAnchors map[string][]int
	hotFuzzyAnchors  map[string]map[int64][]int
	hotSubAnchors    map[string]map[int64][]int
	groupDays        []int64
}

func newDuplicateGroupIndex() *duplicateGroupIndex {
	return &duplicateGroupIndex{
		urls:             make(map[string][]int),
		shortTitles:      make(map[string][]int),
		fuzzyAnchors:     make(map[string][]int),
		substringAnchors: make(map[string][]int),
		hotFuzzyAnchors:  make(map[string]map[int64][]int),
		hotSubAnchors:    make(map[string]map[int64][]int),
	}
}

const duplicateIndexThreshold = 128
const duplicateShortAnchorSize = 4
const duplicateFuzzyAnchorSize = 6
const duplicateSubstringAnchorSize = 8
const duplicateSubstringAnchorStride = 11
const duplicateHotAnchorThreshold = 96
const duplicateUndatedBucket int64 = -1 << 63

// floor Unix seconds to UTC days; negative timestamps must round down.
// A zero Go time is special: the legacy title rule matches it regardless
// of the other item's date, so it is indexed in its own universal bucket.
func duplicateDateBucket(t time.Time) int64 {
	if t.IsZero() {
		return duplicateUndatedBucket
	}
	seconds := t.UTC().Unix()
	day := seconds / 86400
	if seconds < 0 && seconds%86400 != 0 {
		day--
	}
	return day
}

// A cold posting stores only a compact list. Once an anchor becomes hot,
// its old IDs are transferred to UTC-day buckets, and all future IDs go
// directly into their day bucket. Only high-frequency keys pay map overhead.
func (index *duplicateGroupIndex) addTitlePosting(cold map[string][]int,
	hot map[string]map[int64][]int, key string, id int) {
	if buckets, exists := hot[key]; exists {
		day := index.groupDays[id]
		buckets[day] = append(buckets[day], id)
		return
	}
	posting := append(cold[key], id)
	if len(posting) < duplicateHotAnchorThreshold {
		cold[key] = posting
		return
	}
	buckets := make(map[int64][]int)
	for _, groupID := range posting {
		day := index.groupDays[groupID]
		buckets[day] = append(buckets[day], groupID)
	}
	hot[key] = buckets
	delete(cold, key)
}

func (index *duplicateGroupIndex) add(group *duplicateGroup, groupID int) {
	// add() is called in increasing groupID order, including when the
	// index is first constructed at the 128-group threshold.
	index.groupDays = append(index.groupDays, duplicateDateBucket(group.publishedAt))
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
		index.addTitlePosting(index.fuzzyAnchors, index.hotFuzzyAnchors, key, groupID)
	}
	if len(runes) >= 18 {
		clear(seen)
		for start := 0; start+duplicateSubstringAnchorSize <= len(runes); start += duplicateSubstringAnchorStride {
			key := string(runes[start : start+duplicateSubstringAnchorSize])
			if seen[key] {
				continue
			}
			seen[key] = true
			index.addTitlePosting(index.substringAnchors, index.hotSubAnchors, key, groupID)
		}
	}
}

// The title branch of group.matches only accepts dates within 72 hours,
// except when EITHER item has Go's zero time. A UTC-day range is a superset
// of that 72-hour window, including its inclusive endpoints; the final
// matches() check still filters off-window items within edge day buckets.
func (index *duplicateGroupIndex) addAnchorCandidates(candidates map[int]struct{},
	cold map[string][]int, hot map[string]map[int64][]int, key string, date time.Time) {
	for _, id := range cold[key] {
		candidates[id] = struct{}{}
	}
	buckets, ok := hot[key]
	if !ok {
		return
	}
	if date.IsZero() {
		// A zero-dated query may title-match any old representative.
		for _, posting := range buckets {
			for _, id := range posting {
				candidates[id] = struct{}{}
			}
		}
		return
	}
	for _, id := range buckets[duplicateUndatedBucket] {
		candidates[id] = struct{}{}
	}
	first, last := duplicateDateBucket(date.Add(-duplicateTitleWindow)), duplicateDateBucket(date.Add(duplicateTitleWindow))
	for day := first; day <= last; day++ {
		for _, id := range buckets[day] {
			candidates[id] = struct{}{}
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
				index.addAnchorCandidates(candidates, index.fuzzyAnchors, index.hotFuzzyAnchors, key, publishedAt)
			}
		}
		for pos := 0; pos+duplicateSubstringAnchorSize <= len(runes); pos++ {
			key := string(runes[pos : pos+duplicateSubstringAnchorSize])
			index.addAnchorCandidates(candidates, index.substringAnchors, index.hotSubAnchors, key, publishedAt)
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
