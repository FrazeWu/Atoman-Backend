package recommendation

func rerankBySourceDiversity(items []RankedItem, maxPerSource int) []RankedItem {
	if maxPerSource <= 0 {
		return items
	}

	counts := map[string]int{}
	out := make([]RankedItem, 0, len(items))
	overflow := make([]RankedItem, 0)

	for _, item := range items {
		if item.SourceKey == "" {
			out = append(out, item)
			continue
		}

		if counts[item.SourceKey] < maxPerSource {
			out = append(out, item)
			counts[item.SourceKey]++
			continue
		}

		counts[item.SourceKey]++
		overflow = append(overflow, item)
	}

	return append(out, overflow...)
}
