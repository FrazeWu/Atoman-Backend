package docs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSwaggerDoesNotExposeLegacyLyricAnnotations(t *testing.T) {
	assertNoLegacyLyrics := func(t *testing.T, document map[string]any) {
		t.Helper()
		paths, _ := document["paths"].(map[string]any)
		for path := range paths {
			if strings.HasPrefix(path, "/api/v1/songs/{id}/annotations") {
				t.Fatalf("legacy lyric annotation path remains in Swagger: %s", path)
			}
		}
		definitions, _ := document["definitions"].(map[string]any)
		if _, exists := definitions["model.LyricAnnotation"]; exists {
			t.Fatal("legacy model.LyricAnnotation definition remains in Swagger")
		}
	}

	jsonBytes, err := os.ReadFile("swagger.json")
	if err != nil {
		t.Fatalf("read swagger.json: %v", err)
	}
	var jsonDocument map[string]any
	if err := json.Unmarshal(jsonBytes, &jsonDocument); err != nil {
		t.Fatalf("parse swagger.json: %v", err)
	}
	assertNoLegacyLyrics(t, jsonDocument)

	yamlBytes, err := os.ReadFile("swagger.yaml")
	if err != nil {
		t.Fatalf("read swagger.yaml: %v", err)
	}
	var yamlDocument map[string]any
	if err := yaml.Unmarshal(yamlBytes, &yamlDocument); err != nil {
		t.Fatalf("parse swagger.yaml: %v", err)
	}
	assertNoLegacyLyrics(t, yamlDocument)

	docsGo, err := os.ReadFile("docs.go")
	if err != nil {
		t.Fatalf("read docs.go: %v", err)
	}
	if strings.Contains(string(docsGo), "/api/v1/songs/{id}/annotations") ||
		strings.Contains(string(docsGo), `"model.LyricAnnotation"`) {
		t.Fatal("legacy lyric annotation contract remains in docs.go")
	}
}
