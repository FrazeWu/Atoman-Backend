package service

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func createRevisionTestUser(t *testing.T, db *gorm.DB) uuid.UUID {
	t.Helper()
	userID := uuid.New()
	suffix := strings.ReplaceAll(userID.String(), "-", "")
	user := model.User{
		UUID: userID, Username: "revision-" + suffix[:12], Email: suffix + "@example.test",
		Password: "test", IsActive: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create revision test user: %v", err)
	}
	return userID
}

func TestRevisionContributorsAreDistinctRecentAndSafe(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Revision{})

	first := model.User{
		UUID: uuid.New(), Username: "first", DisplayName: "First", AvatarURL: "https://example.com/first.jpg",
		Email: "first@example.com", Password: "secret",
	}
	second := model.User{
		UUID: uuid.New(), Username: "second", DisplayName: "Second", AvatarURL: "https://example.com/second.jpg",
		Email: "second@example.com", Password: "secret",
	}
	third := model.User{
		UUID: uuid.New(), Username: "pending", Email: "pending@example.com", Password: "secret",
	}
	for _, user := range []*model.User{&first, &second, &third} {
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	}

	contentID := uuid.New()
	baseTime := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	revisions := []model.Revision{
		{ContentType: "album", ContentID: contentID, VersionNumber: 1, ContentSnapshot: []byte(`{"title":"one"}`), EditorID: first.UUID, EditType: "creation", EditSummary: "created", Status: "approved", CreatedAt: baseTime},
		{ContentType: "album", ContentID: contentID, VersionNumber: 2, ContentSnapshot: []byte(`{"title":"two"}`), EditorID: first.UUID, EditType: "edit", EditSummary: "edited", Status: "approved", CreatedAt: baseTime.Add(time.Hour)},
		{ContentType: "album", ContentID: contentID, VersionNumber: 3, ContentSnapshot: []byte(`{"title":"three"}`), EditorID: second.UUID, EditType: "edit", EditSummary: "latest", Status: "approved", CreatedAt: baseTime.Add(2 * time.Hour)},
		{ContentType: "album", ContentID: contentID, VersionNumber: 4, ContentSnapshot: []byte(`{"title":"pending"}`), EditorID: third.UUID, EditType: "edit", EditSummary: "pending", Status: "pending", CreatedAt: baseTime.Add(3 * time.Hour)},
	}
	if err := db.Create(&revisions).Error; err != nil {
		t.Fatalf("create revisions: %v", err)
	}

	service := NewRevisionService(db)
	contributors, total, err := service.GetContributors("album", contentID, 10)
	if err != nil {
		t.Fatalf("get contributors: %v", err)
	}
	if total != 2 || len(contributors) != 2 {
		t.Fatalf("expected 2 approved contributors, total=%d len=%d", total, len(contributors))
	}
	if contributors[0].UserID != second.UUID || contributors[1].UserID != first.UUID {
		t.Fatalf("contributors are not ordered by latest contribution: %#v", contributors)
	}
	if contributors[1].RevisionCount != 2 {
		t.Fatalf("expected first user to have 2 revisions, got %d", contributors[1].RevisionCount)
	}

	history, _, err := service.GetRevisions("album", contentID, 10, 0)
	if err != nil {
		t.Fatalf("get revisions: %v", err)
	}
	raw, err := json.Marshal(history[0])
	if err != nil {
		t.Fatalf("marshal revision DTO: %v", err)
	}
	serialized := string(raw)
	if strings.Contains(serialized, "second@example.com") || strings.Contains(serialized, `"email"`) {
		t.Fatalf("revision response exposed private user fields: %s", serialized)
	}
	if !strings.Contains(serialized, `"content_snapshot":{"title":"pending"}`) {
		t.Fatalf("revision snapshot was not returned as JSON: %s", serialized)
	}
}

func TestCreateRevisionConcurrentAutoApproveKeepsUniqueVersionAndCurrent(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Album{}, &model.Song{}, &model.Revision{}, &model.EditConflict{})

	contentID := uuid.New()
	editorID := createRevisionTestUser(t, db)
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

