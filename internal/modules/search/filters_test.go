package search

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func parseRequest(t *testing.T, query string) (SearchRequest, error) {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/?"+query, nil)
	return ParseSearchRequest(c)
}

func TestNormalizeSearchRequest(t *testing.T) {
	req, err := parseRequest(t, "q=++三体++世界++&type=work&type=work&filter[language]=zh&filter[language]=zh&filter[language]=en")
	require.NoError(t, err)
	require.Equal(t, "三体 世界", req.Query)
	require.Equal(t, []string{"work"}, req.Types)
	require.Equal(t, []string{"zh", "en"}, req.Filters["language"])
	require.Equal(t, ScopePublic, req.Scope)
	require.Equal(t, ViewGrouped, req.View)
	require.Equal(t, 1, req.Page)
	require.Equal(t, DefaultPageSize, req.PageSize)
}

func TestParseSearchRequestRejectsInvalidParameters(t *testing.T) {
	for _, query := range []string{
		"scope=private", "view=all", "q=a&q=b", "unknown=x", "user_id=" + uuid.NewString(),
		"page=0", "page=-1", "page=bad", "page=99999999999999999999999", "page_size=51", "page_size=0",
		"page=2", "view=list", "view=list&module=books&type=work&type=author", "type=", "module=books;drop", "sort=created%20desc",
		"filter[a=x", "filter[]=x", "filter[x][y]=z", "filter[x]=", "q=%ZZ", "q=" + strings.Repeat("中", MaxQueryRunes+1),
	} {
		t.Run(query[:min(len(query), 70)], func(t *testing.T) {
			_, err := parseRequest(t, query)
			require.Error(t, err)
		})
	}
}

func TestValidatePagination(t *testing.T) {
	page, size, err := ValidatePagination(0, 0)
	require.NoError(t, err)
	require.Equal(t, 1, page)
	require.Equal(t, 24, size)
	_, _, err = ValidatePagination(201, 50)
	require.NoError(t, err)
	_, _, err = ValidatePagination(202, 50)
	require.Error(t, err)
	_, _, err = ValidatePagination(int(^uint(0)>>1), 50)
	require.Error(t, err)
}

func TestValidateSearchRequest(t *testing.T) {
	minimum, maximum := 1900.0, 2100.0
	filters := []FilterDefinition{
		{Key: "language", Label: "语言", Kind: "enum", Multiple: true, Options: []FilterOption{{Value: "zh"}, {Value: "en"}}},
		{Key: "author_id", Kind: "entity"}, {Key: "published_at", Kind: "date"},
		{Key: "year", Kind: "number", Integer: true, Min: &minimum, Max: &maximum}, {Key: "active", Kind: "boolean"},
	}
	kind := testType("work")
	kind.Filters = filters
	caps := []ModuleCapabilities{{Module: "books", Types: []TypeCapability{kind}}}
	for key, value := range map[string]string{"language": "zh", "author_id": uuid.NewString(), "published_at": "2026-10-21", "year": "2000", "active": "true"} {
		require.NoError(t, ValidateSearchRequest(SearchRequest{Query: "x", Filters: map[string][]string{key: {value}}}, caps))
	}
	for key, value := range map[string]string{"unknown": "x", "language": "xx", "author_id": "bad", "published_at": "2026-02-30", "year": "NaN", "active": "1"} {
		require.Error(t, ValidateSearchRequest(SearchRequest{Query: "x", Filters: map[string][]string{key: {value}}}, caps), key)
	}
	for _, year := range []string{"1899", "2101", "Inf", "2000.5"} {
		require.Error(t, ValidateSearchRequest(SearchRequest{Query: "x", Filters: map[string][]string{"year": {year}}}, caps))
	}
	require.Error(t, ValidateSearchRequest(SearchRequest{Query: "x", Sort: "unknown"}, caps))
	require.Error(t, ValidateSearchRequest(SearchRequest{Query: "x", Filters: map[string][]string{"year": {"2000", "2001"}}}, caps))
	other := testType("author")
	caps[0].Types = append(caps[0].Types, other)
	require.Error(t, ValidateSearchRequest(SearchRequest{Query: "x", Filters: map[string][]string{"language": {"zh"}}}, caps))
}
