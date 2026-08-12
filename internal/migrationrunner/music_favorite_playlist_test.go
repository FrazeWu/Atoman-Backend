package migrationrunner

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

type legacyMusicSongBookmark struct {
	model.Base
	UserID uuid.UUID
	SongID uuid.UUID
}

func (legacyMusicSongBookmark) TableName() string { return "music_song_bookmarks" }

func TestRunMusicFavoritePlaylistMigrationConvertsLegacyBookmarks(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Song{}, &legacyMusicSongBookmark{},
		&model.Playlist{}, &model.PlaylistSong{}, &model.PlaylistBookmark{},
	)
	user := model.User{Username: "legacy-favorite", Email: "legacy-favorite@example.test", Password: "hash"}
	song := model.Song{Title: "Bookmarked", AudioURL: "/song.mp3"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	if err := db.Create(&legacyMusicSongBookmark{UserID: user.UUID, SongID: song.ID}).Error; err != nil {
		t.Fatalf("create legacy bookmark: %v", err)
	}

	if err := runMusicFavoritePlaylistMigration(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}
	var playlist model.Playlist
	if err := db.First(&playlist, "user_id = ? AND kind = ?", user.UUID, "favorite").Error; err != nil {
		t.Fatalf("find favorite playlist: %v", err)
	}
	var songs int64
	if err := db.Model(&model.PlaylistSong{}).Where("playlist_id = ? AND song_id = ?", playlist.ID, song.ID).Count(&songs).Error; err != nil || songs != 1 {
		t.Fatalf("expected one migrated song, got %d err=%v", songs, err)
	}
}
