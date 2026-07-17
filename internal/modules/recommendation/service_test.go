package recommendation

import (
	"math"
	"testing"
)

func TestModeConstants(t *testing.T) {
	if ModeHot != "hot" {
		t.Fatalf("expected ModeHot to be hot, got %q", ModeHot)
	}

	if ModeFeatured != "featured" {
		t.Fatalf("expected ModeFeatured to be featured, got %q", ModeFeatured)
	}

	if ModeDiscover != "discover" {
		t.Fatalf("expected ModeDiscover to be discover, got %q", ModeDiscover)
	}

	if ModeLatest != "latest" {
		t.Fatalf("expected ModeLatest to be latest, got %q", ModeLatest)
	}
}

func TestScoreLatestUsesPublishedTime(t *testing.T) {
	older := Candidate{PublishedAtUnix: 100}
	newer := Candidate{PublishedAtUnix: 200}

	if scoreCandidate(ModeLatest, newer) <= scoreCandidate(ModeLatest, older) {
		t.Fatal("expected latest mode to rank newer candidates higher")
	}
}

func TestScoreHotPrioritizesTrend(t *testing.T) {
	candidate := Candidate{
		TrendScore:     0.9,
		QualityScore:   0.2,
		FreshnessScore: 0.3,
		AuthorityScore: 0.4,
	}

	got := scoreCandidate(ModeHot, candidate)
	want := 0.5*0.9 + 0.2*0.2 + 0.2*0.3 + 0.1*0.4

	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("expected hot score %.2f, got %.2f", want, got)
	}
}

func TestScoreFeaturedPrioritizesQuality(t *testing.T) {
	candidate := Candidate{
		QualityScore:   0.8,
		EditorialScore: 0.5,
		AuthorityScore: 0.4,
		TrendScore:     0.2,
	}

	got := scoreCandidate(ModeFeatured, candidate)
	want := 0.55*0.8 + 0.2*0.5 + 0.15*0.4 + 0.1*0.2

	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("expected featured score %.2f, got %.2f", want, got)
	}
}

func TestScoreDiscoverDropsLowQuality(t *testing.T) {
	candidate := Candidate{
		QualityScore:   0.29,
		ExposureScore:  0.1,
		FreshnessScore: 0.9,
	}

	got := scoreCandidate(ModeDiscover, candidate)

	if got != 0 {
		t.Fatalf("expected discover score 0 for low quality candidate, got %.2f", got)
	}
}

func TestRerankLimitsRepeatedSource(t *testing.T) {
	items := []RankedItem{
		{Candidate: Candidate{EntityID: "a1", SourceKey: "source-a"}, FinalScore: 0.9},
		{Candidate: Candidate{EntityID: "a2", SourceKey: "source-a"}, FinalScore: 0.8},
		{Candidate: Candidate{EntityID: "a3", SourceKey: "source-a"}, FinalScore: 0.7},
		{Candidate: Candidate{EntityID: "b1", SourceKey: "source-b"}, FinalScore: 0.6},
	}

	got := rerankBySourceDiversity(items, 2)

	if len(got) != len(items) {
		t.Fatalf("expected %d items after rerank, got %d", len(items), len(got))
	}

	repeated := 0
	for _, item := range got[:3] {
		if item.SourceKey == "source-a" {
			repeated++
		}
	}

	if repeated > 2 {
		t.Fatalf("expected at most 2 items from source-a in top 3, got %d", repeated)
	}
}

func TestRankCandidatesSortsDescending(t *testing.T) {
	candidates := []Candidate{
		{
			EntityID:       "low",
			SourceKey:      "source-a",
			TrendScore:     0.2,
			QualityScore:   0.2,
			FreshnessScore: 0.2,
			AuthorityScore: 0.2,
		},
		{
			EntityID:       "high",
			SourceKey:      "source-b",
			TrendScore:     0.9,
			QualityScore:   0.8,
			FreshnessScore: 0.7,
			AuthorityScore: 0.6,
		},
	}

	got := rankCandidates(ModeHot, candidates, 2)

	if len(got) != 2 {
		t.Fatalf("expected 2 ranked items, got %d", len(got))
	}

	if got[0].EntityID != "high" {
		t.Fatalf("expected highest score candidate first, got %q", got[0].EntityID)
	}
}
