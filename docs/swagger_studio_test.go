package docs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type studioSwaggerOperation struct {
	Security  []map[string][]string      `json:"security"`
	Responses map[string]json.RawMessage `json:"responses"`
}

func TestStudioSwaggerCoversUnifiedCreatorAPI(t *testing.T) {
	raw, err := os.ReadFile("swagger.json")
	if err != nil {
		t.Fatalf("read swagger.json: %v", err)
	}
	var spec struct {
		Paths map[string]map[string]studioSwaggerOperation `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse swagger.json: %v", err)
	}

	expected := map[string][]string{
		"/api/v1/studio/state":                        {"get", "patch"},
		"/api/v1/studio/dashboard":                    {"get"},
		"/api/v1/studio/channels":                     {"get", "post"},
		"/api/v1/studio/channels/{id}":                {"patch", "delete"},
		"/api/v1/studio/{module}/contents":            {"get"},
		"/api/v1/studio/{module}/contents/{id}/share": {"post"},
		"/api/v1/studio/{module}/collections":         {"get", "post"},
		"/api/v1/studio/{module}/collections/{id}":    {"patch", "delete"},
		"/api/v1/studio/{module}/analytics":           {"get"},
		"/api/v1/studio/{module}/interactions":        {"get"},
		"/api/v1/studio/{module}/settings":            {"get", "patch"},
	}
	for path, methods := range expected {
		for _, method := range methods {
			if _, ok := spec.Paths[path][method]; !ok {
				t.Errorf("missing Swagger operation %s %s", strings.ToUpper(method), path)
			}
		}
	}

	writes := map[string][]string{
		"/api/v1/studio/state":                        {"patch"},
		"/api/v1/studio/channels":                     {"post"},
		"/api/v1/studio/channels/{id}":                {"patch", "delete"},
		"/api/v1/studio/{module}/contents/{id}/share": {"post"},
		"/api/v1/studio/{module}/collections":         {"post"},
		"/api/v1/studio/{module}/collections/{id}":    {"patch", "delete"},
		"/api/v1/studio/{module}/settings":            {"patch"},
	}
	for path, methods := range writes {
		for _, method := range methods {
			operation, ok := spec.Paths[path][method]
			if !ok {
				continue
			}
			if !hasStudioBearerAuth(operation.Security) {
				t.Errorf("Swagger operation %s %s is missing BearerAuth", strings.ToUpper(method), path)
			}
			for _, status := range []string{"400", "401", "403"} {
				if _, ok := operation.Responses[status]; !ok {
					t.Errorf("Swagger operation %s %s is missing response %s", strings.ToUpper(method), path, status)
				}
			}
		}
	}
}

func hasStudioBearerAuth(security []map[string][]string) bool {
	for _, requirement := range security {
		if _, ok := requirement["BearerAuth"]; ok {
			return true
		}
	}
	return false
}
