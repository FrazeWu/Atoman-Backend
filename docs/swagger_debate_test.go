package docs

import (
	"encoding/json"
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDebateSwaggerUsesHTTPXResponseEnvelopes(t *testing.T) {
	documents := map[string]map[string]any{}
	jsonBytes, err := os.ReadFile("swagger.json")
	if err != nil {
		t.Fatalf("read swagger.json: %v", err)
	}
	var jsonDocument map[string]any
	if err := json.Unmarshal(jsonBytes, &jsonDocument); err != nil {
		t.Fatalf("parse swagger.json: %v", err)
	}
	documents["json"] = jsonDocument

	yamlBytes, err := os.ReadFile("swagger.yaml")
	if err != nil {
		t.Fatalf("read swagger.yaml: %v", err)
	}
	var yamlDocument map[string]any
	if err := yaml.Unmarshal(yamlBytes, &yamlDocument); err != nil {
		t.Fatalf("parse swagger.yaml: %v", err)
	}
	documents["yaml"] = yamlDocument

	for name, document := range documents {
		t.Run(name, func(t *testing.T) {
			assertDebateSwaggerContract(t, document)
		})
	}
}

func assertDebateSwaggerContract(t *testing.T, document map[string]any) {
	t.Helper()
	operations := []struct {
		path, method, response string
	}{
		{"/api/v1/debate/topics", "get", "handlers.DebateListResponse"},
		{"/api/v1/debate/topics", "post", "handlers.DebateResponse"},
		{"/api/v1/debate/topics/{id}", "get", "handlers.DebateResponse"},
		{"/api/v1/debate/topics/{id}", "put", "handlers.DebateResponse"},
		{"/api/v1/debate/topics/{id}/archive", "post", "handlers.DebateResponse"},
		{"/api/v1/debate/topics/{id}/revisions", "get", "handlers.DebateRevisionListResponse"},
		{"/api/v1/debate/topics/{id}/revisions/{revisionID}", "get", "handlers.DebateRevisionResponse"},
		{"/api/v1/debate/topics/{id}/revisions/{revisionID}/diff", "get", "handlers.DebateRevisionDiffResponse"},
		{"/api/v1/debate/topics/{id}/revisions/{revisionID}/revert", "post", "handlers.DebateResponse"},
		{"/api/v1/debate/topics/{id}/references/{relationID}/reconfirm", "post", "handlers.DebateResponse"},
		{"/api/v1/debate/topics/{id}/protection", "put", "handlers.DebateMessageResponse"},
		{"/api/v1/debate/topics/{id}/protection", "delete", "handlers.DebateMessageResponse"},
		{"/api/v1/debates/{id}/relations", "get", "handlers.DebateGraphResponse"},
		{"/api/v1/debate/topics/{id}/votes", "get", "handlers.DebateVoteResponse"},
		{"/api/v1/debate/topics/{id}/vote", "put", "handlers.DebateVoteResponse"},
		{"/api/v1/debate/topics/{id}/vote", "delete", "handlers.DebateVoteResponse"},
		{"/api/v1/debate/topics/{id}/conclusions", "get", "handlers.DebateConclusionListResponse"},
	}
	paths := swaggerMap(t, document, "paths")
	for _, expected := range operations {
		path := swaggerMap(t, paths, expected.path)
		operation := swaggerMap(t, path, expected.method)
		responses := swaggerMap(t, operation, "responses")
		response := swaggerMap(t, responses, successStatus(expected.method, expected.path))
		schema := swaggerMap(t, response, "schema")
		if got := schema["$ref"]; got != "#/definitions/"+expected.response {
			t.Errorf("%s %s response schema = %v, want %s", expected.method, expected.path, got, expected.response)
		}
	}

	definitions := swaggerMap(t, document, "definitions")
	assertSwaggerDataRef(t, definitions, "handlers.DebateResponse", "#/definitions/debate.DebateDTO")
	assertSwaggerDataRef(t, definitions, "handlers.DebateVoteResponse", "#/definitions/debate_voting.VoteStats")
	listProperties := swaggerMap(t, swaggerMap(t, definitions, "handlers.DebateListResponse"), "properties")
	listData := swaggerMap(t, listProperties, "data")
	if got := listData["type"]; got != "array" {
		t.Errorf("DebateListResponse.data type = %v, want array", got)
	}
	items := swaggerMap(t, listData, "items")
	if got := items["$ref"]; got != "#/definitions/model.Debate" {
		t.Errorf("DebateListResponse.data items = %v", got)
	}
	meta := swaggerMap(t, listProperties, "meta")
	if got := meta["$ref"]; got != "#/definitions/httpx.PageMeta" {
		t.Errorf("DebateListResponse.meta = %v", got)
	}
}

func successStatus(method, path string) string {
	if method == "post" && path == "/api/v1/debate/topics" {
		return "201"
	}
	return "200"
}

func assertSwaggerDataRef(t *testing.T, definitions map[string]any, definition, want string) {
	t.Helper()
	properties := swaggerMap(t, swaggerMap(t, definitions, definition), "properties")
	data := swaggerMap(t, properties, "data")
	if got := data["$ref"]; got != want {
		t.Errorf("%s.data = %v, want %s", definition, got, want)
	}
}

func swaggerMap(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("Swagger object %q is missing or has type %T", key, parent[key])
	}
	return value
}
