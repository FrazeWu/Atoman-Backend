package service

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
)

// This reference implementation intentionally retains the original linear
// first-matching-group logic. Any index optimization must match it exactly.
func annotateDuplicatesReference(items []model.FeedItem) {
	groups := make([]*duplicateGroup, 0, len(items))
	for i := range items {
		items[i].IsDuplicate = false
		items[i].DuplicateCount = 1
		items[i].DuplicateOfID = nil
		items[i].DuplicateSources = nil
		items[i].DuplicateItemIDs = []uuid.UUID{items[i].ID}
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
				indices: []int{i}, normalizedURL: normalizedURL,
				normalizedTitle: normalizedTitle, publishedAt: items[i].PublishedAt,
				sources: map[string]struct{}{duplicateSourceTitle(items[i]): {}},
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
		itemIDs := make([]uuid.UUID, 0, len(group.indices))
		for _, itemIndex := range group.indices {
			itemIDs = append(itemIDs, items[itemIndex].ID)
		}
		for position, itemIndex := range group.indices {
			items[itemIndex].DuplicateCount = len(group.indices)
			items[itemIndex].DuplicateSources = append([]string(nil), sourceTitles...)
			items[itemIndex].DuplicateItemIDs = append([]uuid.UUID(nil), itemIDs...)
			if position == 0 {
				continue
			}
			items[itemIndex].IsDuplicate = true
			primaryID := items[primaryIndex].ID
			items[itemIndex].DuplicateOfID = &primaryID
		}
	}
}

func TestDuplicateIndexMatchesReference(t *testing.T) {
	titles := []string{
		"The quick brown fox jumps over the lazy dog",
		"The quick brown fox jumps over the lazy bog",
		"The quick brown fox jumps over the very lazy dog",
		"An entirely different article with a very long title",
		"This title includes a twelve character middle substring deliberately",
		"a twelve character middle substring",
		"abcdefghijklmno abcdefghijklmnopqrst uvwxyz0123456789",
		"abcdefghijklmnopqrst",
		"科技 创新 和 大模型 系统 安全 测评 设计",
		"科技 创新 和 大模型 系统 安全 测评 实验",
		"cat", "cat", "a", "ab", "abc", "abcdefghi", "abcdefghij", "",
	}
	urls := []string{"", "https://example.com/item/one?utm_a=x", "https://example.com/item/one", "https://other.net/story", "https://another.org/post"}
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for seed := int64(1); seed <= 10; seed++ {
		rng := rand.New(rand.NewSource(seed))
		items := make([]model.FeedItem, 0, 240)
		for i := 0; i < 240; i++ {
			title := titles[rng.Intn(len(titles))]
			if rng.Intn(3) == 0 {
				title = fmt.Sprintf("Unique title %d variable %04d", i, rng.Intn(10000))
			}
			publishedAt := base.Add(time.Duration(rng.Intn(500)-250) * time.Hour)
			if rng.Intn(30) == 0 {
				publishedAt = time.Time{}
			}
			items = append(items, model.FeedItem{
				Base: model.Base{ID: uuid.New()}, Title: title,
				Link: urls[rng.Intn(len(urls))], PublishedAt: publishedAt,
				Author: fmt.Sprintf("Source %d", rng.Intn(5)),
			})
		}
		reference := append([]model.FeedItem(nil), items...)
		got := append([]model.FeedItem(nil), items...)
		annotateDuplicatesReference(reference)
		AnnotateDuplicateFeedItems(got)
		if !reflect.DeepEqual(got, reference) {
			for i := range got {
				if !reflect.DeepEqual(got[i], reference[i]) {
					t.Fatalf("seed %d item %d: indexed and reference results differ: got %+v, expected %+v", seed, i, got[i], reference[i])
				}
			}
			t.Fatalf("seed %d: indexed and reference results differ", seed)
		}
	}
}

func TestDuplicateIndexCatchesMiddleSubstringAndNearMatch(t *testing.T) {
	cases := [][2]string{
		{"this has a very long beginning and then embedded material followed by a very long ending", "embedded material followed by a very"},
		{"1234567890123456789012345678901234567890", "12345678901234567890123456789012345678a0"},
		{"a long string with recognizable contents and extra words", "recognizable contents"},
		{"abc", "abc"},
	}
	for _, pair := range cases {
		first := model.FeedItem{Base: model.Base{ID: uuid.New()}, Title: pair[0]}
		second := model.FeedItem{Base: model.Base{ID: uuid.New()}, Title: pair[1]}
		a := []model.FeedItem{first, second}
		b := []model.FeedItem{first, second}
		AnnotateDuplicateFeedItems(a)
		annotateDuplicatesReference(b)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("near-match or substring discrepancy: %q vs %q", pair[0], pair[1])
		}
	}
}

func TestDuplicateIndexMatchesReferenceAboveThreshold(t *testing.T) {
	rng := rand.New(rand.NewSource(20261103))
	base := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	letters := []rune("abcdefghijklmnopqrstuvwxyz")
	items := make([]model.FeedItem, 1100)
	for i := range items {
		title := make([]rune, 22)
		for j := range title {
			title[j] = letters[rng.Intn(len(letters))]
		}
		link := fmt.Sprintf("https://example.org/article/%d", i)
		if i > 0 && i%55 == 0 {
			title = []rune(items[i-1].Title)
			link = items[i-1].Link + "?utm_source=mirror"
		}
		items[i] = model.FeedItem{Base: model.Base{ID: uuid.New()}, Title: string(title), Link: link,
			PublishedAt: base.Add(time.Duration(rng.Intn(60)) * time.Minute)}
	}
	expected := append([]model.FeedItem(nil), items...)
	actual := append([]model.FeedItem(nil), items...)
	annotateDuplicatesReference(expected)
	AnnotateDuplicateFeedItems(actual)
	if !reflect.DeepEqual(actual, expected) {
		for i := range actual {
			if !reflect.DeepEqual(actual[i], expected[i]) {
				t.Fatalf("large index result differs at item %d", i)
			}
		}
		t.Fatal("large index result differs from reference")
	}
}

func duplicateBenchmarkItems() []model.FeedItem {
	items := make([]model.FeedItem, 600)
	for i := range items {
		items[i] = model.FeedItem{Base: model.Base{ID: uuid.New()}, Title: fmt.Sprintf("Article about %08x computer systems and distributed indexing", i),
			Link: fmt.Sprintf("https://example.com/articles/%d", i), PublishedAt: time.Now().Add(-time.Duration(i) * time.Minute)}
	}
	return items
}

func BenchmarkDuplicateIndexManyTitles(b *testing.B) {
	items := duplicateBenchmarkItems()
	b.ResetTimer()
	for run := 0; run < b.N; run++ {
		copyItems := append([]model.FeedItem(nil), items...)
		AnnotateDuplicateFeedItems(copyItems)
	}
}

func BenchmarkDuplicateReferenceManyTitles(b *testing.B) {
	items := duplicateBenchmarkItems()
	b.ResetTimer()
	for run := 0; run < b.N; run++ {
		copyItems := append([]model.FeedItem(nil), items...)
		annotateDuplicatesReference(copyItems)
	}
}
