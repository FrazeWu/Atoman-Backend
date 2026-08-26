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

func TestAutomatedMusicOperationsDoNotCreateContribution(t *testing.T) {
	for _, summary := range []string{
		"Initial version (migrated from existing data)",
		"自动匹配歌词",
		"修复 LRC 歌词时间轴",
		"通过专辑版本更新歌词",
	} {
		if !isAutomatedMusicOperation(summary) {
			t.Fatalf("summary %q should be classified as automated", summary)
		}
	}
	if isAutomatedMusicOperation("补充专辑简介") {
		t.Fatal("manual music edit should remain eligible for contribution")
	}
}
