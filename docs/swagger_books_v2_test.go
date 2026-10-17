package docs

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestSwaggerBookInteractionContractsMatchAcrossFormats(t *testing.T) {
	raw, err := os.ReadFile("swagger.json")
	require.NoError(t, err)
	var expected map[string]any
	require.NoError(t, json.Unmarshal(raw, &expected))
	yamlRaw, err := os.ReadFile("swagger.yaml")
	require.NoError(t, err)
	var fromYAML map[string]any
	require.NoError(t, yaml.Unmarshal(yamlRaw, &fromYAML))
	// Normalize YAML integer types before comparing JSON schemas.
	normalized, err := json.Marshal(fromYAML)
	require.NoError(t, err)
	for _, source := range []string{SwaggerInfo.ReadDoc(), string(normalized)} {
		var spec map[string]any
		require.NoError(t, json.Unmarshal([]byte(source), &spec))
		for _, path := range []string{
			"/api/v1/books/works/{workId}/rating", "/api/v1/books/rankings/high-rated",
			"/api/v1/books/asset-revisions/{revisionId}/passages", "/api/v1/books/passages/{passageId}",
			"/api/v1/books/works/{workId}/reviews", "/api/v1/books/works/{workId}/reviews/{contentId}",
			"/api/v1/books/booklists",
			"/api/v1/books/booklists/{listId}",
			"/api/v1/books/me/booklists",
			"/api/v1/books/me/booklists/{listId}/draft",
			"/api/v1/books/me/booklists/{listId}/publish",
			"/api/v1/books/me/booklists/{listId}/unpublish",
			"/api/v1/books/me/booklists/{listId}",
			"/api/v1/books/booklists/{listId}/bookmark",
			"/api/v1/books/me/booklist-bookmarks",
		} {
			require.Contains(t, expected["paths"], path)
			require.Equal(t, expected["paths"].(map[string]any)[path], spec["paths"].(map[string]any)[path])
		}
		for _, definition := range []string{
			"books.BookWorkRatingDTO", "books.BookRatingRankingItem", "books.BookRatingRankingResult",
			"books.CreateBookDiscussionPassageInput", "books.BookDiscussionPassageDTO", "books.BookDiscussionPassageList",
			"books.LinkBookContentReviewInput", "model.BookContentReview", "books.BookContentReviewDTO", "books.BookContentReviewList",
			"books.BookListEntry",
			"books.SaveBookListDraftInput",
			"books.PublishBookListInput",
			"books.BookListVersionInput",
			"books.BookListDraftDTO",
			"books.BookListChanges",
			"books.BookListCard",
			"books.BookListItemDTO",
			"books.BookListDetail",
			"books.BookListSearchResult",
			"books.SaveBookListBookmarkInput",
			"books.BookListBookmarkDTO",
			"books.BookListBookmarkResult",
		} {
			require.Contains(t, expected["definitions"], definition)
			require.Equal(t, expected["definitions"].(map[string]any)[definition], spec["definitions"].(map[string]any)[definition])
		}
	}
}