func TestCreateRevisionDetectsStaleAlbumFieldConflict(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Album{}, &model.Song{}, &model.Revision{}, &model.EditConflict{})
	actorID := createRevisionTestUser(t, db)
	album := model.Album{Title: "base"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	base := model.Revision{
		ContentType: "album", ContentID: album.ID, VersionNumber: 1,
		ContentSnapshot: []byte(`{"album":{"title":"base"},"songs":[]}`),
		EditorID:        actorID, EditSummary: "base", EditType: "creation", Status: "approved", IsCurrent: true,
	}
	if err := db.Create(&base).Error; err != nil {
		t.Fatalf("create base revision: %v", err)
	}

	revisionService := NewRevisionService(db)
	if _, _, err := revisionService.CreateRevision(
		"album", album.ID, actorID, map[string]interface{}{"title": "first"}, "first", 1, true,
	); err != nil {
		t.Fatalf("create first revision: %v", err)
	}
	revision, conflicts, err := revisionService.CreateRevision(
		"album", album.ID, actorID, map[string]interface{}{"title": "second"}, "second", 1, true,
	)
	if err != nil {
		t.Fatalf("detect conflict: %v", err)
	}
	if revision != nil || len(conflicts) != 1 || conflicts[0].FieldName != "title" {
		t.Fatalf("expected title conflict, revision=%#v conflicts=%#v", revision, conflicts)
	}
}

