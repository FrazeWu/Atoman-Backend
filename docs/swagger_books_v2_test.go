package docs

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestSwaggerDocumentsIndependentBookCreationAndRevisionContent(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				In     string `json:"in"`
				Schema struct {
					Required   []string       `json:"required"`
					Properties map[string]any `json:"properties"`
				} `json:"schema"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	raw, err := os.ReadFile("swagger.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &spec))
	for path, methods := range map[string][]string{
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
	input := spec.Paths["/api/v1/books/works"]["post"].Parameters[0].Schema
	require.Equal(t, []string{"title"}, input.Required)
	require.Contains(t, input.Properties, "author_ids")
	require.Contains(t, input.Properties, "authors")
	require.NotContains(t, input.Properties, "import_id")
	require.NotContains(t, input.Properties, "edition")
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
