package docs

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSwaggerDocumentsDMRequestsResponsesAndParameters(t *testing.T) {
	raw, err := os.ReadFile("swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths map[string]map[string]struct {
			Parameters  []any                 `json:"parameters"`
			Responses   map[string]any        `json:"responses"`
			Security    []map[string][]string `json:"security"`
			Description string                `json:"description"`
		} `json:"paths"`
		Definitions map[string]any `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	message := document.Paths["/api/v1/dm/targets/{type}/{id}/messages"]["post"]
	if len(message.Parameters) < 3 || len(message.Responses) == 0 {
		t.Fatalf("message operation lacks parameters or responses: %#v", message)
	}
	if _, ok := document.Definitions["dm.SendInput"]; !ok {
		t.Fatal("dm.SendInput schema missing")
	}
	if _, ok := document.Definitions["dm.MessageResponse"]; !ok {
		t.Fatal("dm.MessageResponse schema missing")
	}
	settings := document.Paths["/api/v1/dm/settings"]["put"]
	if _, ok := settings.Responses["400"]; !ok || len(settings.Security) != 2 || settings.Description == "" {
		t.Fatalf("settings mutation lacks documented errors, auth, or csrf requirement: %#v", settings)
	}
}
