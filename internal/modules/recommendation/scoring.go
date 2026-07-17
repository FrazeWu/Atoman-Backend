package recommendation

func scoreCandidate(mode Mode, c Candidate) float64 {
	switch mode {
	case ModeHot:
		return 0.50*c.TrendScore +
			0.20*c.QualityScore +
			0.20*c.FreshnessScore +
			0.10*c.AuthorityScore
	case ModeFeatured:
		return 0.55*c.QualityScore +
			0.20*c.EditorialScore +
			0.15*c.AuthorityScore +
			0.10*c.TrendScore
	case ModeDiscover:
		if c.QualityScore < 0.30 {
			return 0
		}

		underexposedBonus := 1 - c.ExposureScore
		return 0.45*c.QualityScore +
			0.30*underexposedBonus +
			0.15*c.FreshnessScore
	case ModeLatest:
		return float64(candidatePublishedAtUnixNano(c))
	default:
		return 0
	}
}

func candidatePublishedAtUnixNano(candidate Candidate) int64 {
	if candidate.PublishedAtUnixNano != 0 {
		return candidate.PublishedAtUnixNano
	}
	return candidate.PublishedAtUnix * 1_000_000_000
}
