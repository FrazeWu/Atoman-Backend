package blog

import (
	"math"
	"testing"
	"time"
)

func TestBlogCompositeScoreUsesConfirmedWeights(t *testing.T) {
	signals := blogEngagementSignals{Reads: 0.2, Bookmarks: 1, Likes: 0.4, Comments: 0.6, Subscribers: 0.8}
	got := blogCompositeScore(signals)
	want := 0.20*0.2 + 0.30*1 + 0.20*0.4 + 0.20*0.6 + 0.10*0.8
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("expected %.3f, got %.3f", want, got)
	}
}

func TestBlogHotScoreAppliesSevenDayDecay(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	recent := blogHotScore(0.8, now, now)
	old := blogHotScore(0.8, now.Add(-14*24*time.Hour), now)
	if recent <= old {
		t.Fatalf("expected recent score %.3f above old score %.3f", recent, old)
	}
}

func TestBlogRecommendedScoreBoostsSubscriptions(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	withoutSubscription := blogRecommendedScore(0.5, false, now, now)
	withSubscription := blogRecommendedScore(0.5, true, now, now)
	if withSubscription <= withoutSubscription {
		t.Fatalf("expected subscription boost, got %.3f <= %.3f", withSubscription, withoutSubscription)
	}
}

func TestRerankBlogDiversityLimitsConsecutiveChannelPosts(t *testing.T) {
	items := []blogRankedPost{
		{ID: "a1", ChannelID: "a", Score: 1},
		{ID: "a2", ChannelID: "a", Score: 0.9},
		{ID: "a3", ChannelID: "a", Score: 0.8},
		{ID: "b1", ChannelID: "b", Score: 0.7},
	}
	got := rerankBlogDiversity(items, 2)
	if len(got) != 4 || got[0].ID != "a1" || got[1].ID != "a2" || got[2].ID != "b1" || got[3].ID != "a3" {
		t.Fatalf("unexpected order: %#v", got)
	}
}
