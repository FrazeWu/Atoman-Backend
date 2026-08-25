package docs

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSwaggerCoversPublicCreatorRSS(t *testing.T) {
	raw, err := os.ReadFile("swagger.json")
	if err != nil {
		t.Fatalf("read swagger.json: %v", err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			Produces []string `json:"produces"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse swagger.json: %v", err)
	}

	for _, path := range []string{
		"/api/v1/rss/users/{username}.xml",
		"/api/v1/rss/channels/{slug}.xml",
		"/api/v1/rss/collections/{id}.xml",
	} {
		operation, ok := spec.Paths[path]["get"]
		if !ok {
			t.Errorf("missing Swagger operation GET %s", path)
			continue
		}
		if !containsSwaggerValue(operation.Produces, "application/rss+xml") {
			t.Errorf("Swagger operation GET %s must produce application/rss+xml, got %v", path, operation.Produces)
		}
	}

	for _, path := range []string{
		"/api/v1/channels/{slug}/rss/podcast",
		"/api/v1/channels/slug/{slug}/rss/video",
		"/api/v1/blog/channels/slug/{slug}/rss/article",
		"/api/v1/feed/rss/{username}",
	} {
		if _, ok := spec.Paths[path]; ok {
			t.Errorf("legacy RSS path %s is still documented", path)
		}
	}
}

func containsSwaggerValue(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
