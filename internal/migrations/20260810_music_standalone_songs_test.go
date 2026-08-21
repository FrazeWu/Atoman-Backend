package migrations

import (
	"errors"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestRunMusicStandaloneSongsMigrationPreservesSongAndRemovesReleaseWrapper(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Artist{}, &model.Album{}, &model.AlbumArtist{},
		&model.Song{}, &model.SongArtist{}, &model.AlbumBookmark{}, &model.AlbumImportSession{},
		&model.Revision{},
	)

	actor := model.User{Username: "migration-actor", Email: "migration-actor@example.test", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&actor).Error; err != nil {
		t.Fatalf("create actor: %v", err)
	}
	artist := model.Artist{Name: "Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	releaseDate := time.Date(2025, time.January, 2, 0, 0, 0, 0, time.UTC)
	release := model.Album{
		Title: "So Called Friends", Description: "Optional description", AlbumType: "leak",
		ReleaseDate: releaseDate, ReleaseDatePrecision: "day", CoverURL: "https://cdn.test/cover.jpg",
		CoverSource: "external", SourcesJSON: `[{"type":"url","url":"https://example.test/source"}]`,
		Status: "open", EntryStatus: "open", LifecycleStatus: model.MusicLifecycleActive,
	}
	if err := db.Create(&release).Error; err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := db.Create(&model.AlbumArtist{AlbumID: release.ID, ArtistID: artist.ID, Role: "primary", Position: 1}).Error; err != nil {
		t.Fatalf("create album credit: %v", err)
	}
	song := model.Song{
		Title: "SO CALLED FRIENDS", AlbumID: &release.ID, TrackNumber: 7, DiscNumber: 2,
		AudioURL: "https://cdn.test/song.mp3", Status: "open", LifecycleStatus: model.MusicLifecycleActive,
	}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	session := model.AlbumImportSession{TargetAlbumID: &release.ID, PayloadJSON: `{}`}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("create import session: %v", err)
	}
	if err := db.Create(&model.AlbumBookmark{UserID: uuid.New(), AlbumID: release.ID}).Error; err != nil {
		t.Fatalf("create album bookmark: %v", err)
	}
	if err := db.Create(&model.Revision{
		ContentType: "album", ContentID: release.ID, VersionNumber: 1,
		ContentSnapshot: []byte(`{"album":{"title":"So Called Friends"},"songs":[]}`),
		EditorID:        actor.UUID, Status: "approved", IsCurrent: true,
	}).Error; err != nil {
		t.Fatalf("create album revision: %v", err)
	}

	bully := model.Album{Base: model.Base{ID: bullyAlbumID}, Title: "BULLY", AlbumType: "single", Status: "open", EntryStatus: "open", LifecycleStatus: model.MusicLifecycleActive}
	if err := db.Create(&bully).Error; err != nil {
		t.Fatalf("create BULLY: %v", err)
	}
	for _, title := range []string{"Track One", "Track Two"} {
		row := model.Song{Title: title, AlbumID: &bully.ID, AudioURL: "/" + title + ".mp3", Status: "open", LifecycleStatus: model.MusicLifecycleActive}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("create BULLY track: %v", err)
		}
	}

	if err := RunMusicStandaloneSongsPreSchemaMigration(db); err != nil {
		t.Fatalf("run standalone migration: %v", err)
	}
	if err := RunMusicStandaloneSongsPreSchemaMigration(db); err != nil {
		t.Fatalf("rerun standalone migration: %v", err)
	}

	var migrated model.Song
	if err := db.First(&migrated, "id = ?", song.ID).Error; err != nil {
		t.Fatalf("load migrated song: %v", err)
	}
	if migrated.AlbumID != nil || standaloneReleaseType(migrated.ReleaseType) != "leak" {
		t.Fatalf("unexpected ownership after migration: album=%v type=%v", migrated.AlbumID, migrated.ReleaseType)
	}
	if migrated.Title != "So Called Friends" || migrated.Description != release.Description || migrated.CoverURL != release.CoverURL {
		t.Fatalf("unexpected migrated metadata: %#v", migrated)
	}
	if !migrated.ReleaseDate.Equal(releaseDate) || migrated.TrackNumber != 1 || migrated.DiscNumber != 1 {
		t.Fatalf("unexpected migrated date/order: %#v", migrated)
	}
	if len(migrated.Sources) != 1 || migrated.Sources[0].URL != "https://example.test/source" {
		t.Fatalf("unexpected migrated sources: %#v", migrated.Sources)
	}
	var credits []model.SongArtist
	if err := db.Where("song_id = ?", song.ID).Find(&credits).Error; err != nil {
		t.Fatalf("load song credits: %v", err)
	}
	if len(credits) != 1 || credits[0].ArtistID != artist.ID || credits[0].Role != "primary" {
		t.Fatalf("unexpected migrated credits: %#v", credits)
	}
	var migratedSession model.AlbumImportSession
	if err := db.First(&migratedSession, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("load migrated session: %v", err)
	}
	if migratedSession.TargetAlbumID != nil || migratedSession.TargetSongID == nil || *migratedSession.TargetSongID != song.ID {
		t.Fatalf("unexpected migrated import target: %#v", migratedSession)
	}
	if err := db.Unscoped().First(&model.Album{}, "id = ?", release.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected release wrapper deletion, got %v", err)
	}
	var bookmarkCount int64
	if err := db.Unscoped().Model(&model.AlbumBookmark{}).Where("album_id = ?", release.ID).Count(&bookmarkCount).Error; err != nil || bookmarkCount != 0 {
		t.Fatalf("expected bookmark cleanup, count=%d err=%v", bookmarkCount, err)
	}
	var revisionCount int64
	if err := db.Unscoped().Model(&model.Revision{}).Where("content_type = ? AND content_id = ?", "album", release.ID).Count(&revisionCount).Error; err != nil || revisionCount != 0 {
		t.Fatalf("expected album revision cleanup, count=%d err=%v", revisionCount, err)
	}
	var migratedBully model.Album
	if err := db.First(&migratedBully, "id = ?", bully.ID).Error; err != nil {
		t.Fatalf("load BULLY: %v", err)
	}
	if migratedBully.AlbumType != "album" {
		t.Fatalf("expected BULLY album type, got %q", migratedBully.AlbumType)
	}
	if !db.Migrator().HasColumn(&model.Song{}, "ReleaseType") {
		t.Fatal("expected release_type column before constraints")
	}
	if db.Dialector.Name() != "postgres" {
		t.Fatalf("expected postgres test database, got %q", db.Dialector.Name())
	}
	if err := RunMusicStandaloneSongsConstraintsMigration(db); err != nil {
		t.Fatalf("install standalone constraints: %v", err)
	}
	untyped := model.Song{Title: "Untyped", AudioURL: "/untyped.mp3", Status: "open", LifecycleStatus: model.MusicLifecycleActive}
	if err := db.Create(&untyped).Error; err == nil {
		t.Fatal("expected untyped standalone song to violate ownership constraint")
	}
}

func TestValidateStandaloneSongOwnership(t *testing.T) {
	single := "single"
	albumID := uuid.New()
	cases := []struct {
		name    string
		song    model.Song
		wantErr bool
	}{
		{name: "standalone single", song: model.Song{ReleaseType: &single}},
		{name: "untyped standalone", song: model.Song{}, wantErr: true},
		{name: "album track", song: model.Song{AlbumID: &albumID}},
		{name: "album track with type", song: model.Song{AlbumID: &albumID, ReleaseType: &single}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateStandaloneSongOwnership(tc.song)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate error = %v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
