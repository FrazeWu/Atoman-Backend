package docs

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSwaggerDocumentsAlbumImportFileAPIs(t *testing.T) {
	raw, err := os.ReadFile("swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths       map[string]map[string]any `json:"paths"`
		Definitions map[string]any            `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}

	paths := map[string][]string{
		"/api/v1/music/imports/albums/{sessionId}/files":                                      {"post"},
		"/api/v1/music/imports/albums/{sessionId}/files/{fileId}/parts/{partNumber}":          {"post"},
		"/api/v1/music/imports/albums/{sessionId}/files/{fileId}/parts/{partNumber}/complete": {"post"},
		"/api/v1/music/imports/albums/{sessionId}/files/{fileId}/complete":                    {"post"},
		"/api/v1/music/imports/albums/{sessionId}/files/{fileId}/retry":                       {"post"},
		"/api/v1/music/imports/albums/{sessionId}/files/{fileId}/replace":                     {"post"},
		"/api/v1/music/imports/albums/{sessionId}/files/{fileId}":                             {"delete"},
		"/api/v1/music/imports/albums/{sessionId}/complete":                                   {"post"},
		"/api/v1/music/imports/albums/{sessionId}":                                            {"delete"},
	}
	for path, methods := range paths {
		operations, ok := spec.Paths[path]
		if !ok {
			t.Fatalf("missing Swagger path %s", path)
		}
		for _, method := range methods {
			operation, ok := operations[method]
			if !ok {
				t.Fatalf("missing Swagger operation %s %s", method, path)
			}
			operationObject, ok := operation.(map[string]any)
			if !ok {
				t.Fatalf("invalid Swagger operation %s %s", method, path)
			}
			responses, ok := operationObject["responses"].(map[string]any)
			if !ok {
				t.Fatalf("missing Swagger responses for %s %s", method, path)
			}
			for _, status := range []string{"422", "503"} {
				if _, ok := responses[status]; !ok {
					t.Fatalf("missing Swagger response %s for %s %s", status, method, path)
				}
			}
		}
	}
	for _, definition := range []string{
		"music.AlbumImportResponse",
		"music.AlbumImportFileResponse",
		"music.AlbumImportMultipartPartUploadResponse",
		"music.RegisterAlbumImportFilesInput",
		"music.AlbumImportFileInput",
	} {
		if _, ok := spec.Definitions[definition]; !ok {
			t.Fatalf("missing Swagger definition %s", definition)
		}
	}
}
