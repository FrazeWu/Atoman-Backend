package docs

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSwaggerMusicIntegrityContract(t *testing.T) {
	raw, err := os.ReadFile("swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{string(raw), SwaggerInfo.ReadDoc()} {
		var spec struct {
			Definitions map[string]struct {
				Properties map[string]map[string]any `json:"properties"`
			} `json:"definitions"`
		}
		if err := json.Unmarshal([]byte(source), &spec); err != nil {
			t.Fatal(err)
		}
		for schema, field := range map[string]string{"music.RecordSongPlayRequest": "play_event_id", "model.Album": "redirect_song_id", "model.Song": "redirect_to"} {
			if spec.Definitions[schema].Properties[field]["type"] != "string" {
				t.Fatalf("missing %s.%s", schema, field)
			}
		}
		if spec.Definitions["music.PendingLyricAnnotationDTO"].Properties["album_id"]["x-nullable"] != true {
			t.Fatal("standalone lyrics album_id must be nullable")
		}
	}
}
