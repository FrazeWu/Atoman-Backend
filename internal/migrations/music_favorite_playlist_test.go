package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunMusicFavoritePlaylistMigrationMarksExistingFavoriteAndAddsUniqueIndex(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Playlist{})

	user := model.User{Username: "favorite-migration", Email: "favorite-migration@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	favorite := model.Playlist{UserID: user.UUID, Name: "最爱", IsPublic: true}
	if err := db.Create(&favorite).Error; err != nil {
		t.Fatalf("create favorite playlist: %v", err)
	}

	if err := RunMusicFavoritePlaylistMigration(db); err != nil {
		t.Fatalf("run favorite playlist migration: %v", err)
	}

	var migrated model.Playlist
	if err := db.First(&migrated, "id = ?", favorite.ID).Error; err != nil {
		t.Fatalf("load migrated favorite: %v", err)
	}
	if !migrated.IsFavorite || migrated.IsPublic {
		t.Fatalf("expected private favorite playlist, got %#v", migrated)
	}
	assertIndexExists(t, db, "music_playlists", "idx_music_playlists_user_favorite")

	duplicate := model.Playlist{UserID: user.UUID, Name: "Duplicate", IsFavorite: true}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("expected duplicate favorite playlist to violate unique index")
	}
}

func TestRunMusicFavoritePlaylistMigrationHandlesDuplicateLegacyFavorites(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Playlist{})

	user := model.User{Username: "duplicate-favorite-migration", Email: "duplicate-favorite-migration@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := db.Create(&model.Playlist{UserID: user.UUID, Name: "最爱", IsPublic: true}).Error; err != nil {
			t.Fatalf("create legacy favorite %d: %v", i, err)
		}
	}

	if err := RunMusicFavoritePlaylistMigration(db); err != nil {
		t.Fatalf("run favorite playlist migration: %v", err)
	}

	var favorites []model.Playlist
	if err := db.Where("user_id = ? AND is_favorite = ?", user.UUID, true).Find(&favorites).Error; err != nil {
		t.Fatalf("find migrated favorites: %v", err)
	}
	if len(favorites) != 1 {
		t.Fatalf("expected one canonical favorite playlist, got %d", len(favorites))
	}
	if favorites[0].IsPublic {
		t.Fatal("expected canonical favorite playlist to be private")
	}
}

func TestRunMusicFavoritePlaylistMigrationCreatesFavoriteForExistingUser(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Playlist{})

	user := model.User{Username: "favorite-backfill", Email: "favorite-backfill@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	if err := RunMusicFavoritePlaylistMigration(db); err != nil {
		t.Fatalf("run favorite playlist migration: %v", err)
	}

	var favorites []model.Playlist
	if err := db.Where("user_id = ? AND is_favorite = ?", user.UUID, true).Find(&favorites).Error; err != nil {
		t.Fatalf("find migrated favorites: %v", err)
	}
	if len(favorites) != 1 {
		t.Fatalf("expected one backfilled favorite playlist, got %d", len(favorites))
	}
	if favorites[0].Name != "最爱" || favorites[0].IsPublic {
		t.Fatalf("unexpected backfilled favorite playlist: %#v", favorites[0])
	}
}
