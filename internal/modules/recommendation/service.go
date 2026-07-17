package recommendation

import "sort"

func RankCandidates(mode Mode, candidates []Candidate, limit int) []RankedItem {
	return rankCandidates(mode, candidates, limit)
}

func rankCandidates(mode Mode, candidates []Candidate, limit int) []RankedItem {
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
		return ranked[i].FinalScore > ranked[j].FinalScore
	})

	ranked = rerankBySourceDiversity(ranked, 2)

	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}

	return ranked
}
