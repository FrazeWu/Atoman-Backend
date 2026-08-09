package migrationrunner

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/service"
	"atoman/internal/testdb"
)

func TestRunMusicRevisionBaselinesMigrationBackfillsOnce(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Artist{}, &model.Album{}, &model.AlbumArtist{},
		&model.Song{}, &model.SongArtist{}, &model.Revision{},
	)
	user := model.User{Username: "revision-owner", Email: "revision@example.test", Password: "hash"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	createdAt := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)
	artist := model.Artist{Base: model.Base{CreatedAt: createdAt}, Name: "Artist", CreatedBy: &user.UUID}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	album := model.Album{Base: model.Base{CreatedAt: createdAt}, Title: "Album", UploadedBy: &user.UUID}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	song := model.Song{Base: model.Base{CreatedAt: createdAt}, Title: "Song", AlbumID: &album.ID, AudioURL: "/song.mp3", UploadedBy: &user.UUID}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}

	for run := 1; run <= 2; run++ {
		if err := runMusicRevisionBaselinesMigration(db); err != nil {
			t.Fatalf("run migration %d: %v", run, err)
		}
	}

	var revisions []model.Revision
	if err := db.Order("content_type ASC").Find(&revisions).Error; err != nil {
		t.Fatalf("find revisions: %v", err)
	}
	if len(revisions) != 3 {
		t.Fatalf("revision count = %d, want 3", len(revisions))
	}
	for _, revision := range revisions {
		if revision.VersionNumber != 1 || revision.EditType != "creation" || revision.Status != "approved" || !revision.IsCurrent {
			t.Fatalf("unexpected baseline revision: %+v", revision)
		}
		if !revision.CreatedAt.Equal(createdAt) {
			t.Fatalf("revision created_at = %v, want %v", revision.CreatedAt, createdAt)
		}
	}
}

func TestRunMusicRevisionBaselinesMigrationPreservesExistingHistory(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Artist{}, &model.Revision{})
	user := model.User{Username: "revision-editor", Email: "editor@example.test", Password: "hash"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	artist := model.Artist{Name: "Edited Artist", CreatedBy: &user.UUID}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	revisions := service.NewRevisionService(db)
	if _, err := revisions.EnsureInitialRevision("artist", artist.ID, user.UUID); err != nil {
		t.Fatalf("create initial revision: %v", err)
	}
	if _, conflicts, err := revisions.CreateRevision("artist", artist.ID, user.UUID, map[string]interface{}{"bio": "updated"}, "update", 1, true); err != nil || len(conflicts) != 0 {
		t.Fatalf("create second revision: conflicts=%v err=%v", conflicts, err)
	}

	if err := runMusicRevisionBaselinesMigration(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}
	var count int64
	if err := db.Model(&model.Revision{}).Where("content_type = ? AND content_id = ?", "artist", artist.ID).Count(&count).Error; err != nil {
		t.Fatalf("count revisions: %v", err)
	}
	if count != 2 {
		t.Fatalf("revision count = %d, want 2", count)
	}
}
