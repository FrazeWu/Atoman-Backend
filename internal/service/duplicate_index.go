package service

import (
	"math"
	"math/bits"
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
	fuzzyAnchors     map[uint64][]int
	substringAnchors map[uint64][]int
	hotFuzzyAnchors  map[uint64]map[int64][]int
	hotSubAnchors    map[uint64]map[int64][]int
	groupDays        []int64
	groupDayCounts   map[int64]int
	hotFingerprints  map[int]duplicateTitleFingerprint
	candidateSeen    []uint32
	candidateEpoch   uint32
	candidateIDs     []int
}

func newDuplicateGroupIndex() *duplicateGroupIndex {
	return &duplicateGroupIndex{
		urls:             make(map[string][]int),
		shortTitles:      make(map[string][]int),
		fuzzyAnchors:     make(map[uint64][]int),
		substringAnchors: make(map[uint64][]int),
		hotFuzzyAnchors:  make(map[uint64]map[int64][]int),
		hotSubAnchors:    make(map[uint64]map[int64][]int),
		groupDayCounts:   make(map[int64]int),
	}
}

const duplicateIndexThreshold = 128
const duplicateShortAnchorSize = 4
const duplicateFuzzyAnchorSize = 6
const duplicateSubstringAnchorSize = 8
const duplicateSubstringAnchorStride = 11
const duplicateHotAnchorThreshold = 96

