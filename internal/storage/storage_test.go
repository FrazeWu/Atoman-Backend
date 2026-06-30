package storage

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestDeleteSongAndS3ObjectsNilClientWithS3URLs(t *testing.T) {
	t.Setenv("S3_URL_PREFIX", "https://storage.example.com/music")
	t.Setenv("S3_BUCKET", "unit-test-bucket")

	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Album{}, &model.Song{})

	album := model.Album{Title: "Album"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}

	song := model.Song{
		Title:       "Song",
		AudioURL:    "https://storage.example.com/music/audio/song.mp3",
		AudioSource: "s3",
		CoverURL:    "https://storage.example.com/music/covers/song.jpg",
		CoverSource: "s3",
		AlbumID:     &album.ID,
		Album:       &album,
	}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}

	if err := DeleteSongAndS3Objects(db, nil, &song); err != nil {
		t.Fatalf("DeleteSongAndS3Objects(nil, s3-looking URLs) error = %v, want nil", err)
	}

	var remaining model.Song
	if err := db.First(&remaining, "id = ?", song.ID).Error; err == nil {
		t.Fatalf("expected song to be deleted, still found %+v", remaining)
	}
}

func TestDeleteAlbumAndS3ObjectsNilClientSkipsLocalCover(t *testing.T) {
	t.Setenv("S3_URL_PREFIX", "https://storage.example.com/music")
	t.Setenv("S3_BUCKET", "unit-test-bucket")

	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Album{})

	album := model.Album{
		Title:       "Album",
		CoverURL:    "/uploads/music/artist/album/cover.jpg",
		CoverSource: "local",
	}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}

	if err := DeleteAlbumAndS3Objects(db, nil, &album); err != nil {
		t.Fatalf("DeleteAlbumAndS3Objects(nil, local cover) error = %v, want nil", err)
	}

	var remaining model.Album
	if err := db.First(&remaining, "id = ?", album.ID).Error; err == nil {
		t.Fatalf("expected album to be deleted, still found %+v", remaining)
	}
}

func TestIsLocalUploadURL(t *testing.T) {
	if !isLocalUploadURL("/uploads/music/a/b/c.jpg") {
		t.Fatal("expected uploads path to be local")
	}
	if isLocalUploadURL("https://storage.example.com/music/a/b/c.jpg") {
		t.Fatal("expected https URL to be non-local")
	}
}

func TestS3ObjectKeyFromURL(t *testing.T) {
	t.Setenv("S3_URL_PREFIX", "https://storage.example.com/music")

	key, ok := s3ObjectKeyFromURL("https://storage.example.com/music/audio/song.mp3")
	if !ok {
		t.Fatal("expected S3 URL to yield a key")
	}
	if key != "audio/song.mp3" {
		t.Fatalf("unexpected key: %s", key)
	}

	if key, ok := s3ObjectKeyFromURL("/uploads/music/audio/song.mp3"); ok || key != "" {
		t.Fatalf("expected local upload URL to be skipped, got ok=%v key=%q", ok, key)
	}
}

func TestSaveFileLocallyRejectsFilenameWithPathSeparator(t *testing.T) {
	tmpDir := t.TempDir()
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working dir: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDir); err != nil {
			t.Fatalf("restore working dir: %v", err)
		}
	})

	if _, _, err := SaveFileLocally(bytes.NewBufferString("audio"), "../evil.mp3", "Artist", "Album"); err == nil {
		t.Fatal("expected filename with path separator to be rejected")
	}

	if _, err := os.Stat(filepath.Join(tmpDir, "uploads", "music", "artist", "evil.mp3")); !os.IsNotExist(err) {
		t.Fatalf("expected traversal target not to exist, stat err=%v", err)
	}
}
