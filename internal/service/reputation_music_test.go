package service

import "testing"

func TestScoreMusicChangesUsesHighestMeaningfulAlbumOperation(t *testing.T) {
	before := map[string]any{
		"album": map[string]any{"title": "old", "description": ""},
		"songs": []any{map[string]any{"id": "one"}},
	}
	after := map[string]any{
		"album": map[string]any{"title": "new", "description": "substantial"},
		"songs": []any{map[string]any{"id": "one"}, map[string]any{"id": "two"}},
	}
	score, _ := scoreMusicChanges("album", before, after)
	if score.Family != "album.tracks" || score.Type != "album.tracks.add" || score.Points != 3 {
		t.Fatalf("score = %#v, want album track addition worth 3", score)
	}
}

func TestScoreMusicChangesIgnoresSourceOnlyChange(t *testing.T) {
	before := map[string]any{"name": "Artist", "sources": []any{"one"}}
	after := map[string]any{"name": "Artist", "sources": []any{"two"}}
	score, _ := scoreMusicChanges("artist", before, after)
	if score.Points != 0 {
		t.Fatalf("source-only score = %#v, want zero", score)
	}
}

func TestScoreMusicCreationRequiresMinimumCompleteness(t *testing.T) {
	incomplete := scoreMusicCreation("artist", map[string]any{"name": "Artist", "artist_form": "person"})
	if incomplete.Points != 0 {
		t.Fatalf("incomplete artist score = %#v, want zero", incomplete)
	}
	complete := scoreMusicCreation("artist", map[string]any{"name": "Artist", "artist_form": "person", "nationality": "CN"})
	if complete.Points != 6 {
		t.Fatalf("complete artist score = %#v, want 6", complete)
	}
}

func TestScoreMusicChangesUsesOnePointForAlbumTrackRemoval(t *testing.T) {
	before := map[string]any{"album": map[string]any{}, "songs": []any{map[string]any{"id": "one"}, map[string]any{"id": "two"}}}
	after := map[string]any{"album": map[string]any{}, "songs": []any{map[string]any{"id": "one"}}}
	score, _ := scoreMusicChanges("album", before, after)
	if score.Type != "album.tracks.remove" || score.Points != 1 {
		t.Fatalf("score = %#v, want removal worth 1", score)
	}
}