// A guard only pays off for dense title collisions. Sparse shared wording
// may generate hundreds of candidates, but those are cheaper to check with
// the old bounded Levenshtein path than to build fingerprints and a cache.
const duplicateBigramGuardThreshold = 128
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
func (index *duplicateGroupIndex) addTitlePosting(cold map[uint64][]int,
	hot map[uint64]map[int64][]int, key uint64, id int) {
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
	day := duplicateDateBucket(group.publishedAt)
	index.groupDays = append(index.groupDays, day)
	index.candidateSeen = append(index.candidateSeen, 0)
	index.groupDayCounts[day]++

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
	seen := make(map[uint64]bool)
	for start := 0; start+q <= len(runes); start += q {
		key := duplicateAnchorHash(runes, start, q)
		if seen[key] {
			continue
		}
		seen[key] = true
		index.addTitlePosting(index.fuzzyAnchors, index.hotFuzzyAnchors, key, groupID)
	}
	if len(runes) >= 18 {
		clear(seen)
		for start := 0; start+duplicateSubstringAnchorSize <= len(runes); start += duplicateSubstringAnchorStride {
			key := duplicateAnchorHash(runes, start, duplicateSubstringAnchorSize)
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
func (index *duplicateGroupIndex) addCandidate(id int) {
	if index.candidateSeen[id] != index.candidateEpoch {
		index.candidateSeen[id] = index.candidateEpoch
		index.candidateIDs = append(index.candidateIDs, id)
	}
}

func (index *duplicateGroupIndex) addAnchorCandidates(
	cold map[uint64][]int, hot map[uint64]map[int64][]int, key uint64, date time.Time) {
	for _, id := range cold[key] {
		index.addCandidate(id)
	}
	buckets, ok := hot[key]
	if !ok {
		return
	}
	if date.IsZero() {
		// A zero-dated query may title-match any old representative.
		for _, posting := range buckets {
			for _, id := range posting {
				index.addCandidate(id)
			}
		}
		return
	}
	for _, id := range buckets[duplicateUndatedBucket] {
		index.addCandidate(id)
	}
	first, last := duplicateDateBucket(date.Add(-duplicateTitleWindow)), duplicateDateBucket(date.Add(duplicateTitleWindow))
	for day := first; day <= last; day++ {
		for _, id := range buckets[day] {
			index.addCandidate(id)
		}
	}
}

func (index *duplicateGroupIndex) firstMatch(groups []*duplicateGroup, url, title string, publishedAt time.Time) int {
	index.candidateEpoch++
	if index.candidateEpoch == 0 {
		clear(index.candidateSeen)
		index.candidateEpoch = 1
	}
	index.candidateIDs = index.candidateIDs[:0]
	if url != "" {
		for _, groupID := range index.urls[url] {
			index.addCandidate(groupID)
		}
	}
	runes := []rune(title)
	if len(runes) < duplicateShortAnchorSize {
		for _, groupID := range index.shortTitles[title] {
			index.addCandidate(groupID)
		}
	} else {
		seen := make(map[uint64]bool)
		for _, width := range [...]int{duplicateShortAnchorSize, duplicateFuzzyAnchorSize} {
			duplicateForEachRollingAnchor(runes, width, func(key uint64) {
				if seen[key] {
					return
				}
				seen[key] = true
				index.addAnchorCandidates(index.fuzzyAnchors, index.hotFuzzyAnchors, key, publishedAt)
			})
		}
		duplicateForEachRollingAnchor(runes, duplicateSubstringAnchorSize, func(key uint64) {
			index.addAnchorCandidates(index.substringAnchors, index.hotSubAnchors, key, publishedAt)
		})
	}
	ordered := index.candidateIDs
	sort.Ints(ordered)
	// Ordinary feeds have few candidates. Computing signatures for all
	// representatives (or for every small query) costs more than it saves.
	// Only high-collision title queries use this guard, with lazy per-group
	// fingerprints cached for the lifetime of this one annotation scan.
	useFingerprint := index.shouldUseBigramGuard(len(ordered), publishedAt)
	var fingerprint duplicateTitleFingerprint
	if useFingerprint {
		fingerprint = fingerprintDuplicateTitle(title)
		if index.hotFingerprints == nil {
			index.hotFingerprints = make(map[int]duplicateTitleFingerprint)
		}
	}
	for _, groupID := range ordered {
		group := groups[groupID]
		// URL equality is date-independent and bypasses title prefilter.
		// A conservative bigram lower bound only rejects groups that cannot
		// satisfy EITHER the edit-distance OR exact substring title rule.
		if useFingerprint && !(url != "" && group.normalizedURL != "" && url == group.normalizedURL) {
			groupFingerprint, ok := index.hotFingerprints[groupID]
			if !ok {
				groupFingerprint = fingerprintDuplicateTitle(group.normalizedTitle)
				index.hotFingerprints[groupID] = groupFingerprint
			}
			if !duplicateTitleFingerprintsMayMatch(fingerprint, groupFingerprint) {
				continue
			}
		}
		if group.matches(url, title, publishedAt) {
			return groupID
		}
	}
	return -1
}

// A compact two-rune feature set supplies an admissible lower bound on edit
// distance, not an alternative duplicate rule. A single Levenshtein edit can
// remove at most two adjacent bigrams from either string; a true distance
// <=k therefore cannot have >2k distinct left-only bigrams. Hash collisions
// only create false positives, never false negatives. Exact substring matches
// preserve every bigram of the shorter string inside the longer string.
// Date filtering and URL equality are deliberately handled separately.
type duplicateTitleFingerprint struct {
	features  [4]uint64
	runeCount int
}

func fingerprintDuplicateTitle(title string) duplicateTitleFingerprint {
	result := duplicateTitleFingerprint{}
	var previous rune
	for _, current := range title {
		if result.runeCount > 0 {
			// Fixed unsigned mixing is deterministic and affects only which
			// different bigrams collide (which cannot yield a false negative).
			hash := uint32(previous)*0x9e3779b1 ^ uint32(current)*0x85ebca77
			hash ^= hash >> 16
			index := hash & 255
			result.features[index/64] |= uint64(1) << (index % 64)
		}
		previous = current
		result.runeCount++
	}
	return result
}

func duplicateTitleFingerprintsMayMatch(a, b duplicateTitleFingerprint) bool {
	if a.runeCount == 0 || b.runeCount == 0 {
		return false
	}
	longer, shorter := a, b
	if a.runeCount < b.runeCount {
		longer, shorter = b, a
	}
	// Containment is an independent legacy rule: any exact substring with
	// >=18 runes can match even when edit distance is too high.
	if shorter.runeCount >= 18 {
		contained := true
		for word := range shorter.features {
			if shorter.features[word]&^longer.features[word] != 0 {
				contained = false
				break
			}
		}
		if contained {
			return true
		}
	}
	maxEdits := int(math.Ceil((1 - duplicateTitleSimilarityThreshold) * float64(longer.runeCount)))
	if longer.runeCount-shorter.runeCount > maxEdits {
		return false
	}
	var missingLeft, missingRight int
	for word := range a.features {
		missingLeft += bits.OnesCount64(a.features[word] &^ b.features[word])
		missingRight += bits.OnesCount64(b.features[word] &^ a.features[word])
	}
	return missingLeft <= 2*maxEdits && missingRight <= 2*maxEdits
}

// Candidate density is measured against the current 72h title window,
// not the ever-growing global number of groups. A dense local hotspot should
// be optimized even if years of unrelated feed history exist. URL equality
// is still checked first and never depends on this heuristic.
func (index *duplicateGroupIndex) shouldUseBigramGuard(candidateCount int, date time.Time) bool {
	if candidateCount < duplicateBigramGuardThreshold {
		return false
	}
	if date.IsZero() {
		// The legacy zero-time title rule considers every old group.
		return candidateCount*2 >= len(index.groupDays)
	}
	nearby := index.groupDayCounts[duplicateUndatedBucket]
	first, last := duplicateDateBucket(date.Add(-duplicateTitleWindow)), duplicateDateBucket(date.Add(duplicateTitleWindow))
	for day := first; day <= last; day++ {
		nearby += index.groupDayCounts[day]
	}
	return nearby > 0 && candidateCount*2 >= nearby
}

// Fixed-width rune anchors use a rolling unsigned polynomial key. It replaces
// millions of temporary q-gram strings during a full Feed rebuild. Hash
// collisions can only add candidates, never drop a match: the original
// duplicateGroup.matches predicate still checks every selected group.
const duplicateAnchorHashBase uint64 = 11400714819323198485
const duplicateAnchorWidthSalt uint64 = 0x9e3779b97f4a7c15

func duplicateAnchorHash(runes []rune, start, width int) uint64 {
	h := uint64(0)
	for i := start; i < start+width; i++ {
		h = h*duplicateAnchorHashBase + uint64(runes[i]) + 1
	}
	return h ^ (uint64(width) * duplicateAnchorWidthSalt)
}

func duplicateForEachRollingAnchor(runes []rune, width int, visit func(uint64)) {
	if len(runes) < width {
		return
	}
	rolling := uint64(0)
	for i := 0; i < width; i++ {
		rolling = rolling*duplicateAnchorHashBase + uint64(runes[i]) + 1
	}
	power := uint64(1)
	for i := 1; i < width; i++ {
		power *= duplicateAnchorHashBase
	}
	salt := uint64(width) * duplicateAnchorWidthSalt
	for start := 0; start+width <= len(runes); start++ {
		visit(rolling ^ salt)
		if start+width < len(runes) {
			rolling = (rolling-(uint64(runes[start])+1)*power)*duplicateAnchorHashBase + uint64(runes[start+width]) + 1
		}
	}
}
