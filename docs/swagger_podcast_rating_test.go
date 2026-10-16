package docs

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestSwaggerPodcastRatingContract(t *testing.T) {
	var jsonSpec, embeddedSpec, yamlSpec map[string]any
	data, err := os.ReadFile("swagger.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &jsonSpec))
	require.NoError(t, json.Unmarshal([]byte(SwaggerInfo.ReadDoc()), &embeddedSpec))
	path := "/api/v1/podcast/episodes/{id}/rating"
	require.Equal(t, jsonSpec["paths"].(map[string]any)[path], embeddedSpec["paths"].(map[string]any)[path])
	for _, name := range []string{"handlers.podcastRatingInput", "handlers.podcastRatingSummary", "model.PodcastEpisode"} {
		require.Equal(t, jsonSpec["definitions"].(map[string]any)[name], embeddedSpec["definitions"].(map[string]any)[name])
	}
	data, err = os.ReadFile("swagger.yaml")
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(data, &yamlSpec))
	for _, spec := range []map[string]any{jsonSpec, yamlSpec} {
		paths := spec["paths"].(map[string]any)
		operations := paths["/api/v1/podcast/episodes/{id}/rating"].(map[string]any)
		for _, method := range []string{"get", "put", "delete"} {
			op := operations[method].(map[string]any)
			require.Contains(t, op["responses"], "200")
			require.Contains(t, op["responses"], "404")
			if method != "get" {
				require.NotEmpty(t, op["security"])
			}
		}
		defs := spec["definitions"].(map[string]any)
		for _, name := range []string{"handlers.podcastRatingSummary", "model.PodcastEpisode"} {
			props := defs[name].(map[string]any)["properties"].(map[string]any)
			for _, field := range []string{"rating_score", "rating_count", "viewer_rating"} {
				require.Contains(t, props, field)
			}
		}
	}
}
