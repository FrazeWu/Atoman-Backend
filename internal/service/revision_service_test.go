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
	testdb.Migrate(t, db, &model.Revision{}, &model.EditConflict{})

	contentID := uuid.New()
	editorID := uuid.New()
	base := model.Revision{
		ContentType:     "album",
		ContentID:       contentID,
		VersionNumber:   1,
		ContentSnapshot: []byte(`{"title":"base","note":"start"}`),
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
				map[string]interface{}{"title": title},
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

func TestApproveRevisionKeepsOnlyOneCurrentWithUniqueIndex(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Album{}, &model.Revision{})

	contentID := uuid.New()
	editorID := uuid.New()
	reviewerID := uuid.New()
	current := model.Revision{
		ContentType:     "album",
		ContentID:       contentID,
		VersionNumber:   1,
		ContentSnapshot: []byte(`{"title":"base"}`),
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
		ContentSnapshot:    []byte(`{"title":"next"}`),
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
		Base: model.Base{ID: albumID},
		Title: "Before",
		Year:  2024,
		ReleaseDate: releaseDate,
		AlbumType: "album",
		EntryStatus: "open",
		Status: "open",
	}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}

	existingSong := model.Song{
		Base: model.Base{ID: existingSongID},
		Title: "Old Track",
		TrackNumber: 1,
		ReleaseDate: releaseDate,
		AudioURL: "https://cdn.example.com/old.mp3",
		AudioSource: "s3",
		Status: "open",
		AlbumID: &albumID,
	}
	if err := db.Create(&existingSong).Error; err != nil {
		t.Fatalf("create existing song: %v", err)
	}

	deletedSong := model.Song{
		Base: model.Base{ID: uuid.New()},
		Title: "Deleted Track",
		TrackNumber: 2,
		ReleaseDate: releaseDate,
		AudioURL: "https://cdn.example.com/deleted.mp3",
		AudioSource: "s3",
		Status: "open",
		AlbumID: &albumID,
	}
	if err := db.Create(&deletedSong).Error; err != nil {
		t.Fatalf("create deleted song: %v", err)
	}

	baseSnapshot, err := json.Marshal(map[string]interface{}{
		"album": map[string]interface{}{
			"id": albumID.String(),
			"title": "Before",
			"release_date": "2024-09-13",
			"album_type": "album",
			"entry_status": "open",
			"cover_url": "",
		},
		"songs": []map[string]interface{}{
			{
				"id": existingSongID.String(),
				"title": "Old Track",
				"track_number": 1,
				"lyrics": "",
				"audio_url": "https://cdn.example.com/old.mp3",
				"status": "open",
			},
			{
				"id": deletedSong.ID.String(),
				"title": "Deleted Track",
				"track_number": 2,
				"lyrics": "",
				"audio_url": "https://cdn.example.com/deleted.mp3",
				"status": "open",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal base snapshot: %v", err)
	}

	current := model.Revision{
		ContentType: "album",
		ContentID: albumID,
		VersionNumber: 1,
		ContentSnapshot: baseSnapshot,
		EditorID: editorID,
		EditSummary: "base",
		EditType: "creation",
		Status: "approved",
		IsCurrent: true,
	}
	if err := db.Create(&current).Error; err != nil {
		t.Fatalf("create current revision: %v", err)
	}

	nextSnapshot, err := json.Marshal(map[string]interface{}{
		"album": map[string]interface{}{
			"id": albumID.String(),
			"title": "After",
			"release_date": "2024-10-01",
			"album_type": "ep",
			"entry_status": "open",
			"cover_url": "https://cdn.example.com/cover.jpg",
		},
		"songs": []map[string]interface{}{
			{
				"id": existingSongID.String(),
				"title": "Renamed Track",
				"track_number": 3,
				"lyrics": "new words",
				"audio_url": "https://cdn.example.com/renamed.mp3",
				"status": "open",
			},
			{
				"title": "Brand New Track",
				"track_number": 4,
				"lyrics": "brand new lyrics",
				"audio_url": "https://cdn.example.com/new.mp3",
				"status": "open",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal next snapshot: %v", err)
	}

	pending := model.Revision{
		ContentType: "album",
		ContentID: albumID,
		VersionNumber: 2,
		PreviousRevisionID: &current.ID,
		ContentSnapshot: nextSnapshot,
		EditorID: editorID,
		EditSummary: "update album",
		EditType: "edit",
		Status: "pending",
		IsCurrent: false,
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
