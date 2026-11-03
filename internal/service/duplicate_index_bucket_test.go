package service

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

func TestDuplicateIndexHotBucketsPreserveFirstMatchAcrossDates(t *testing.T) {
	rng := rand.New(rand.NewSource(20261103))
	base := time.Date(2026, 11, 3, 14, 0, 0, 0, time.UTC)
	groups := make([]*duplicateGroup, 0, 700)
	index := newDuplicateGroupIndex()
	for i := 0; i < 700; i++ {
		published := base.Add(-time.Duration(rng.Intn(600)) * 24 * time.Hour).Add(time.Duration(rng.Intn(24)) * time.Hour)
		if i%67 == 0 {
			published = time.Time{}
		}
		title := fmt.Sprintf("%04d Unique synthetic research digest publication on AI systems", i)
		if i%51 == 0 {
			title = "含有共同摘要的动态大模型安全研究报告和独立来源" + fmt.Sprint(i)
		}
		group := &duplicateGroup{normalizedTitle: NormalizeFeedItemTitle(title),
			normalizedURL: fmt.Sprintf("https://example.invalid/story/%d", i), publishedAt: published}
		index.add(group, i)
		groups = append(groups, group)
	}
	if len(index.hotFuzzyAnchors) == 0 || len(index.hotSubAnchors) == 0 {
		t.Fatal("did not exercise hot posting promotion")
	}
	for trial := 0; trial < 500; trial++ {
		id := rng.Intn(len(groups))
		date := base.Add(-time.Duration(rng.Intn(600)) * 24 * time.Hour)
		if trial%29 == 0 {
			date = time.Time{}
		}
		title := groups[id].normalizedTitle
		url := ""
		if trial%9 == 0 {
			url = groups[id].normalizedURL
		}
		if trial%11 == 0 {
			title += "!"
		}
		want := -1
		for j, group := range groups {
			if group.matches(url, title, date) {
				want = j
				break
			}
		}
		got := index.firstMatch(groups, url, title, date)
		if got != want {
			t.Fatalf("first match differs at trial=%d got=%d want=%d date=%v", trial, got, want, date)
		}
	}
}

func TestDuplicateIndexHotBucket72HourBoundaryAndUndated(t *testing.T) {
	base := time.Date(2026, 11, 3, 12, 1, 0, 0, time.UTC)
	groups := make([]*duplicateGroup, 0, 200)
	index := newDuplicateGroupIndex()
	for i := 0; i < 200; i++ {
		date := base.Add(time.Duration(i-100) * 24 * time.Hour)
		if i == 41 {
			date = time.Time{}
		}
		title := fmt.Sprintf("item %04d shared exact prefix for title candidate testing", i)
		group := &duplicateGroup{normalizedTitle: title, publishedAt: date, normalizedURL: fmt.Sprintf("https://test.invalid/%d", i)}
		index.add(group, i)
		groups = append(groups, group)
	}
	target := groups[100].normalizedTitle
	for _, date := range []time.Time{base.Add(-72 * time.Hour), base.Add(-72*time.Hour - time.Nanosecond), base.Add(72 * time.Hour), base.Add(72*time.Hour + time.Nanosecond), time.Time{}} {
		want := -1
		for i, group := range groups {
			if group.matches("", target, date) {
				want = i
				break
			}
		}
		got := index.firstMatch(groups, "", target, date)
		if got != want {
			t.Fatalf("boundary date %v got=%d want=%d", date, got, want)
		}
	}
}

func TestDuplicateDateBucketNegativeUnixAndZero(t *testing.T) {
	if got := duplicateDateBucket(time.Time{}); got != duplicateUndatedBucket {
		t.Fatal("lost Go zero sentinel")
	}
	for _, tc := range []struct {
		time time.Time
		want int64
	}{
		{time.Unix(-1, 0), -1},
		{time.Unix(-86400, 0), -1},
		{time.Unix(-86401, 0), -2},
		{time.Unix(0, 0), 0},
	} {
		if got := duplicateDateBucket(tc.time); got != tc.want {
			t.Fatalf("bucket(%v)=%d want %d", tc.time, got, tc.want)
		}
	}
}
