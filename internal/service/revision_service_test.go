package service

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestCreateRevisionConcurrentAutoApproveKeepsUniqueVersionAndCurrent(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Album{}, &model.Song{}, &model.Revision{}, &model.EditConflict{})

	contentID := uuid.New()
	editorID := uuid.New()
	if err := db.Create(&model.Album{Base: model.Base{ID: contentID}, Title: "base"}).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	base := model.Revision{
		ContentType:     "album",
		ContentID:       contentID,
		VersionNumber:   1,
		ContentSnapshot: []byte(`{"album":{"title":"base"},"songs":[]}`),
		EditorID:        editorID,
		EditSummary:     "base",
		EditType:        "creation",
		Status:          "approved",
		IsCurrent:       true,
	}
	if err := db.Create(&base).Error; err != nil {
		t.Fatalf("create base revision: %v", err)
	}

	service := NewRevisionService(db)
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)

	for _, title := range []string{"first", "second"} {
		title := title
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, conflicts, err := service.CreateRevision(
				"album",
				contentID,
				editorID,
				map[string]interface{}{
					"album": map[string]interface{}{"title": title},
					"songs": []interface{}{},
				},
				title,
				1,
				true,
			)
			if len(conflicts) > 0 {
				return
			}
			errs <- err
		}()
	}

	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly 1 concurrent edit to create a revision, got %d", successes)
	}

	assertSingleCurrentAndUniqueVersions(t, db, contentID)
}

func TestCreateRevisionAutoApproveAppliesArtistChanges(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Artist{}, &model.Revision{})

	artist := model.Artist{Name: "Before"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}

	base := model.Revision{
		ContentType:     "artist",
		ContentID:       artist.ID,
		VersionNumber:   1,
		ContentSnapshot: []byte(`{"name":"Before"}`),
		EditorID:        uuid.New(),
		EditSummary:     "base",
		EditType:        "creation",
		Status:          "approved",
		IsCurrent:       true,
	}
	if err := db.Create(&base).Error; err != nil {
		t.Fatalf("create base revision: %v", err)
	}

	if _, _, err := NewRevisionService(db).CreateRevision(
		"artist",
		artist.ID,
		uuid.New(),
		map[string]interface{}{"name": "After"},
		"rename",
		1,
		true,
	); err != nil {
		t.Fatalf("create auto-approved revision: %v", err)
	}

	var reloaded model.Artist
	if err := db.First(&reloaded, "id = ?", artist.ID).Error; err != nil {
		t.Fatalf("reload artist: %v", err)
	}
	if reloaded.Name != "After" {
		t.Fatalf("expected auto-approved revision to update artist, got %q", reloaded.Name)
	}
}

func TestApproveRevisionKeepsOnlyOneCurrentWithUniqueIndex(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Album{}, &model.Song{}, &model.Revision{})

	contentID := uuid.New()
	editorID := uuid.New()
	reviewerID := uuid.New()
	album := model.Album{Base: model.Base{ID: contentID}, Title: "base"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	current := model.Revision{
		ContentType:     "album",
		ContentID:       contentID,
		VersionNumber:   1,
		ContentSnapshot: []byte(`{"album":{"title":"base"},"songs":[]}`),
		EditorID:        editorID,
		EditSummary:     "base",
		EditType:        "creation",
		Status:          "approved",
		IsCurrent:       true,
	}
	if err := db.Create(&current).Error; err != nil {
		t.Fatalf("create current revision: %v", err)
	}

	pending := model.Revision{
		ContentType:        "album",
		ContentID:          contentID,
		VersionNumber:      2,
		PreviousRevisionID: &current.ID,
		ContentSnapshot:    []byte(`{"album":{"title":"next"},"songs":[]}`),
		EditorID:           editorID,
		EditSummary:        "next",
		EditType:           "edit",
		Status:             "pending",
		IsCurrent:          false,
	}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatalf("create pending revision: %v", err)
	}

	service := NewRevisionService(db)
	if err := service.ApproveRevision(pending.ID, reviewerID, "ok"); err != nil {
		t.Fatalf("approve revision: %v", err)
	}

	assertSingleCurrentAndUniqueVersions(t, db, contentID)

	var approved model.Revision
	if err := db.First(&approved, "id = ?", pending.ID).Error; err != nil {
		t.Fatalf("load approved revision: %v", err)
	}
	if approved.Status != "approved" || !approved.IsCurrent {
		t.Fatalf("expected pending revision to be approved current, got status=%q current=%v", approved.Status, approved.IsCurrent)
	}
}