func TestCreateRevisionAutoApproveAppliesArtistChanges(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Artist{}, &model.Revision{})
	actorID := createRevisionTestUser(t, db)

	artist := model.Artist{Name: "Before"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}

	base := model.Revision{
		ContentType:     "artist",
		ContentID:       artist.ID,
		VersionNumber:   1,
		ContentSnapshot: []byte(`{"name":"Before"}`),
		EditorID:        actorID,
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
		actorID,
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
	editorID := createRevisionTestUser(t, db)
	reviewerID := createRevisionTestUser(t, db)
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
	actorID := createRevisionTestUser(t, db)

	contentID := uuid.New()
	current := model.Revision{
		ContentType:     "artist",
		ContentID:       contentID,
		VersionNumber:   1,
		ContentSnapshot: []byte(`{"name":"base"}`),
		EditorID:        actorID,
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
		EditorID:           actorID,
		Status:             "pending",
	}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatalf("create pending revision: %v", err)
	}

	if err := NewRevisionService(db).ApproveRevision(pending.ID, actorID, "approve"); err == nil {
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
	testdb.Migrate(t, db,
		&model.User{}, &model.Album{}, &model.Song{}, &model.Revision{},
		&model.MusicSongLyric{}, &model.MusicSongLyricLine{}, &model.MusicSongLyricVersion{},
		&model.MusicLyricAnnotation{}, &model.MusicLyricAnnotationVote{},
	)

	editorID := uuid.New()
	reviewerID := uuid.New()
	for _, user := range []model.User{
		{UUID: editorID, Username: "revision-editor", Email: "revision-editor@example.com", Password: "hash", IsActive: true},
		{UUID: reviewerID, Username: "revision-reviewer", Email: "revision-reviewer@example.com", Password: "hash", IsActive: true},
	} {
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("create revision user: %v", err)
		}
	}
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
	var renamedLyrics model.MusicSongLyric
	if err := db.First(&renamedLyrics, "song_id = ?", renamed.ID).Error; err != nil {
		t.Fatalf("load renamed song wiki lyrics: %v", err)
	}
	if renamedLyrics.Content != "new words" || renamedLyrics.UpdatedBy != editorID || renamedLyrics.EditSummary != "通过专辑版本更新歌词" {
		t.Fatalf("unexpected renamed song wiki lyrics: %#v", renamedLyrics)
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
	var createdLyrics model.MusicSongLyric
	if err := db.First(&createdLyrics, "song_id = ?", createdSongs[0].ID).Error; err != nil {
		t.Fatalf("load created song wiki lyrics: %v", err)
	}
	if createdLyrics.Content != "brand new lyrics" || createdLyrics.UpdatedBy != editorID {
		t.Fatalf("unexpected created song wiki lyrics: %#v", createdLyrics)
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
	actorID := createRevisionTestUser(t, db)

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
		EditorID:        actorID,
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
		EditorID:           actorID,
		Status:             "pending",
	}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatalf("create pending revision: %v", err)
	}

	if err := NewRevisionService(db).ApproveRevision(pending.ID, actorID, "approve"); err == nil {
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

func TestCreateRevisionBaselineUsesEmptySongsArray(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Album{}, &model.AlbumArtist{}, &model.Song{}, &model.Revision{}, &model.EditConflict{})
	actorID := createRevisionTestUser(t, db)

	album := model.Album{Title: "No Tracks", AlbumType: "album", EntryStatus: "open", Status: "open"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	if _, _, err := NewRevisionService(db).CreateRevision(
		"album", album.ID, actorID, map[string]interface{}{"title": "Still No Tracks"}, "edit", 0, true,
	); err != nil {
		t.Fatalf("create album revision: %v", err)
	}

	var revision model.Revision
	if err := db.Where("content_type = ? AND content_id = ? AND version_number = ?", "album", album.ID, 1).First(&revision).Error; err != nil {
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

func TestCreateRevisionBootstrapsAndAppliesAlbumEditorChanges(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.Artist{}, &model.Album{}, &model.AlbumArtist{}, &model.Song{},
		&model.MusicSongLyric{}, &model.MusicSongLyricLine{}, &model.MusicSongLyricVersion{},
		&model.Revision{}, &model.EditConflict{},
	)
	actorID := createRevisionTestUser(t, db)

	primary := model.Artist{Name: "Primary", EntryStatus: "open"}
	featured := model.Artist{Name: "Featured", EntryStatus: "open"}
	for _, value := range []any{&primary, &featured} {
		if err := db.Create(value).Error; err != nil {
			t.Fatalf("create artist: %v", err)
		}
	}
	album := model.Album{Title: "Before", Description: "Old", AlbumType: "album", EntryStatus: "open", Status: "open"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}

	revisionService := NewRevisionService(db)
	revision, conflicts, err := revisionService.CreateRevision(
		"album", album.ID, actorID,
		map[string]interface{}{
			"title":        "After",
			"description":  "New",
			"release_date": "2026-08-07",
			"artist_credits": []map[string]interface{}{
				{"artist_id": primary.ID.String(), "position": 1, "roles": []map[string]interface{}{{"role": "primary"}}},
				{"artist_id": featured.ID.String(), "position": 2, "roles": []map[string]interface{}{{"role": "featured"}}},
			},
			"tracks": []map[string]interface{}{{"title": "Track", "track_number": 1, "audio_url": "https://example.com/track.mp3"}},
		},
		"编辑专辑", 0, true,
	)
	if err != nil {
		t.Fatalf("create revision: %v", err)
	}
	if len(conflicts) != 0 || revision.VersionNumber != 2 || !revision.IsCurrent {
		t.Fatalf("unexpected revision result: revision=%#v conflicts=%#v", revision, conflicts)
	}

	var revisions []model.Revision
	if err := db.Where("content_type = ? AND content_id = ?", "album", album.ID).Order("version_number").Find(&revisions).Error; err != nil {
		t.Fatalf("load revisions: %v", err)
	}
	if len(revisions) != 2 || revisions[0].EditType != "creation" || revisions[0].IsCurrent || !revisions[1].IsCurrent {
		t.Fatalf("expected baseline and current revision, got %#v", revisions)
	}

	var updated model.Album
	if err := db.Preload("ArtistCredits").Preload("Songs").First(&updated, "id = ?", album.ID).Error; err != nil {
		t.Fatalf("reload album: %v", err)
	}
	if updated.Title != "After" || updated.Description != "New" || updated.ReleaseYear != 2026 || len(updated.ArtistCredits) != 2 || len(updated.Songs) != 1 || updated.Songs[0].AudioSource != "external" {
		t.Fatalf("unexpected updated album: %#v", updated)
	}
	var linkedAlbums []model.Album
	if err := db.Joins("JOIN album_artists ON album_artists.album_id = \"Albums\".id").
		Where("album_artists.artist_id = ?", featured.ID).
		Distinct("\"Albums\".*").
		Find(&linkedAlbums).Error; err != nil {
		t.Fatalf("list albums linked to featured artist: %v", err)
	}
	if len(linkedAlbums) != 1 || linkedAlbums[0].ID != album.ID {
		t.Fatalf("expected revised album in featured artist list, got %#v", linkedAlbums)
	}

	if _, err := revisionService.RevertToRevision("album", album.ID, 1, actorID, "恢复初始版本"); err != nil {
		t.Fatalf("revert album: %v", err)
	}
	if err := db.Preload("ArtistCredits").First(&updated, "id = ?", album.ID).Error; err != nil {
		t.Fatalf("reload reverted album: %v", err)
	}
	if updated.Title != "Before" || !updated.ReleaseDate.IsZero() || updated.Year != 0 || updated.ReleaseYear != 0 || len(updated.ArtistCredits) != 0 {
		t.Fatalf("unexpected reverted album: %#v", updated)
	}
}

func TestRevertAlbumRevisionRestoresSoftDeletedSong(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.Album{}, &model.Song{},
		&model.MusicSongLyric{}, &model.MusicSongLyricLine{}, &model.MusicSongLyricVersion{},
		&model.Revision{}, &model.EditConflict{},
	)

	album := model.Album{Title: "Album", AlbumType: "album", EntryStatus: "open", Status: "open"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	song := model.Song{Title: "Original Track", AudioURL: "/original.mp3", Status: "open", AlbumID: &album.ID}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}

	targetSnapshot, err := json.Marshal(albumRevisionSnapshot{
		Album: &albumRevisionAlbum{Title: album.Title, AlbumType: album.AlbumType, EntryStatus: album.EntryStatus},
		Songs: []albumRevisionSong{{ID: song.ID.String(), Title: song.Title, AudioURL: song.AudioURL, Status: song.Status}},
	})
	if err != nil {
		t.Fatalf("marshal target snapshot: %v", err)
	}
	currentSnapshot, err := json.Marshal(albumRevisionSnapshot{
		Album: &albumRevisionAlbum{Title: album.Title, AlbumType: album.AlbumType, EntryStatus: album.EntryStatus},
		Songs: []albumRevisionSong{},
	})
	if err != nil {
		t.Fatalf("marshal current snapshot: %v", err)
	}
	editorID := createRevisionTestUser(t, db)
	revisions := []model.Revision{
		{ContentType: "album", ContentID: album.ID, VersionNumber: 1, ContentSnapshot: targetSnapshot, EditorID: editorID, EditType: "creation", Status: "approved"},
		{ContentType: "album", ContentID: album.ID, VersionNumber: 2, ContentSnapshot: currentSnapshot, EditorID: editorID, EditType: "edit", Status: "approved", IsCurrent: true},
	}
	if err := db.Create(&revisions).Error; err != nil {
		t.Fatalf("create revisions: %v", err)
	}
	if err := db.Delete(&song).Error; err != nil {
		t.Fatalf("soft delete song: %v", err)
	}

	if _, err := NewRevisionService(db).RevertToRevision("album", album.ID, 1, editorID, "restore track"); err != nil {
		t.Fatalf("revert album revision: %v", err)
	}

	var restored model.Song
	if err := db.Unscoped().First(&restored, "id = ?", song.ID).Error; err != nil {
		t.Fatalf("load restored song: %v", err)
	}
	if restored.DeletedAt.Valid || restored.ID != song.ID || restored.Title != song.Title {
		t.Fatalf("expected original song row to be restored, got %#v", restored)
	}
}

func TestCreateRevisionRejectsProtectedArtistFields(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Artist{}, &model.ArtistMember{}, &model.Revision{}, &model.EditConflict{})
	actorID := createRevisionTestUser(t, db)
	artist := model.Artist{Name: "Artist", EntryStatus: "protected"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}

	if _, _, err := NewRevisionService(db).CreateRevision(
		"artist", artist.ID, actorID, map[string]interface{}{"entry_status": "open"}, "bypass", 0, true,
	); err == nil {
		t.Fatal("expected protected artist field to be rejected")
	}

	var revisionCount int64
	if err := db.Model(&model.Revision{}).Where("content_id = ?", artist.ID).Count(&revisionCount).Error; err != nil {
		t.Fatalf("count revisions: %v", err)
	}
	if revisionCount != 0 {
		t.Fatalf("expected rejected revision transaction to roll back, got %d revisions", revisionCount)
	}
}

