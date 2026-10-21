package docs

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestSwaggerUnifiedSearchContract(t *testing.T) {
	var spec, embedded, yamlSpec map[string]any
	data, err := os.ReadFile("swagger.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &spec))
	require.NoError(t, json.Unmarshal([]byte(SwaggerInfo.ReadDoc()), &embedded))
	data, err = os.ReadFile("swagger.yaml")
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(data, &yamlSpec))
	for _, path := range []string{"/api/v1/search", "/api/v1/search/capabilities"} {
		paths := spec["paths"].(map[string]any)
		require.Equal(t, paths[path], embedded["paths"].(map[string]any)[path])
		for _, source := range []map[string]any{spec, yamlSpec} {
			op := source["paths"].(map[string]any)[path].(map[string]any)["get"].(map[string]any)
			for _, code := range []string{"200", "400", "401", "403"} {
				require.Contains(t, op["responses"], code)
			}
			var repeatedTypes bool
			for _, raw := range op["parameters"].([]any) {
				param := raw.(map[string]any)
				if param["name"] == "type" {
					repeatedTypes = param["collectionFormat"] == "multi"
				}
			}
			require.True(t, repeatedTypes)
		}
	}
	for _, name := range []string{"search.SearchHit", "search.SearchGroup", "search.SearchResponse", "search.CapabilitiesResponse", "search.TypeCapability"} {
		require.Contains(t, spec["definitions"], name)
		require.Equal(t, spec["definitions"].(map[string]any)[name], embedded["definitions"].(map[string]any)[name])
	}
	group := spec["definitions"].(map[string]any)["search.SearchGroup"].(map[string]any)["properties"].(map[string]any)
	require.Equal(t, true, group["total"].(map[string]any)["x-nullable"])
	require.Equal(t, true, group["error"].(map[string]any)["x-nullable"])
}
