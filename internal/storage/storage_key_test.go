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

func TestBuildUserAvatarSlotKey(t *testing.T) {
	tests := []struct {
		slot string
		want string
	}{
		{slot: UserAvatarOldSlot, want: "users/avatars/user-123/old"},
		{slot: UserAvatarNewSlot, want: "users/avatars/user-123/new"},
	}
	for _, tt := range tests {
		t.Run(tt.slot, func(t *testing.T) {
			if key := BuildUserAvatarSlotKey("user-123", tt.slot); key != tt.want {
				t.Fatalf("unexpected key: %s", key)
			}
		})
	}
}

func TestBuildMusicUploadKey(t *testing.T) {
	now := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)

	key := BuildMusicUploadKey("audio", "user-123", "file-123.mp3", now)

	if key != "music/audio/uploads/users/user-123/2026/05/file-123.mp3" {
		t.Fatalf("unexpected key: %s", key)
	}
}

func TestBuildMusicAlbumCoverKey(t *testing.T) {
	key := BuildMusicAlbumCoverKey("album-123", ".jpg")

	if key != "music/albums/album-123/cover.jpg" {
		t.Fatalf("unexpected key: %s", key)
	}
}

func TestBuildMusicAlbumTrackKey(t *testing.T) {
	key := BuildMusicAlbumTrackKey("album-123", "song-456", ".mp3")

	if key != "music/albums/album-123/tracks/song-456.mp3" {
		t.Fatalf("unexpected key: %s", key)
	}
}

func TestBuildMusicAlbumVersionKeys(t *testing.T) {
	coverKey := BuildMusicAlbumCoverVersionKey("album-123", "asset-789", ".jpg")
	if coverKey != "music/albums/album-123/covers/asset-789.jpg" {
		t.Fatalf("unexpected versioned cover key: %s", coverKey)
	}

	trackKey := BuildMusicAlbumTrackVersionKey("album-123", "song-456", "asset-789", "mp3")
	if trackKey != "music/albums/album-123/tracks/song-456/asset-789.mp3" {
		t.Fatalf("unexpected versioned track key: %s", trackKey)
	}
}

func TestBuildMusicEntityCoverVersionKeys(t *testing.T) {
	if key := BuildMusicArtistImageVersionKey("artist-1", "asset-1", "png"); key != "music/artists/artist-1/images/asset-1.png" {
		t.Fatalf("unexpected artist image key: %s", key)
	}
	if key := BuildMusicPlaylistCoverVersionKey("playlist-1", "asset-1", ".webp"); key != "music/playlists/playlist-1/covers/asset-1.webp" {
		t.Fatalf("unexpected playlist cover key: %s", key)
	}
	if key := BuildMusicSongCoverVersionKey("song-1", "asset-1", ".jpg"); key != "music/songs/song-1/covers/asset-1.jpg" {
		t.Fatalf("unexpected song cover key: %s", key)
	}
	if key := BuildMusicSongAudioVersionKey("song-1", "asset-1", ".flac"); key != "music/songs/song-1/audio/asset-1.flac" {
		t.Fatalf("unexpected song audio key: %s", key)
	}
}
