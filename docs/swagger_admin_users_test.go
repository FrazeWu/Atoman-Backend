package docs

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSwaggerDocumentsAdminUserManagement(t *testing.T) {
	raw, err := os.ReadFile("swagger.json")
	if err != nil {
		t.Fatalf("read swagger.json: %v", err)
	}
	var document struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse swagger.json: %v", err)
	}
	expected := map[string][]string{
		"/api/v1/admin/users":                           {"get", "post"},
		"/api/v1/admin/users/{id}":                      {"get", "patch", "delete"},
		"/api/v1/admin/users/{id}/status":               {"put"},
		"/api/v1/admin/users/{id}/password":             {"put"},
		"/api/v1/admin/users/{id}/login-events":         {"get"},
		"/api/v1/admin/users/{id}/sessions":             {"get", "delete"},
		"/api/v1/admin/users/{id}/sessions/{sessionID}": {"delete"},
		"/api/v1/admin/users/{id}/audit-logs":           {"get"},
		"/api/v1/admin/user-audit-logs":                 {"get"},
	}
	for path, methods := range expected {
		operations, ok := document.Paths[path]
		if !ok {
			t.Fatalf("missing Swagger path %s", path)
		}
		for _, method := range methods {
			if _, ok := operations[method]; !ok {
				t.Fatalf("missing Swagger operation %s %s", method, path)
			}
		}
	}
}
