package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunMusicFavoritePlaylistMigrationMovesBookmarksIntoFavoritePlaylist(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Song{}, &model.SongBookmark{}, &model.Playlist{}, &model.PlaylistSong{}, &model.PlaylistBookmark{})

	user := model.User{Username: "favorite-migration", Email: "favorite-migration@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	song := model.Song{Title: "Saved song", AudioURL: "saved.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	bookmark := model.SongBookmark{UserID: user.UUID, SongID: song.ID}
	if err := db.Create(&bookmark).Error; err != nil {
		t.Fatalf("create song bookmark: %v", err)
	}

	if err := RunMusicFavoritePlaylistMigration(db); err != nil {
		t.Fatalf("run standalone bookmark migration: %v", err)
	}
	if err := RunMusicFavoritePlaylistMigration(db); err != nil {
		t.Fatalf("rerun standalone bookmark migration: %v", err)
	}

	if db.Migrator().HasTable(&model.SongBookmark{}) {
		t.Fatal("expected standalone song bookmarks table to be removed")
	}
	var favorite model.Playlist
	if err := db.First(&favorite, "user_id = ? AND kind = ?", user.UUID, "favorite").Error; err != nil {
		t.Fatalf("load favorite playlist: %v", err)
	}
	if favorite.Name != "最爱" || favorite.IsPublic {
		t.Fatalf("unexpected favorite playlist: %#v", favorite)
	}
	var entry model.PlaylistSong
	if err := db.First(&entry, "playlist_id = ? AND song_id = ?", favorite.ID, song.ID).Error; err != nil {
		t.Fatalf("load migrated favorite song: %v", err)
	}
	if entry.Position != 1 {
		t.Fatalf("expected migrated song at position 1, got %d", entry.Position)
	}
}

func TestRunMusicFavoritePlaylistMigrationCreatesOneSystemPlaylistPerUser(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Playlist{}, &model.PlaylistSong{}, &model.PlaylistBookmark{})
	user := model.User{Username: "standalone-bookmarks", Email: "standalone-bookmarks@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	favorite := model.Playlist{UserID: user.UUID, Name: "旧名称", Kind: "favorite", IsPublic: true}
	if err := db.Create(&favorite).Error; err != nil {
		t.Fatalf("create legacy favorite playlist: %v", err)
	}
	if err := RunMusicFavoritePlaylistMigration(db); err != nil {
		t.Fatalf("run standalone bookmark migration: %v", err)
	}
	var count int64
	if err := db.Model(&model.Playlist{}).Where("user_id = ? AND kind = ?", user.UUID, "favorite").Count(&count).Error; err != nil {
		t.Fatalf("count playlists: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one favorite playlist, got %d", count)
	}
	if err := db.First(&favorite, "id = ?", favorite.ID).Error; err != nil {
		t.Fatalf("load normalized favorite playlist: %v", err)
	}
	if favorite.Name != "最爱" || favorite.IsPublic {
		t.Fatalf("expected normalized favorite playlist, got %#v", favorite)
	}
}