func TestSwaggerDocumentsIndependentBookCreationAndRevisionContent(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				In     string `json:"in"`
				Schema struct {
					Ref        string         `json:"$ref"`
					Required   []string       `json:"required"`
					Properties map[string]any `json:"properties"`
				} `json:"schema"`
			} `json:"parameters"`
		} `json:"paths"`
		Definitions map[string]struct {
			Required   []string       `json:"required"`
			Properties map[string]any `json:"properties"`
		} `json:"definitions"`
	}
	raw, err := os.ReadFile("swagger.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &spec))
	for path, methods := range map[string][]string{
		"/api/v1/books/booklists":                                              {"get"},
		"/api/v1/books/booklists/{listId}":                                     {"get"},
		"/api/v1/books/me/booklists":                                           {"get"},
		"/api/v1/books/me/booklists/{listId}/draft":                            {"get", "put"},
		"/api/v1/books/me/booklists/{listId}/publish":                          {"post"},
		"/api/v1/books/me/booklists/{listId}/unpublish":                        {"post"},
		"/api/v1/books/me/booklists/{listId}":                                  {"delete"},
		"/api/v1/books/booklists/{listId}/bookmark":                            {"put", "delete"},
		"/api/v1/books/me/booklist-bookmarks":                                  {"get"},
		"/api/v1/books/asset-revisions/{revisionId}/passages":                  {"get", "post"},
		"/api/v1/books/passages/{passageId}":                                   {"get"},
		"/api/v1/books/works/{workId}/reviews":                                 {"get"},
		"/api/v1/books/works/{workId}/reviews/{contentId}":                     {"put", "delete"},
		"/api/v1/books/works/{workId}/rating":                                  {"get", "put", "delete"},
		"/api/v1/books/rankings/high-rated":                                    {"get"},
		"/api/v1/books/me/reading-sessions/{sessionId}/heartbeat":              {"post"},
		"/api/v1/books/me/reading-statistics":                                  {"get"},
		"/api/v1/books/me/private-notes/{noteId}/publish":                      {"post"},
		"/api/v1/books/me/library":                                             {"get"},
		"/api/v1/books/me/library/works/{workId}":                              {"put"},
		"/api/v1/books/me/library/entries/{entryId}":                           {"delete"},
		"/api/v1/books/me/private-notes":                                       {"get"},
		"/api/v1/books/me/private-notes/{noteId}":                              {"get", "put", "delete"},
		"/api/v1/books/me/private-notes/{noteId}/restore":                      {"post"},
		"/api/v1/books/me/private-notes/export":                                {"get"},
		"/api/v1/books/me/private-notes/import":                                {"post"},
		"/api/v1/books/me/reading-marks/{revisionId}":                          {"get"},
		"/api/v1/books/me/reading-marks/{revisionId}/{markId}":                 {"put", "delete"},
		"/api/v1/books/me/selections/{workId}":                                 {"get", "put"},
		"/api/v1/books/me/reading-sessions":                                    {"post"},
		"/api/v1/books/me/reading-states/{revisionId}":                         {"get", "put"},
		"/api/v1/books/works/{workId}/reading-recommendation":                  {"get"},
		"/api/v1/books/works":                                                  {"get", "post"},
		"/api/v1/books/works/{workId}":                                         {"get", "patch"},
		"/api/v1/books/people":                                                 {"get", "post"},
		"/api/v1/books/works/{workId}/editions":                                {"post"},
		"/api/v1/books/assets/{assetId}/revisions":                             {"get"},
		"/api/v1/books/asset-revisions/{revisionId}/content":                   {"get"},
		"/api/v1/books/wiki/{entityType}/{entityId}/revisions":                 {"get"},
		"/api/v1/books/wiki/revisions/{revisionId}/revert":                     {"post"},
		"/api/v1/books/wiki/merges/preview":                                    {"post"},
		"/api/v1/books/wiki/merges":                                            {"post"},
		"/api/v1/books/wiki/merges/{mergeId}/revert":                           {"post"},
		"/api/v1/books/works/{workId}/assets":                                  {"get", "post"},
		"/api/v1/books/assets/{assetId}/upload-sessions":                       {"post"},
		"/api/v1/books/upload-sessions/{uploadId}":                             {"get"},
		"/api/v1/books/upload-sessions/{uploadId}/parts/{partNumber}":          {"post"},
		"/api/v1/books/upload-sessions/{uploadId}/parts/{partNumber}/complete": {"post"},
		"/api/v1/books/upload-sessions/{uploadId}/complete":                    {"post"},
		"/api/v1/books/works/{workId}/default-assets/{language}":               {"put"},
	} {
		for _, method := range methods {
			require.Contains(t, spec.Paths[path], method, path)
		}
	}
	// swag renders the request body as a $ref into the definitions section.
	input := spec.Paths["/api/v1/books/works"]["post"].Parameters[0].Schema
	require.Equal(t, "#/definitions/books.CreateBookWorkInput", input.Ref)
	work := spec.Definitions["books.CreateBookWorkInput"]
	require.Equal(t, []string{"title"}, work.Required)
	require.Contains(t, work.Properties, "author_ids")
	require.Contains(t, work.Properties, "authors")
	require.NotContains(t, work.Properties, "import_id")
	require.NotContains(t, work.Properties, "edition")
	// The served spec and checked-in YAML must both contain the new routes.
	served := SwaggerInfo.ReadDoc()
	var servedSpec map[string]any
	require.NoError(t, json.Unmarshal([]byte(served), &servedSpec))
	yamlRaw, err := os.ReadFile("swagger.yaml")
	require.NoError(t, err)
	var yamlSpec map[string]any
	require.NoError(t, yaml.Unmarshal(yamlRaw, &yamlSpec))
	for path := range spec.Paths {
		require.Contains(t, servedSpec["paths"], path)
		require.Contains(t, yamlSpec["paths"], path)
	}
}
