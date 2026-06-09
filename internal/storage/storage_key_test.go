package storage

import (
	"testing"
	"time"
)

func TestBuildUserMediaKey(t *testing.T) {
	now := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)

	key := BuildUserMediaKey("video", "files", "user-123", "file-123.mp4", now)

	if key != "video/files/users/user-123/2026/05/file-123.mp4" {
		t.Fatalf("unexpected key: %s", key)
	}
}

func TestBuildMusicUploadKey(t *testing.T) {
	now := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)

	key := BuildMusicUploadKey("audio", "user-123", "file-123.mp3", now)

	if key != "music/audio/uploads/users/user-123/2026/05/file-123.mp3" {
		t.Fatalf("unexpected key: %s", key)
	}
}
