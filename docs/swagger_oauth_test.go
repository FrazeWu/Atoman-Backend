package docs

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSwaggerDocumentsOAuthWebRoutes(t *testing.T) {
	raw, err := os.ReadFile("swagger.json")
	if err != nil {
		t.Fatalf("read swagger.json: %v", err)
	}
	var document struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse swagger.json: %v", err)
	}
	want := map[string][]string{
		"/api/v1/auth/oauth/providers":                {"get"},
		"/api/v1/auth/oauth/{provider}/start":         {"get"},
		"/api/v1/auth/oauth/{provider}/callback":      {"get", "post"},
		"/api/v1/auth/oauth/pending":                  {"get", "delete"},
		"/api/v1/auth/oauth/pending/complete-profile": {"post"},
		"/api/v1/auth/oauth/pending/confirm-account":  {"post"},
		"/api/v1/auth/oauth/identities":               {"get"},
		"/api/v1/auth/oauth/{provider}":               {"delete"},
	}
	for path, methods := range want {
		operations, ok := document.Paths[path]
		if !ok {
			t.Errorf("missing oauth path %s", path)
			continue
		}
		for _, method := range methods {
			if _, ok := operations[method]; !ok {
				t.Errorf("missing %s operation for %s", method, path)
			}
		}
	}
}
