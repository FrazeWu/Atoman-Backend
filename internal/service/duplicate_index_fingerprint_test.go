package service

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
)

func TestDuplicateBigramFingerprintNeverRejectsLegacyTitleMatch(t *testing.T) {
	rng := rand.New(rand.NewSource(20261103))
	alphabet := []rune("abcxyz0123456789技术安全测试大模型网络语言")
	makeTitle := func(count int) string {
		var s strings.Builder
		for i := 0; i < count; i++ {
			s.WriteRune(alphabet[rng.Intn(len(alphabet))])
		}
		return s.String()
	}
	for i := 0; i < 35000; i++ {
		a := makeTitle(1 + rng.Intn(210))
		b := makeTitle(1 + rng.Intn(210))
		switch i % 11 {
		case 0:
			b = a
		case 1, 2, 3:
			runes := []rune(a)
			edits := 1 + rng.Intn(len(runes)/12+1)
			for j := 0; j < edits; j++ {
				at := rng.Intn(len(runes))
				switch rng.Intn(3) {
				case 0:
					runes[at] = alphabet[rng.Intn(len(alphabet))]
				case 1:
					runes = append(runes[:at], runes[at+1:]...)
				case 2:
					runes = append(runes[:at], append([]rune{alphabet[rng.Intn(len(alphabet))]}, runes[at:]...)...)
				}
				if len(runes) == 0 {
					runes = []rune{'a'}
				}
			}
			b = string(runes)
		case 4, 5:
			arunes := []rune(a)
			if len(arunes) >= 18 {
				start := rng.Intn(len(arunes) - 17)
				b = string(arunes[start : start+18+rng.Intn(len(arunes)-start-17)])
			}
		case 6:
			b = "common digest about security research " + a
		case 7:
			a = "common digest about security research " + a
		case 8:
			b = strings.TrimSpace(a) + "末尾"
		case 9:
			a = strings.Repeat("安全", 20) + a
			b = strings.Repeat("安全", 20) + b
		}
		a = NormalizeFeedItemTitle(a)
		b = NormalizeFeedItemTitle(b)
		group := &duplicateGroup{normalizedTitle: a, publishedAt: time.Now()}
		matched := group.matches("", b, group.publishedAt)
		maybe := duplicateTitleFingerprintsMayMatch(fingerprintDuplicateTitle(a), fingerprintDuplicateTitle(b))
		if matched && !maybe {
			t.Fatalf("fingerprint false negative i=%d titleA=%q titleB=%q", i, a, b)
		}
	}
}

func TestDuplicateBigramIndexPreservesSharedPhraseClusteringAndOrder(t *testing.T) {
	base := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	items := make([]model.FeedItem, 1250)
	for i := range items {
		unique := fmt.Sprintf("%032x", i*7919+13)
		title := unique + " Common coverage of the latest research digest and scientific discoveries"
		if i%47 == 0 && i > 0 {
			title = items[i-1].Title
		}
		if i%101 == 0 && i > 0 {
			title = "Latest news: " + items[i-1].Title
		}
		items[i] = model.FeedItem{Base: model.Base{ID: uuid.New()}, Title: title,
			Link:        fmt.Sprintf("https://synthetic.invalid/article/%d", i),
			PublishedAt: base.Add(-time.Duration(i) * 24 * time.Second), Author: fmt.Sprintf("Source %d", i%20)}
		if i%173 == 0 {
			items[i].PublishedAt = time.Time{}
		}
	}
	expected := append([]model.FeedItem(nil), items...)
	actual := append([]model.FeedItem(nil), items...)
	annotateDuplicatesReference(expected)
	AnnotateDuplicateFeedItems(actual)
	if !reflect.DeepEqual(actual, expected) {
		for i := range actual {
			if !reflect.DeepEqual(actual[i], expected[i]) {
				t.Fatalf("unexpected cluster i=%d title=%q got=%+v expected=%+v", i, actual[i].Title, actual[i], expected[i])
			}
		}
		t.Fatal("optimized fingerprint changed reference clusters")
	}
}

func TestDuplicateBigramCollisionCannotRejectTrueSubstring(t *testing.T) {
	left := "The latest deep systematic exploration of semantic jailbreak vulnerabilities and safety boundaries"
	right := "deep systematic exploration of semantic jailbreak vulnerabilities"
	if !duplicateTitleFingerprintsMayMatch(fingerprintDuplicateTitle(left), fingerprintDuplicateTitle(right)) {
		t.Fatal("true substring was rejected")
	}
}

func BenchmarkDuplicateBigramSharedPhraseManyTitles(b *testing.B) {
	original := make([]model.FeedItem, 2000)
	for i := range original {
		original[i] = model.FeedItem{Base: model.Base{ID: uuid.New()},
			Title:       fmt.Sprintf("%032x Common coverage of the latest research digest and scientific discoveries", i*7919+13),
			Link:        fmt.Sprintf("https://synthetic.invalid/article/%d", i),
			PublishedAt: time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC).Add(-time.Duration(i) * 24 * time.Second)}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		items := append([]model.FeedItem(nil), original...)
		AnnotateDuplicateFeedItems(items)
	}
}

func TestDuplicateBigramGuardRequiresDenseLocalCandidates(t *testing.T) {
	index := newDuplicateGroupIndex()
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 400; i++ {
		date := now.Add(-24 * time.Hour)
		if i%2 == 1 {
			date = now.Add(-300 * 24 * time.Hour)
		}
		title := fmt.Sprintf("%032x Synthetic research digest common headlines", i*7919+13)
		group := &duplicateGroup{normalizedTitle: title, publishedAt: date}
		index.add(group, i)
	}
	if index.shouldUseBigramGuard(100, now) {
		t.Fatal("100 candidates must use old fast path")
	}
	if !index.shouldUseBigramGuard(150, now) {
		t.Fatal("150 of 200 nearby candidates is a dense local hotspot")
	}
	if index.shouldUseBigramGuard(150, now.Add(300*24*time.Hour)) {
		t.Fatal("remote unrelated day should not count as a local hotspot")
	}
	if index.shouldUseBigramGuard(150, time.Time{}) {
		t.Fatal("zero-time search should use entire group population")
	}
	if !index.shouldUseBigramGuard(250, time.Time{}) {
		t.Fatal("dense zero-time candidate set was ignored")
	}
}
