package docs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Prevent Papers API and its authentication contract from disappearing from
// the published OpenAPI document during unrelated Swagger regenerations.
func TestPapersSwaggerCoversPrivateProjectEndpoints(t *testing.T) {
	raw, err := os.ReadFile("swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			Security []map[string][]string `json:"security"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	expected := map[string][]string{
		"/api/v1/papers/projects/{id}/files/{fileId}/collaboration":       {"get"},
		"/api/v1/papers/projects/{id}/files/{fileId}/collaboration/ws":    {"get"},
		"/api/v1/papers/projects/{id}/members":                            {"get"},
		"/api/v1/papers/projects/{id}/members/{userId}":                   {"put", "delete"},
		"/api/v1/papers/access":                                           {"get"},
		"/api/v1/papers/admin/grants":                                     {"post"},
		"/api/v1/papers/admin/grants/{userId}":                            {"delete"},
		"/api/v1/papers/projects":                                         {"get", "post"},
		"/api/v1/papers/projects/{id}":                                    {"get", "patch", "delete"},
		"/api/v1/papers/projects/{id}/files":                              {"get", "post"},
		"/api/v1/papers/projects/{id}/files/{fileId}":                     {"get", "put"},
		"/api/v1/papers/projects/{id}/files/{fileId}/versions":            {"get"},
		"/api/v1/papers/projects/{id}/files/{fileId}/versions/{revision}": {"get"},
		"/api/v1/papers/projects/{id}/snapshots":                          {"get", "post"},
		"/api/v1/papers/projects/{id}/snapshots/{snapshotId}":             {"get", "delete"},
		"/api/v1/papers/projects/{id}/snapshots/{snapshotId}/restore":     {"post"},
	}
	for route, methods := range expected {
		for _, method := range methods {
			operation, ok := spec.Paths[route][method]
			if !ok {
				t.Errorf("missing %s %s", strings.ToUpper(method), route)
				continue
			}
			if len(operation.Security) == 0 {
				t.Errorf("missing auth security for %s %s", strings.ToUpper(method), route)
			}
		}
	}
}
