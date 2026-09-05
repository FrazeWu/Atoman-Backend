package music

import "testing"

func TestMatchDerivedTrackAudioUsesAudioKeyAfterTrackRenameAndReorder(t *testing.T) {
	derivedTracks := []any{
		map[string]any{"title": "IGOR'S THEME", "audio_key": "audio-igor", "audio_url": "https://cdn.test/igor.mp3"},
		map[string]any{"title": "EARFQUAKE", "audio_key": "audio-earfquake", "audio_url": "https://cdn.test/earfquake.mp3"},
	}

	matched := matchDerivedTrackAudio(derivedTracks, AlbumImportTrackPayload{
		Title: "EARFQUAKE (用户修改)", TrackNumber: 1, AudioKey: "audio-earfquake",
	}, map[int]bool{})

	if matched.AudioURL != "https://cdn.test/earfquake.mp3" {
		t.Fatalf("expected selected audio to follow audio key, got %#v", matched)
	}
}

func TestMatchDerivedTrackAudioDoesNotFallBackToArrayPosition(t *testing.T) {
	derivedTracks := []any{
		map[string]any{"title": "IGOR'S THEME", "audio_key": "audio-igor", "audio_url": "https://cdn.test/igor.mp3"},
		map[string]any{"title": "EARFQUAKE", "audio_key": "audio-earfquake", "audio_url": "https://cdn.test/earfquake.mp3"},
	}

	matched := matchDerivedTrackAudio(derivedTracks, AlbumImportTrackPayload{
		Title: "用户改名", TrackNumber: 1,
	}, map[int]bool{})

	if matched.AudioURL != "" {
		t.Fatalf("expected no audio match without a stable identity, got %#v", matched)
	}
}
