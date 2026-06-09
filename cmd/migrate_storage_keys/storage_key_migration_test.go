package main

import (
	"testing"
	"time"
)

func TestParseObjectURL(t *testing.T) {
	key, ok := parseObjectURL("http://localhost:9100/atoman-assets/video/files/u1/file.mp4", "http://localhost:9100/atoman-assets")
	if !ok {
		t.Fatal("expected URL to parse")
	}
	if key != "video/files/u1/file.mp4" {
		t.Fatalf("unexpected key: %s", key)
	}
}

func TestBuildLegacyVideoKey(t *testing.T) {
	createdAt := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)

	next, ok := buildLegacyVideoKey("video/files/u1/file.mp4", "u1", "files", createdAt)

	if !ok {
		t.Fatal("expected legacy video key")
	}
	if next != "video/files/users/u1/2026/05/file.mp4" {
		t.Fatalf("unexpected key: %s", next)
	}
}

func TestBuildMusicAudioKey(t *testing.T) {
	next := buildMusicAudioKey("music/kanye_west/2049/01 INTRO.wav", "album-1", "song-1")

	if next != "music/audio/albums/album-1/song-1.wav" {
		t.Fatalf("unexpected key: %s", next)
	}
}

func TestBuildMusicCoverKey(t *testing.T) {
	next := buildMusicCoverKey("music/kanye_west/2049/cover.jpg", "album-1")

	if next != "music/covers/albums/album-1/cover.jpg" {
		t.Fatalf("unexpected key: %s", next)
	}
}
