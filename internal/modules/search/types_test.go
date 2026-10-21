package search

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeSearchHit(t *testing.T) {
	hit := NormalizeSearchHit(SearchHit{Module: " books ", Type: "work", ID: " 42 ", Title: " 示例 ", Href: "/books/work/42"})
	require.Equal(t, "books:work:42", hit.Key)
	require.NoError(t, ValidateSearchHit(hit))
	for _, href := range []string{"https://evil.test", "//evil.test", "/%2fevil.test", "/%5cevil.test", "javascript:alert(1)", "relative"} {
		hit.Href = href
		require.Error(t, ValidateSearchHit(hit), href)
	}
}
