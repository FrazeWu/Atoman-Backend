package docs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type swaggerOperation struct {
	Parameters []struct {
		Name string `json:"name"`
		In   string `json:"in"`
	} `json:"parameters"`
	Responses map[string]struct {
		Schema json.RawMessage `json:"schema"`
	} `json:"responses"`
}

func TestForumSwaggerCoversNewAPIPaths(t *testing.T) {
	raw, err := os.ReadFile("swagger.json")
	if err != nil {
		t.Fatalf("read swagger.json: %v", err)
	}
	var spec struct {
		Paths map[string]map[string]swaggerOperation `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse swagger.json: %v", err)
	}

	expected := map[string][]string{
		"/api/v1/forum/search":                                {"get"},
		"/api/v1/forum/follows":                               {"get"},
		"/api/v1/forum/follows/{targetType}":                  {"put", "delete"},
		"/api/v1/forum/groups":                                {"get", "post"},
		"/api/v1/forum/groups/{groupID}":                      {"put", "delete"},
		"/api/v1/forum/groups/{groupID}/members/{userID}":     {"put", "delete"},
		"/api/v1/forum/category-permissions":                  {"get", "put"},
		"/api/v1/forum/category-permissions/{permissionID}":   {"delete"},
		"/api/v1/forum/reports/{reportID}/resolve":            {"post"},
		"/api/v1/forum/moderation/reports":                    {"get"},
		"/api/v1/forum/moderation/reports/{reportID}/resolve": {"post"},
		"/api/v1/forum/moderation/users":                      {"get"},
		"/api/v1/forum/moderation/user-actions":               {"get"},
		"/api/v1/forum/moderation/users/{userID}/actions":     {"post"},
		"/api/v1/forum/trust/me":                              {"get"},
		"/api/v1/forum/trust/users/{userID}":                  {"get"},
		"/api/v1/forum/trust/users/{userID}/evaluate":         {"post"},
	}
	for path, methods := range expected {
		for _, method := range methods {
			operation, ok := spec.Paths[path][method]
			if !ok {
				t.Errorf("missing Swagger operation %s %s", strings.ToUpper(method), path)
				continue
			}
			if !hasDocumentedSuccessSchema(operation) {
				t.Errorf("Swagger operation %s %s has no success response schema", strings.ToUpper(method), path)
			}
		}
	}

	assertSwaggerQueryParam(t, spec.Paths, "/api/v1/forum/search", "get", "q")
	assertSwaggerQueryParam(t, spec.Paths, "/api/v1/forum/follows/{targetType}", "put", "target_key")
	assertSwaggerQueryParam(t, spec.Paths, "/api/v1/forum/follows/{targetType}", "delete", "target_key")
	assertSwaggerQueryParam(t, spec.Paths, "/api/v1/forum/category-permissions", "get", "category_id")
	assertSwaggerQueryParam(t, spec.Paths, "/api/v1/forum/moderation/users", "get", "q")
	assertSwaggerQueryParam(t, spec.Paths, "/api/v1/forum/moderation/user-actions", "get", "user_id")
	assertSwaggerQueryParam(t, spec.Paths, "/api/v1/forum/moderation/reports", "get", "status")
	assertSwaggerQueryParam(t, spec.Paths, "/api/v1/forum/moderation/reports", "get", "page")
	assertSwaggerQueryParam(t, spec.Paths, "/api/v1/forum/moderation/reports", "get", "page_size")
}

func hasDocumentedSuccessSchema(operation swaggerOperation) bool {
	for status, response := range operation.Responses {
		if strings.HasPrefix(status, "2") && len(response.Schema) > 0 && string(response.Schema) != "null" {
			return true
		}
	}
	return false
}

func assertSwaggerQueryParam(t *testing.T, paths map[string]map[string]swaggerOperation, path string, method string, name string) {
	t.Helper()
	for _, parameter := range paths[path][method].Parameters {
		if parameter.In == "query" && parameter.Name == name {
			return
		}
	}
	t.Errorf("Swagger operation %s %s is missing query parameter %q", strings.ToUpper(method), path, name)
}