func TestSongRevisionAppliesStructuredFieldsCreditsAndReverts(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.Artist{}, &model.Album{}, &model.Song{}, &model.SongArtist{},
		&model.MusicSongLyric{}, &model.MusicSongLyricLine{}, &model.MusicSongLyricVersion{},
		&model.MusicLyricAnnotation{}, &model.MusicLyricAnnotationVote{},
		&model.Revision{}, &model.EditConflict{},
	)
	actorID := createRevisionTestUser(t, db)

	primary := model.Artist{Name: "Primary", EntryStatus: "open"}
	producer := model.Artist{Name: "Producer", EntryStatus: "open"}
	if err := db.Create(&primary).Error; err != nil {
		t.Fatalf("create primary artist: %v", err)
	}
	if err := db.Create(&producer).Error; err != nil {
		t.Fatalf("create producer artist: %v", err)
	}
	song := model.Song{Title: "Before", TrackNumber: 1, DiscNumber: 1, Lyrics: "old", AudioURL: "/old.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	if err := db.Create(&model.SongArtist{SongID: song.ID, ArtistID: primary.ID, Role: "primary", Position: 1}).Error; err != nil {
		t.Fatalf("create initial credit: %v", err)
	}

	revisionService := NewRevisionService(db)
	revision, conflicts, err := revisionService.CreateRevision("song", song.ID, actorID, map[string]interface{}{
		"title":        "After",
		"track_number": 4,
		"disc_number":  2,
		"lyrics":       "new",
		"cover_url":    "https://cdn.example.com/song.webp",
		"artist_credits": []map[string]interface{}{
			{"artist_id": primary.ID.String(), "position": 1, "roles": []map[string]interface{}{{"role": "primary"}, {"role": "vocals"}}},
			{"artist_id": producer.ID.String(), "position": 2, "roles": []map[string]interface{}{{"role": "custom", "label": "执行制作"}}},
		},
	}, "编辑歌曲", 0, true)
	if err != nil {
		t.Fatalf("create song revision: %v", err)
	}
	if len(conflicts) != 0 || revision.VersionNumber != 2 || !revision.IsCurrent {
		t.Fatalf("unexpected revision result: revision=%#v conflicts=%#v", revision, conflicts)
	}

	var updated model.Song
	if err := db.Preload("ArtistCredits").First(&updated, "id = ?", song.ID).Error; err != nil {
		t.Fatalf("reload updated song: %v", err)
	}
	if updated.Title != "After" || updated.TrackNumber != 4 || updated.DiscNumber != 2 || updated.Lyrics != "new" || updated.AudioURL != "/old.mp3" || len(updated.ArtistCredits) != 3 {
		t.Fatalf("unexpected updated song: %#v", updated)
	}

	if _, err := revisionService.RevertToRevision("song", song.ID, 1, actorID, "恢复初始版本"); err != nil {
		t.Fatalf("revert song: %v", err)
	}
	if err := db.Preload("ArtistCredits").First(&updated, "id = ?", song.ID).Error; err != nil {
		t.Fatalf("reload reverted song: %v", err)
	}
	if updated.Title != "Before" || updated.TrackNumber != 1 || updated.DiscNumber != 1 || updated.Lyrics != "old" || updated.AudioURL != "/old.mp3" || len(updated.ArtistCredits) != 1 || updated.ArtistCredits[0].Role != "primary" {
		t.Fatalf("unexpected reverted song: %#v", updated)
	}
}

