package music

import "testing"

func TestMusicOperationClassifiesObservedRoutes(t *testing.T) {
	tests := map[string]string{
		"/api/v1/music/search":                        "search",
		"/api/v1/music/imports/albums/session/commit": "import",
		"/api/v1/music/plays":                         "play",
		"/api/v1/music/playback-progress":             "playback_progress",
		"/api/v1/music/playback-session":              "playback_session",
		"/api/v1/music/recommend/albums":              "recommend",
		"/api/v1/music/albums":                        "",
	}
	for path, want := range tests {
		if got := musicOperation(path); got != want {
			t.Errorf("musicOperation(%q) = %q, want %q", path, got, want)
		}
	}
}
