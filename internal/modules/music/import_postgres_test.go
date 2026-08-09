package music

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"
)

func TestAlbumImportCommitIsIdempotentInPostgres(t *testing.T) {
	db := testdb.OpenPostgres(t, "music_import")
	if err := db.AutoMigrate(
		&model.User{},
		&model.Artist{},
		&model.ArtistMember{},
		&model.Album{},
		&model.AlbumArtist{},
		&model.Song{},
		&model.SongArtist{},
		&model.AlbumImportSession{},
		&model.AlbumImportFile{},
		&model.AlbumImportJob{},
		&model.Notification{},
		&model.Revision{},
	); err != nil {
		t.Fatalf("migrate music import schema: %v", err)
	}
	userModel := model.User{Username: "postgres-import-user", Email: "postgres-import@example.test", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&userModel).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	user := authctx.CurrentUser{ID: userModel.UUID, Username: userModel.Username, Role: authctx.RoleUser}
	service := NewService(db)
	session, err := service.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusReady})
	if err != nil {
		t.Fatalf("create import session: %v", err)
	}
	seedReadyImportMedia(t, db, session.ID, "https://example.test/album.jpg", "PostgreSQL Track")
	input := CommitAlbumImportSessionInput{
		ArtistSource: "https://example.test/artist",
		AlbumSource:  "https://example.test/album",
		Artist: AlbumImportArtistPayload{
			Name:        "PostgreSQL Artist",
			LegalName:   "PostgreSQL Artist",
			ImageURL:    "https://example.test/artist.jpg",
			Nationality: "US",
			BirthDate:   "1977-06-08",
		},
		Album: AlbumImportAlbumPayload{
			Title:       "PostgreSQL Album",
			CoverURL:    "https://example.test/album.jpg",
			ReleaseDate: "2026-08-09",
			Tracks:      []AlbumImportTrackPayload{{Title: "PostgreSQL Track", TrackNumber: 1}},
		},
	}
	first, err := service.CommitAlbumImportSession(user, session.ID, input)
	if err != nil {
		t.Fatalf("commit import: %v", err)
	}
	second, err := service.CommitAlbumImportSession(user, session.ID, input)
	if err != nil {
		t.Fatalf("repeat import commit: %v", err)
	}
	if first.TargetAlbumID == nil || second.TargetAlbumID == nil || *first.TargetAlbumID != *second.TargetAlbumID {
		t.Fatalf("idempotent commit targets = %#v and %#v", first.TargetAlbumID, second.TargetAlbumID)
	}
	var albums, songs int64
	if err := db.Model(&model.Album{}).Count(&albums).Error; err != nil {
		t.Fatalf("count albums: %v", err)
	}
	if err := db.Model(&model.Song{}).Count(&songs).Error; err != nil {
		t.Fatalf("count songs: %v", err)
	}
	if albums != 1 || songs != 1 {
		t.Fatalf("persisted albums/songs = %d/%d, want 1/1", albums, songs)
	}
}
