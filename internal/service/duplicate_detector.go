package service

import (
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"

	"atoman/internal/model"
)

const (
	duplicateTitleWindow              = 72 * time.Hour
	duplicateTitleSimilarityThreshold = 0.9
)

type duplicateGroup struct {
	indices         []int
	normalizedURL   string
	normalizedTitle string
	publishedAt     time.Time
	sources         map[string]struct{}
}

// AnnotateDuplicateFeedItems marks duplicate RSS items within the current timeline slice.
// The first item in a duplicate cluster remains visible; later items are marked as duplicates.
func AnnotateDuplicateFeedItems(items []model.FeedItem) {
	if len(items) == 0 {
		return
	}

	groups := make([]*duplicateGroup, 0, len(items))

	for i := range items {
		items[i].IsDuplicate = false
		items[i].DuplicateCount = 1
		items[i].DuplicateOfID = nil
		items[i].DuplicateSources = nil

		normalizedURL := NormalizeFeedItemURL(items[i].Link)
		normalizedTitle := NormalizeFeedItemTitle(items[i].Title)

		matched := -1
		for groupIndex := range groups {
			if groups[groupIndex].matches(normalizedURL, normalizedTitle, items[i].PublishedAt) {
				matched = groupIndex
				break
			}
		}

		if matched == -1 {
			groups = append(groups, &duplicateGroup{
				indices:         []int{i},
				normalizedURL:   normalizedURL,
				normalizedTitle: normalizedTitle,
				publishedAt:     items[i].PublishedAt,
				sources: map[string]struct{}{
					duplicateSourceTitle(items[i]): {},
				},
			})
			continue
		}

		groups[matched].indices = append(groups[matched].indices, i)
		groups[matched].sources[duplicateSourceTitle(items[i])] = struct{}{}
	}

	for _, group := range groups {
		if len(group.indices) < 2 {
			continue
		}

		primaryIndex := group.indices[0]
		sourceTitles := sortedDuplicateSources(group.sources)

		for position, itemIndex := range group.indices {
			items[itemIndex].DuplicateCount = len(group.indices)
			items[itemIndex].DuplicateSources = append([]string(nil), sourceTitles...)
			if position == 0 {
				continue
			}

			items[itemIndex].IsDuplicate = true
			primaryID := items[primaryIndex].ID
			items[itemIndex].DuplicateOfID = &primaryID
		}
	}
}

func (group *duplicateGroup) matches(normalizedURL, normalizedTitle string, publishedAt time.Time) bool {
	if normalizedURL != "" && group.normalizedURL != "" && normalizedURL == group.normalizedURL {
		return true
	}

	if normalizedTitle == "" || group.normalizedTitle == "" {
		return false
	}

	if !publishedWithinWindow(group.publishedAt, publishedAt, duplicateTitleWindow) {
		return false
	}

	if titleSimilarity(normalizedTitle, group.normalizedTitle) >= duplicateTitleSimilarityThreshold {
		return true
	}

	shorter, longer := shorterAndLongerTitle(normalizedTitle, group.normalizedTitle)
	return len([]rune(shorter)) >= 18 && strings.Contains(longer, shorter)
}

func NormalizeFeedItemURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return strings.TrimSuffix(strings.ToLower(raw), "/")
	}

	parsed.Fragment = ""
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.TrimPrefix(strings.ToLower(parsed.Host), "www.")

	cleanPath := path.Clean(parsed.Path)
	if cleanPath == "." {
		cleanPath = ""
	}
	if cleanPath != "/" {
		cleanPath = strings.TrimSuffix(cleanPath, "/")
	}
	parsed.Path = cleanPath

	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "utm_") ||
			lower == "fbclid" ||
			lower == "gclid" ||
			lower == "mc_cid" ||
			lower == "mc_eid" ||
			lower == "ref" ||
			lower == "ref_src" ||
			lower == "source" {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()

	return strings.TrimSuffix(parsed.String(), "?")
}

func NormalizeFeedItemTitle(title string) string {
	title = strings.ToLower(strings.TrimSpace(title))
	if title == "" {
		return ""
	}

	var builder strings.Builder
	builder.Grow(len(title))
	lastWasSpace := false

	for _, r := range title {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			builder.WriteRune(r)
			lastWasSpace = false
		case unicode.IsSpace(r):
			if !lastWasSpace {
				builder.WriteByte(' ')
				lastWasSpace = true
			}
		default:
			if !lastWasSpace {
				builder.WriteByte(' ')
				lastWasSpace = true
			}
		}
	}

	return strings.TrimSpace(builder.String())
}

func duplicateSourceTitle(item model.FeedItem) string {
	if item.FeedSource != nil && strings.TrimSpace(item.FeedSource.Title) != "" {
		return strings.TrimSpace(item.FeedSource.Title)
	}
	if strings.TrimSpace(item.Author) != "" {
		return strings.TrimSpace(item.Author)
	}
	return "未知来源"
}

func sortedDuplicateSources(sourceSet map[string]struct{}) []string {
	sources := make([]string, 0, len(sourceSet))
	for source := range sourceSet {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	return sources
}

func publishedWithinWindow(a, b time.Time, window time.Duration) bool {
	if a.IsZero() || b.IsZero() {
		return true
	}
	diff := a.Sub(b)
	if diff < 0 {
		diff = -diff
	}
	return diff <= window
}

func titleSimilarity(a, b string) float64 {
	if a == b {
		return 1
	}

	arunes := []rune(a)
	brunes := []rune(b)
	if len(arunes) == 0 || len(brunes) == 0 {
		return 0
	}

	distance := levenshteinDistance(arunes, brunes)
	maxLen := len(arunes)
	if len(brunes) > maxLen {
		maxLen = len(brunes)
	}

	return 1 - float64(distance)/float64(maxLen)
}

func levenshteinDistance(a, b []rune) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)

	for j := 0; j <= len(b); j++ {
		previous[j] = j
	}

	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}

			deletion := previous[j] + 1
			insertion := current[j-1] + 1
			substitution := previous[j-1] + cost

			current[j] = minInt(deletion, insertion, substitution)
		}
		copy(previous, current)
	}

	return previous[len(b)]
}

func shorterAndLongerTitle(a, b string) (string, string) {
	if len([]rune(a)) <= len([]rune(b)) {
		return a, b
	}
	return b, a
}

func minInt(values ...int) int {
	min := values[0]
	for _, value := range values[1:] {
		if value < min {
			min = value
		}
	}
	return min
}