func TestApproveArtistRevisionRollsBackWhenTargetDoesNotExist(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Artist{}, &model.Revision{})

	contentID := uuid.New()
	current := model.Revision{
		ContentType:     "artist",
		ContentID:       contentID,
		VersionNumber:   1,
		ContentSnapshot: []byte(`{"name":"base"}`),
		EditorID:        uuid.New(),
		Status:          "approved",
		IsCurrent:       true,
	}
	if err := db.Create(&current).Error; err != nil {
		t.Fatalf("create current revision: %v", err)
	}
	pending := model.Revision{
		ContentType:        "artist",
		ContentID:          contentID,
		VersionNumber:      2,
		PreviousRevisionID: &current.ID,
		ContentSnapshot:    []byte(`{"name":"next"}`),
		EditorID:           uuid.New(),
		Status:             "pending",
	}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatalf("create pending revision: %v", err)
	}

	if err := NewRevisionService(db).ApproveRevision(pending.ID, uuid.New(), "approve"); err == nil {
		t.Fatal("expected approval to fail when target artist does not exist")
	}

	var reloaded model.Revision
	if err := db.First(&reloaded, "id = ?", pending.ID).Error; err != nil {
		t.Fatalf("reload pending revision: %v", err)
	}
	if reloaded.Status != "pending" || reloaded.IsCurrent {
		t.Fatalf("expected approval transaction to roll back, got status=%q current=%v", reloaded.Status, reloaded.IsCurrent)
	}
}

func assertSingleCurrentAndUniqueVersions(t *testing.T, db *gorm.DB, contentID uuid.UUID) {
	t.Helper()

	var currentCount int64
	if err := db.Model(&model.Revision{}).
		Where("content_type = ? AND content_id = ? AND is_current = ?", "album", contentID, true).
		Count(&currentCount).Error; err != nil {
		t.Fatalf("count current revisions: %v", err)
	}
	if currentCount != 1 {
		t.Fatalf("expected 1 current revision, got %d", currentCount)
	}

	var rows []struct {
		VersionNumber int
		Count         int64
	}
	if err := db.Model(&model.Revision{}).
		Select("version_number, count(*) as count").
		Where("content_type = ? AND content_id = ?", "album", contentID).
		Group("version_number").
		Having("count(*) > 1").
		Scan(&rows).Error; err != nil {
		t.Fatalf("query duplicate versions: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected unique revision versions, got duplicates: %+v", rows)
	}
}

