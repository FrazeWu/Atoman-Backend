package recommendation

import (
	"math/rand"
	"sort"
)

func RankCandidates(mode Mode, candidates []Candidate, limit int) []RankedItem {
	return rankCandidates(mode, candidates, limit)
}

func rankCandidates(mode Mode, candidates []Candidate, limit int) []RankedItem {
	if mode == ModeRandom {
		shuffled := append([]Candidate(nil), candidates...)
		rand.Shuffle(len(shuffled), func(i, j int) {
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		})
		ranked := make([]RankedItem, 0, len(shuffled))
		for _, candidate := range shuffled {
			ranked = append(ranked, RankedItem{Candidate: candidate, FinalScore: 1})
		}
		if limit > 0 && len(ranked) > limit {
			return ranked[:limit]
		}
		return ranked
	}

	ranked := make([]RankedItem, 0, len(candidates))

	for _, candidate := range candidates {
		score := scoreCandidate(mode, candidate)
		if score <= 0 {
			continue
		}

		ranked = append(ranked, RankedItem{
			Candidate:  candidate,
			FinalScore: score,
		})
	}

	sort.Slice(ranked, func(i, j int) bool {
		leftPublishedAt := candidatePublishedAtUnixNano(ranked[i].Candidate)
		rightPublishedAt := candidatePublishedAtUnixNano(ranked[j].Candidate)
		if mode == ModeLatest && leftPublishedAt != rightPublishedAt {
			return leftPublishedAt > rightPublishedAt
		}
		if mode == ModeLatest {
			return ranked[i].EntityID > ranked[j].EntityID
		}
		if ranked[i].FinalScore != ranked[j].FinalScore {
			return ranked[i].FinalScore > ranked[j].FinalScore
		}
		if ranked[i].QualityScore != ranked[j].QualityScore {
			return ranked[i].QualityScore > ranked[j].QualityScore
		}
		if leftPublishedAt != rightPublishedAt {
			return leftPublishedAt > rightPublishedAt
		}
		return ranked[i].EntityID > ranked[j].EntityID
	})

	ranked = rerankBySourceDiversity(ranked, 2)

	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}

	return ranked
}
