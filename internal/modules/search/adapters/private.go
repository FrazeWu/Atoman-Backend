package adapters

import "atoman/internal/modules/search"

func privateTypeCapability(key, label string, fields []string) search.TypeCapability {
	return search.TypeCapability{Key: key, Label: label, SearchFields: fields,
		Sorts:   []search.SortDefinition{{Key: "relevance", Label: "相关度"}, {Key: "recent", Label: "最新"}},
		Filters: []search.FilterDefinition{}, DefaultSort: "relevance", DiscoverySort: "recent", AllowEmptyQuery: true}
}