func TestApproveAlbumRevisionAppliesSongCollectionSnapshot(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Album{}, &model.Song{}, &model.Revision{})

	editorID := uuid.New()
	reviewerID := uuid.New()
	albumID := uuid.New()
	existingSongID := uuid.New()
	releaseDate := time.Date(2024, 9, 13, 0, 0, 0, 0, time.UTC)

	album := model.Album{
		Base:        model.Base{ID: albumID},
		Title:       "Before",
		Year:        2024,
		ReleaseDate: releaseDate,
		AlbumType:   "album",
		EntryStatus: "open",
		Status:      "open",
	}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}

	existingSong := model.Song{
		Base:        model.Base{ID: existingSongID},
		Title:       "Old Track",
		TrackNumber: 1,
		ReleaseDate: releaseDate,
		AudioURL:    "https://cdn.example.com/old.mp3",
		AudioSource: "s3",
		Status:      "open",
		AlbumID:     &albumID,
	}
	if err := db.Create(&existingSong).Error; err != nil {
		t.Fatalf("create existing song: %v", err)
	}

	deletedSong := model.Song{
		Base:        model.Base{ID: uuid.New()},
		Title:       "Deleted Track",
		TrackNumber: 2,
		ReleaseDate: releaseDate,
		AudioURL:    "https://cdn.example.com/deleted.mp3",
		AudioSource: "s3",
		Status:      "open",
		AlbumID:     &albumID,
	}
	if err := db.Create(&deletedSong).Error; err != nil {
		t.Fatalf("create deleted song: %v", err)
	}

	baseSnapshot, err := json.Marshal(map[string]interface{}{
		"album": map[string]interface{}{
			"id":           albumID.String(),
			"title":        "Before",
			"release_date": "2024-09-13",
			"album_type":   "album",
			"entry_status": "open",
			"cover_url":    "",
		},
		"songs": []map[string]interface{}{
			{
				"id":           existingSongID.String(),
				"title":        "Old Track",
				"track_number": 1,
				"lyrics":       "",
				"audio_url":    "https://cdn.example.com/old.mp3",
				"status":       "open",
			},
			{
				"id":           deletedSong.ID.String(),
				"title":        "Deleted Track",
				"track_number": 2,
				"lyrics":       "",
				"audio_url":    "https://cdn.example.com/deleted.mp3",
				"status":       "open",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal base snapshot: %v", err)
	}

	current := model.Revision{
		ContentType:     "album",
		ContentID:       albumID,
		VersionNumber:   1,
		ContentSnapshot: baseSnapshot,
		EditorID:        editorID,
		EditSummary:     "base",
		EditType:        "creation",
		Status:          "approved",
		IsCurrent:       true,
	}
	if err := db.Create(&current).Error; err != nil {
		t.Fatalf("create current revision: %v", err)
	}

	nextSnapshot, err := json.Marshal(map[string]interface{}{
		"album": map[string]interface{}{
			"id":           albumID.String(),
			"title":        "After",
			"release_date": "2024-10-01",
			"album_type":   "ep",
			"entry_status": "open",
			"cover_url":    "https://cdn.example.com/cover.jpg",
		},
		"songs": []map[string]interface{}{
			{
				"id":           existingSongID.String(),
				"title":        "Renamed Track",
				"track_number": 3,
				"lyrics":       "new words",
				"audio_url":    "https://cdn.example.com/renamed.mp3",
				"status":       "open",
			},
			{
				"title":        "Brand New Track",
				"track_number": 4,
				"lyrics":       "brand new lyrics",
				"audio_url":    "https://cdn.example.com/new.mp3",
				"status":       "open",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal next snapshot: %v", err)
	}

	pending := model.Revision{
		ContentType:        "album",
		ContentID:          albumID,
		VersionNumber:      2,
		PreviousRevisionID: &current.ID,
		ContentSnapshot:    nextSnapshot,
		EditorID:           editorID,
		EditSummary:        "update album",
		EditType:           "edit",
		Status:             "pending",
		IsCurrent:          false,
	}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatalf("create pending revision: %v", err)
	}

	service := NewRevisionService(db)
	if err := service.ApproveRevision(pending.ID, reviewerID, "apply"); err != nil {
		t.Fatalf("approve revision: %v", err)
	}

	var updatedAlbum model.Album
	if err := db.First(&updatedAlbum, "id = ?", albumID).Error; err != nil {
		t.Fatalf("load album: %v", err)
	}
	if updatedAlbum.Title != "After" {
		t.Fatalf("expected album title updated, got %q", updatedAlbum.Title)
	}
	if updatedAlbum.AlbumType != "ep" {
		t.Fatalf("expected album type updated, got %q", updatedAlbum.AlbumType)
	}
	if updatedAlbum.CoverURL != "https://cdn.example.com/cover.jpg" {
		t.Fatalf("expected album cover updated, got %q", updatedAlbum.CoverURL)
	}
	if got := updatedAlbum.ReleaseDate.Format("2006-01-02"); got != "2024-10-01" {
		t.Fatalf("expected release date updated, got %q", got)
	}

	var renamed model.Song
	if err := db.First(&renamed, "id = ?", existingSongID).Error; err != nil {
		t.Fatalf("load renamed song: %v", err)
	}
	if renamed.Title != "Renamed Track" || renamed.TrackNumber != 3 || renamed.AudioURL != "https://cdn.example.com/renamed.mp3" {
		t.Fatalf("expected existing song updated, got %+v", renamed)
	}
	if renamed.Lyrics != "new words" {
		t.Fatalf("expected song lyrics updated, got %q", renamed.Lyrics)
	}

	var createdSongs []model.Song
	if err := db.Where("album_id = ? AND title = ?", albumID, "Brand New Track").Find(&createdSongs).Error; err != nil {
		t.Fatalf("load created songs: %v", err)
	}
	if len(createdSongs) != 1 {
		t.Fatalf("expected 1 new song, got %d", len(createdSongs))
	}
	if createdSongs[0].TrackNumber != 4 || createdSongs[0].AudioURL != "https://cdn.example.com/new.mp3" {
		t.Fatalf("expected new song fields saved, got %+v", createdSongs[0])
	}

	var closedSong model.Song
	if err := db.First(&closedSong, "id = ?", deletedSong.ID).Error; err != nil {
		t.Fatalf("load closed song: %v", err)
	}
	if closedSong.Status != "closed" {
		t.Fatalf("expected missing snapshot song to be closed, got %q", closedSong.Status)
	}
}

func TestApproveAlbumRevisionRejectsFlatSnapshot(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Album{}, &model.Song{}, &model.Revision{})

	album := model.Album{Title: "Before", AlbumType: "album", EntryStatus: "open", Status: "open"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	song := model.Song{Title: "Existing Track", Status: "open", AlbumID: &album.ID}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}

	current := model.Revision{
		ContentType:     "album",
		ContentID:       album.ID,
		VersionNumber:   1,
		ContentSnapshot: []byte(`{"album":{"title":"Before"},"songs":[{"id":"` + song.ID.String() + `","title":"Existing Track"}]}`),
		EditorID:        uuid.New(),
		Status:          "approved",
		IsCurrent:       true,
	}
	if err := db.Create(&current).Error; err != nil {
		t.Fatalf("create current revision: %v", err)
	}
	pending := model.Revision{
		ContentType:        "album",
		ContentID:          album.ID,
		VersionNumber:      2,
		PreviousRevisionID: &current.ID,
		ContentSnapshot:    []byte(`{"title":"Legacy Flat Snapshot"}`),
		EditorID:           uuid.New(),
		Status:             "pending",
	}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatalf("create pending revision: %v", err)
	}

	if err := NewRevisionService(db).ApproveRevision(pending.ID, uuid.New(), "approve"); err == nil {
		t.Fatal("expected flat album snapshot approval to fail")
	}

	var reloadedRevision model.Revision
	if err := db.First(&reloadedRevision, "id = ?", pending.ID).Error; err != nil {
		t.Fatalf("reload revision: %v", err)
	}
	if reloadedRevision.Status != "pending" || reloadedRevision.IsCurrent {
		t.Fatalf("expected revision approval to roll back, got status=%q current=%v", reloadedRevision.Status, reloadedRevision.IsCurrent)
	}

	var reloadedSong model.Song
	if err := db.First(&reloadedSong, "id = ?", song.ID).Error; err != nil {
		t.Fatalf("reload song: %v", err)
	}
	if reloadedSong.Status != "open" {
		t.Fatalf("expected existing song to remain open, got %q", reloadedSong.Status)
	}
}

func TestCreateAlbumSnapshotUsesEmptySongsArray(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Album{}, &model.Song{}, &model.Revision{})

	album := model.Album{Title: "No Tracks", AlbumType: "album", EntryStatus: "open", Status: "open"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	if err := NewRevisionService(db).CreateAlbumSnapshot(album.ID, uuid.New(), "snapshot", db); err != nil {
		t.Fatalf("create album snapshot: %v", err)
	}

	var revision model.Revision
	if err := db.Where("content_type = ? AND content_id = ?", "album", album.ID).First(&revision).Error; err != nil {
		t.Fatalf("load album snapshot: %v", err)
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(revision.ContentSnapshot, &snapshot); err != nil {
		t.Fatalf("parse album snapshot: %v", err)
	}
	if string(snapshot["songs"]) != "[]" {
		t.Fatalf("expected empty songs array, got %s", snapshot["songs"])
	}
}
