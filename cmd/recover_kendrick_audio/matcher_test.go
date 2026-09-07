package main

import (
	"testing"

	"atoman/internal/model"

	"github.com/google/uuid"
)

func TestCanRestoreDeletedSongSkipsActiveDuplicate(t *testing.T) {
	albumID := uuid.MustParse("00000000-0000-0000-0000-000000000010")
	active := model.Song{AlbumID: &albumID, Title: "Donda Chant"}
	deleted := model.Song{AlbumID: &albumID, Title: "DONDA CHANT"}
	occupied := map[string]struct{}{}
	identity, ok := songRestoreIdentity(active)
	if !ok {
		t.Fatal("expected active song identity")
	}
	occupied[identity] = struct{}{}

	if canRestoreDeletedSong(deleted, occupied) {
		t.Fatal("expected duplicate deleted song to be skipped")
	}
}

func TestCanRestoreDeletedSongAcceptsMissingTrack(t *testing.T) {
	albumID := uuid.MustParse("00000000-0000-0000-0000-000000000011")
	deleted := model.Song{AlbumID: &albumID, Title: "Donda Chant"}

	if !canRestoreDeletedSong(deleted, map[string]struct{}{}) {
		t.Fatal("expected deleted song with no active duplicate to be restored")
	}
}

func TestChooseAudioCandidateNormalizesFeaturesAndApostrophes(t *testing.T) {
	target := trackCandidate{Title: "Wesley's Theory", DiscNumber: 1, TrackNumber: 1}
	candidate := trackCandidate{Title: "Wesley’s Theory (feat. George Clinton & Thundercat)", DiscNumber: 1, TrackNumber: 1, ObjectKey: "music/albums/old/tracks/song/audio.mp3"}

	match, ok, reason := chooseAudioCandidate(target, []trackCandidate{candidate})
	if !ok {
		t.Fatalf("expected a match, got reason %q", reason)
	}
	if match.ObjectKey != candidate.ObjectKey {
		t.Fatalf("expected candidate %q, got %q", candidate.ObjectKey, match.ObjectKey)
	}
}

func TestChooseAudioCandidateSkipsAmbiguousTrack(t *testing.T) {
	target := trackCandidate{Title: "LOYALTY.", DiscNumber: 1, TrackNumber: 4}
	candidates := []trackCandidate{
		{ID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), Title: "LOYALTY. (feat. Rihanna)", DiscNumber: 1, TrackNumber: 4, ObjectKey: "first.mp3"},
		{ID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), Title: "LOYALTY. (feat. Rihanna)", DiscNumber: 1, TrackNumber: 4, ObjectKey: "second.mp3"},
	}

	if match, ok, reason := chooseAudioCandidate(target, candidates); ok || match.ObjectKey != "" || reason != "ambiguous" {
		t.Fatalf("expected an ambiguous result, got match=%+v ok=%t reason=%q", match, ok, reason)
	}
}

func TestChooseAudioCandidateFallsBackToUniqueTitle(t *testing.T) {
	target := trackCandidate{Title: "HiiiPower", DiscNumber: 1, TrackNumber: 15}
	candidate := trackCandidate{ID: uuid.MustParse("00000000-0000-0000-0000-000000000003"), Title: "HiiiPoWeR", DiscNumber: 1, TrackNumber: 16, ObjectKey: "hiiipower.mp3"}

	match, ok, reason := chooseAudioCandidate(target, []trackCandidate{candidate})
	if !ok || reason != "album_title" || match.ObjectKey != candidate.ObjectKey {
		t.Fatalf("expected unique title fallback, got match=%+v ok=%t reason=%q", match, ok, reason)
	}
}
