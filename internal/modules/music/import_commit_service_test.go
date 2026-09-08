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

func TestMatchDerivedTrackAudioDoesNotUseTitleWhenAudioKeyIsStale(t *testing.T) {
	derivedTracks := []any{
		map[string]any{"title": "Same title", "audio_key": "audio-first", "audio_url": "https://cdn.test/first.mp3"},
		map[string]any{"title": "Same title", "audio_key": "audio-second", "audio_url": "https://cdn.test/second.mp3"},
	}

	matched := matchDerivedTrackAudio(derivedTracks, AlbumImportTrackPayload{
		Title: "Same title", TrackNumber: 1, AudioKey: "audio-missing",
	}, map[int]bool{})

	if matched.AudioURL != "" {
		t.Fatalf("expected stale audio key to prevent title fallback, got %#v", matched)
	}
}

func TestAlbumImportTracksFromDerivedKeepsStableFieldsAndDeletedKeys(t *testing.T) {
	tracks := albumImportTracksFromDerived(map[string]any{
		"commit_request": map[string]any{
			"deleted_import_track_keys": []any{"audio:audio-second"},
		},
		"derived_tracks": []any{
			map[string]any{
				"file_id": "file-first", "audio_key": "audio-first", "title": "First",
				"track_number": 1, "disc_number": 1, "original_title": "Original First",
				"original_track_number": 7, "match_status": "matched",
			},
			map[string]any{
				"file_id": "file-second", "audio_key": "audio-second", "title": "Second",
				"track_number": 2, "disc_number": 1,
			},
		},
	})
	if len(tracks) != 1 || tracks[0].FileID != "file-first" || tracks[0].AudioKey != "audio-first" || tracks[0].OriginalTrack != 7 || tracks[0].MatchStatus != "matched" {
		t.Fatalf("unexpected restored derived tracks: %#v", tracks)
	}
}