func TestMergeSongRevisionChangesRejectsInvalidArtistRoles(t *testing.T) {
	current, err := json.Marshal(songRevisionSnapshot{ID: uuid.NewString(), Title: "Song"})
	if err != nil {
		t.Fatal(err)
	}
	artistID := uuid.NewString()
	tests := []struct {
		name  string
		roles []map[string]interface{}
	}{
		{name: "unsupported", roles: []map[string]interface{}{{"role": "dj"}}},
		{name: "empty custom", roles: []map[string]interface{}{{"role": "custom", "label": ""}}},
		{name: "duplicate", roles: []map[string]interface{}{{"role": "producer"}, {"role": "producer"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := mergeSongRevisionChanges(current, map[string]interface{}{
				"artist_credits": []map[string]interface{}{{"artist_id": artistID, "roles": test.roles}},
			})
			if err == nil {
				t.Fatal("expected invalid artist roles to be rejected")
			}
		})
	}
}

func TestCreateRevisionBootstrapsAndAppliesArtistChanges(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Artist{}, &model.ArtistMember{}, &model.Revision{}, &model.EditConflict{})
	actorID := createRevisionTestUser(t, db)
	artist := model.Artist{Name: "Before", Bio: "Old", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	member := model.Artist{Name: "Member", EntryStatus: "open"}
	if err := db.Create(&member).Error; err != nil {
		t.Fatalf("create member: %v", err)
	}

	revisionService := NewRevisionService(db)
	revision, conflicts, err := revisionService.CreateRevision(
		"artist", artist.ID, actorID,
		map[string]interface{}{
			"name": "After", "disambiguation": "Group", "bio": "New", "birth_date": "1990-01-02",
			"artist_form": "group", "active_start_date": "2020", "active_end_date": "2024/12/--", "stage_names_json": `[{"name":"After","is_primary":true}]`,
			"members": []map[string]interface{}{{"artist_id": member.ID.String(), "join_date": "2020/01/--", "leave_date": ""}},
			"sources": []map[string]interface{}{{"type": "url", "url": "https://example.com/artist"}},
		},
		"编辑艺术家", 0, true,
	)
	if err != nil {
		t.Fatalf("create revision: %v", err)
	}
	if len(conflicts) != 0 || revision.VersionNumber != 2 {
		t.Fatalf("unexpected revision result: revision=%#v conflicts=%#v", revision, conflicts)
	}
	var updated model.Artist
	if err := db.First(&updated, "id = ?", artist.ID).Error; err != nil {
		t.Fatalf("reload artist: %v", err)
	}
	if updated.Name != "After" || updated.Disambiguation != "Group" || updated.Bio != "New" || updated.BirthDate == nil || updated.ArtistForm != "group" || updated.ActiveStartDatePrecision != "year" || updated.ActiveEndDatePrecision != "month" {
		t.Fatalf("unexpected updated artist: %#v", updated)
	}
	var relations []model.ArtistMember
	if err := db.Where("group_artist_id = ?", artist.ID).Find(&relations).Error; err != nil || len(relations) != 1 || relations[0].MemberArtistID != member.ID || relations[0].JoinDatePrecision != "month" {
		t.Fatalf("unexpected artist members: relations=%#v err=%v", relations, err)
	}

	if _, err := revisionService.RevertToRevision("artist", artist.ID, 1, actorID, "恢复初始版本"); err != nil {
		t.Fatalf("revert artist: %v", err)
	}
	if err := db.Where("group_artist_id = ?", artist.ID).Find(&relations).Error; err != nil || len(relations) != 0 {
		t.Fatalf("expected members to revert: relations=%#v err=%v", relations, err)
	}
	var reverted model.Artist
	if err := db.First(&reverted, "id = ?", artist.ID).Error; err != nil {
		t.Fatalf("reload reverted artist: %v", err)
	}
	if reverted.Name != "Before" || reverted.BirthDate != nil || !reverted.ActiveStartDate.IsZero() || !reverted.ActiveEndDate.IsZero() {
		t.Fatalf("unexpected reverted artist: %#v", reverted)
	}
}

func TestApplyArtistRevisionSnapshotPreservesEntryStatus(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Artist{})
	artist := model.Artist{Name: "Before", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}

	if err := applyArtistRevisionSnapshot(db, artist.ID, map[string]interface{}{
		"name":            "After",
		"entry_status":    "draft",
		"birth_date":      "----/--/--",
		"active_end_date": "----/--/--",
	}); err != nil {
		t.Fatalf("apply artist revision: %v", err)
	}
	if err := db.First(&artist, "id = ?", artist.ID).Error; err != nil {
		t.Fatalf("reload artist: %v", err)
	}
	if artist.Name != "After" || artist.EntryStatus != "open" || artist.BirthDate != nil || artist.BirthDatePrecision != "unknown" || !artist.ActiveEndDate.IsZero() || artist.ActiveEndDatePrecision != "unknown" {
		t.Fatalf("unexpected artist after revision: %#v", artist)
	}
}
