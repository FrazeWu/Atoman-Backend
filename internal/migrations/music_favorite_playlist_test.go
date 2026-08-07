package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunMusicFavoritePlaylistMigrationMovesSongsToBookmarksAndDeletesPlaylist(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Song{}, &model.SongBookmark{}, &model.Playlist{}, &model.PlaylistSong{}, &model.PlaylistBookmark{})
	if err := db.Exec("ALTER TABLE music_playlists ADD COLUMN is_favorite boolean NOT NULL DEFAULT false").Error; err != nil {
		t.Fatalf("add legacy favorite column: %v", err)
	}

	user := model.User{Username: "favorite-migration", Email: "favorite-migration@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	song := model.Song{Title: "Saved song", AudioURL: "saved.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	favorite := model.Playlist{UserID: user.UUID, Name: "最爱", Kind: "favorite"}
	if err := db.Create(&favorite).Error; err != nil {
		t.Fatalf("create favorite playlist: %v", err)
	}
	if err := db.Model(&model.Playlist{}).Where("id = ?", favorite.ID).Update("is_favorite", true).Error; err != nil {
		t.Fatalf("mark legacy favorite playlist: %v", err)
	}
	if err := db.Create(&model.PlaylistSong{PlaylistID: favorite.ID, SongID: song.ID, Position: 1}).Error; err != nil {
		t.Fatalf("create favorite playlist song: %v", err)
	}
	if err := db.Delete(&model.PlaylistSong{}, "playlist_id = ?", favorite.ID).Error; err != nil {
		t.Fatalf("soft delete favorite playlist song: %v", err)
	}
	if err := db.Create(&model.PlaylistBookmark{UserID: user.UUID, PlaylistID: favorite.ID}).Error; err != nil {
		t.Fatalf("create favorite playlist bookmark: %v", err)
	}

	if err := RunMusicFavoritePlaylistMigration(db); err != nil {
		t.Fatalf("run standalone bookmark migration: %v", err)
	}
	if err := RunMusicFavoritePlaylistMigration(db); err != nil {
		t.Fatalf("rerun standalone bookmark migration: %v", err)
	}

	var bookmark model.SongBookmark
	if err := db.First(&bookmark, "user_id = ? AND song_id = ?", user.UUID, song.ID).Error; err != nil {
		t.Fatalf("load migrated song bookmark: %v", err)
	}
	for name, value := range map[string]any{
		"playlist":          &model.Playlist{},
		"playlist song":     &model.PlaylistSong{},
		"playlist bookmark": &model.PlaylistBookmark{},
	} {
		var count int64
		if err := db.Unscoped().Model(value).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != 0 {
			t.Fatalf("expected legacy %s to be deleted, got %d", name, count)
		}
	}
	if db.Migrator().HasColumn(&model.Playlist{}, "is_favorite") {
		t.Fatal("expected legacy is_favorite column to be removed")
	}
}

func TestRunMusicFavoritePlaylistMigrationDoesNotCreateSystemPlaylist(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.SongBookmark{}, &model.Playlist{})
	user := model.User{Username: "standalone-bookmarks", Email: "standalone-bookmarks@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := RunMusicFavoritePlaylistMigration(db); err != nil {
		t.Fatalf("run standalone bookmark migration: %v", err)
	}
	var count int64
	if err := db.Model(&model.Playlist{}).Where("user_id = ?", user.UUID).Count(&count).Error; err != nil {
		t.Fatalf("count playlists: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no default music playlist, got %d", count)
	}
}
