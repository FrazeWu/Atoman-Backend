package music

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"gorm.io/gorm"
)

func newMusicTestService(t *testing.T) (*Service, *gorm.DB, authctx.CurrentUser) {
	t.Helper()

	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Artist{},
		&model.ArtistAlias{},
		&model.ArtistMerge{},
		&model.Album{},
		&model.Song{},
		&model.ArtistBookmark{},
		&model.AlbumBookmark{},
		&model.SongBookmark{},
		&model.Playlist{},
		&model.PlaylistSong{},
		&model.AlbumImportSession{},
		&model.MusicEdit{},
		&model.MusicEditVote{},
		&model.MusicEditDecision{},
		&model.MusicEditChange{},
		&model.AuditLog{},
	)

	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	return NewService(db), db, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: authctx.RoleUser}
}

func createModerator(t *testing.T, db *gorm.DB) authctx.CurrentUser {
	t.Helper()
	moderatorModel := model.User{Username: "mod", Email: "mod@example.com", Password: "hash", Role: authctx.RoleModerator, IsActive: true}
	if err := db.Create(&moderatorModel).Error; err != nil {
		t.Fatalf("create moderator: %v", err)
	}
	return authctx.CurrentUser{ID: moderatorModel.UUID, Username: moderatorModel.Username, Role: authctx.RoleModerator}
}

func TestApproveEditOnlyAllowsOneDecision(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	moderator := createModerator(t, db)
	edit := model.MusicEdit{
		Type:        "create_artist",
		EntityType:  "artist",
		SubmittedBy: user.ID,
		Status:      "open",
		Reason:      "seed edit",
		PayloadJSON: `{"name":"Approve Once Artist"}`,
		ChangesJSON: "{}",
		SourcesJSON: "[]",
		Votable:     true,
	}
	if err := db.Create(&edit).Error; err != nil {
		t.Fatalf("create edit: %v", err)
	}

	first, err := svc.ApproveEdit(moderator, edit.ID, "approve once")
	if err != nil {
		t.Fatalf("first approve: %v", err)
	}
	if first.Status != "applied" {
		t.Fatalf("expected applied edit, got %#v", first)
	}

	_, err = svc.ApproveEdit(moderator, edit.ID, "approve twice")
	if !isEditNotOpenError(err) {
		t.Fatalf("expected edit_not_open on second approve, got %v", err)
	}

	var decisions int64
	if err := db.Model(&model.MusicEditDecision{}).Where("edit_id = ?", edit.ID).Count(&decisions).Error; err != nil {
		t.Fatalf("count decisions: %v", err)
	}
	if decisions != 1 {
		t.Fatalf("expected one decision, got %d", decisions)
	}
}

func TestApproveThenRejectEditOnlyAllowsOneDecisionAndOneApply(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	moderator := createModerator(t, db)
	edit := model.MusicEdit{
		Type:        "create_artist",
		EntityType:  "artist",
		SubmittedBy: user.ID,
		Status:      "open",
		Reason:      "seed artist",
		PayloadJSON: `{"name":"One Shot Artist"}`,
		ChangesJSON: "{}",
		SourcesJSON: "[]",
		Votable:     true,
	}
	if err := db.Create(&edit).Error; err != nil {
		t.Fatalf("create edit: %v", err)
	}

	if _, err := svc.ApproveEdit(moderator, edit.ID, "approve"); err != nil {
		t.Fatalf("approve edit: %v", err)
	}
	_, err := svc.RejectEdit(moderator, edit.ID, "reject too late")
	if !isEditNotOpenError(err) {
		t.Fatalf("expected edit_not_open on reject after approve, got %v", err)
	}

	var artists int64
	if err := db.Model(&model.Artist{}).Where("name = ?", "One Shot Artist").Count(&artists).Error; err != nil {
		t.Fatalf("count artists: %v", err)
	}
	if artists != 1 {
		t.Fatalf("expected one applied artist, got %d", artists)
	}

	var decisions int64
	if err := db.Model(&model.MusicEditDecision{}).Where("edit_id = ?", edit.ID).Count(&decisions).Error; err != nil {
		t.Fatalf("count decisions: %v", err)
	}
	if decisions != 1 {
		t.Fatalf("expected one decision, got %d", decisions)
	}
}

func isEditNotOpenError(err error) bool {
	var appErr *apperr.AppError
	return errors.As(err, &appErr) && appErr.Code == "music.edit_not_open"
}

func TestConcurrentApproveEditOnlyAppliesOnce(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	moderator := createModerator(t, db)
	edit := model.MusicEdit{
		Type:        "create_artist",
		EntityType:  "artist",
		SubmittedBy: user.ID,
		Status:      "open",
		Reason:      "seed artist",
		PayloadJSON: `{"name":"Concurrent Artist"}`,
		ChangesJSON: "{}",
		SourcesJSON: "[]",
		Votable:     true,
	}
	if err := db.Create(&edit).Error; err != nil {
		t.Fatalf("create edit: %v", err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.ApproveEdit(moderator, edit.ID, "approve")
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
			continue
		}
		if !isEditNotOpenError(err) {
			t.Fatalf("expected losing approval to return edit_not_open, got %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one successful approval, got %d", successes)
	}

	var artists int64
	if err := db.Model(&model.Artist{}).Where("name = ?", "Concurrent Artist").Count(&artists).Error; err != nil {
		t.Fatalf("count artists: %v", err)
	}
	if artists != 1 {
		t.Fatalf("expected one applied artist, got %d", artists)
	}

	var decisions int64
	if err := db.Model(&model.MusicEditDecision{}).Where("edit_id = ?", edit.ID).Count(&decisions).Error; err != nil {
		t.Fatalf("count decisions: %v", err)
	}
	if decisions != 1 {
		t.Fatalf("expected one decision, got %d", decisions)
	}
}

func TestSubmitEditAutoAppliesUpdateArtistForMainWikiFlow(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	artist := model.Artist{Name: "Before Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}

	edit, err := svc.SubmitEdit(user, SubmitEditRequest{
		Type:       "update_artist",
		EntityType: "artist",
		EntityID:   &artist.ID,
		Changes:    map[string]any{"name": "New Artist"},
		Reason:     "update artist",
		Sources:    []Source{{Type: "url", URL: "https://example.com", Title: "source"}},
	})
	if err != nil {
		t.Fatalf("submit edit: %v", err)
	}
	if edit.Status != "applied" || !edit.AutoApplied || edit.Type != "update_artist" || edit.SubmittedBy != user.ID {
		t.Fatalf("unexpected edit: %#v", edit)
	}

	var persisted model.Artist
	if err := db.Where("id = ?", artist.ID).First(&persisted).Error; err != nil {
		t.Fatalf("reload artist: %v", err)
	}
	if persisted.Name != "New Artist" {
		t.Fatalf("expected immediate artist update, got %#v", persisted)
	}
}

func TestSubmitEditAutoAppliesCreateArtistForMainWikiFlow(t *testing.T) {
	svc, db, user := newMusicTestService(t)

	edit, err := svc.SubmitEdit(user, SubmitEditRequest{
		Type:       "create_artist",
		EntityType: "artist",
		Payload: map[string]any{
			"name":       "Instant Artist",
			"bio":        "created immediately",
			"legal_name": "Instant Legal Name",
			"stage_names": []map[string]any{
				{"name": "Instant Artist", "is_primary": true, "start_date_text": "2020"},
				{"name": "IA", "is_primary": false, "end_date_text": "2021"},
			},
			"birth_place": "Shanghai",
		},
		Reason: "new artist",
	})
	if err != nil {
		t.Fatalf("submit edit: %v", err)
	}

	if edit.Status != "applied" || !edit.AutoApplied {
		t.Fatalf("expected auto-applied create artist edit, got %#v", edit)
	}

	var artist model.Artist
	if err := db.Where("name = ?", "Instant Artist").First(&artist).Error; err != nil {
		t.Fatalf("expected artist persisted immediately: %v", err)
	}
	var stageNames []ArtistStageNamePayload
	if err := json.Unmarshal([]byte(artist.StageNamesJSON), &stageNames); err != nil {
		t.Fatalf("unmarshal stage names json: %v", err)
	}
	if artist.LegalName != "Instant Legal Name" || artist.BirthPlace != "Shanghai" {
		t.Fatalf("expected extended artist fields, got %#v", artist)
	}
	if len(stageNames) != 2 || !stageNames[0].IsPrimary || stageNames[0].Name != "Instant Artist" || stageNames[1].Name != "IA" || stageNames[1].EndDateText != "2021" {
		t.Fatalf("expected structured stage names, got %#v", stageNames)
	}
}

func TestMergeArtistsMovesAlbumRelationsAndAliasesToTarget(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	target := model.Artist{Name: "Ye", LegalName: "Kanye Omari West", EntryStatus: "open"}
	source := model.Artist{Name: "kanye", EntryStatus: "open"}
	album := model.Album{Title: "2049", EntryStatus: "open", Status: "open"}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("create target artist: %v", err)
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source artist: %v", err)
	}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	if err := db.Model(&album).Association("Artists").Append(&source); err != nil {
		t.Fatalf("append source artist to album: %v", err)
	}
	if err := db.Create(&model.ArtistAlias{
		ArtistID: source.ID,
		Alias:    "Kanye West",
	}).Error; err != nil {
		t.Fatalf("create source alias: %v", err)
	}

	if err := svc.MergeArtists(user, source.ID, target.ID); err != nil {
		t.Fatalf("merge artists: %v", err)
	}

	var refreshedSource model.Artist
	if err := db.First(&refreshedSource, "id = ?", source.ID).Error; err != nil {
		t.Fatalf("load source artist: %v", err)
	}
	if refreshedSource.EntryStatus != "closed" {
		t.Fatalf("expected source artist closed after merge, got %#v", refreshedSource)
	}

	var refreshedAlbum model.Album
	if err := db.Preload("Artists").First(&refreshedAlbum, "id = ?", album.ID).Error; err != nil {
		t.Fatalf("load album: %v", err)
	}
	if len(refreshedAlbum.Artists) != 1 || refreshedAlbum.Artists[0].ID != target.ID {
		t.Fatalf("expected album linked to target artist only, got %#v", refreshedAlbum.Artists)
	}

	var aliases []model.ArtistAlias
	if err := db.Where("artist_id = ?", target.ID).Find(&aliases).Error; err != nil {
		t.Fatalf("load target aliases: %v", err)
	}
	aliasSet := map[string]bool{}
	for _, alias := range aliases {
		aliasSet[alias.Alias] = true
	}
	if !aliasSet["kanye"] || !aliasSet["Kanye West"] {
		t.Fatalf("expected merged aliases on target artist, got %#v", aliases)
	}

	var mergeRecord model.ArtistMerge
	if err := db.First(&mergeRecord, "source_artist_id = ? AND target_artist_id = ?", source.ID, target.ID).Error; err != nil {
		t.Fatalf("expected merge audit record, got %v", err)
	}
}

func TestSubmitEditAutoAppliesCreateAlbumForMainWikiFlow(t *testing.T) {
	svc, db, user := newMusicTestService(t)

	artist := model.Artist{Name: "Seed Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}

	edit, err := svc.SubmitEdit(user, SubmitEditRequest{
		Type:       "create_album",
		EntityType: "album",
		Payload: map[string]any{
			"title":        "Instant Album",
			"artist_ids":   []string{artist.ID.String()},
			"album_type":   "album",
			"release_year": 2024,
			"tracks": []map[string]any{
				{"title": "Intro", "track_number": 1},
			},
		},
		Reason: "new album",
	})
	if err != nil {
		t.Fatalf("submit edit: %v", err)
	}

	if edit.Status != "applied" || !edit.AutoApplied {
		t.Fatalf("expected auto-applied create album edit, got %#v", edit)
	}

	var album model.Album
	if err := db.Preload("Artists").Where("title = ?", "Instant Album").First(&album).Error; err != nil {
		t.Fatalf("expected album persisted immediately: %v", err)
	}
	if len(album.Artists) != 1 || album.Artists[0].ID != artist.ID {
		t.Fatalf("expected linked artist, got %#v", album.Artists)
	}
	if album.ReleaseYear != 2024 {
		t.Fatalf("expected release year persisted, got %#v", album)
	}
}

func TestSubmitEditAutoAppliesUpdateAlbumForMainWikiFlow(t *testing.T) {
	svc, db, user := newMusicTestService(t)

	artist := model.Artist{Name: "Album Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	album := model.Album{Title: "Original Album", EntryStatus: "open", Status: "open"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	if err := db.Model(&album).Association("Artists").Append(&artist); err != nil {
		t.Fatalf("append artist: %v", err)
	}

	edit, err := svc.SubmitEdit(user, SubmitEditRequest{
		Type:       "update_album",
		EntityType: "album",
		EntityID:   &album.ID,
		Changes: map[string]any{
			"title":        "New Album",
			"artist_ids":   []any{artist.ID.String()},
			"release_date": "2026-06-17",
			"album_type":   "album",
			"description":  "release notes",
		},
		Reason: "update album",
	})
	if err != nil {
		t.Fatalf("submit edit: %v", err)
	}

	if edit.Status != "applied" || !edit.AutoApplied {
		t.Fatalf("expected auto-applied update album edit, got %#v", edit)
	}

	var updatedAlbum model.Album
	if err := db.Preload("Artists").Where("title = ?", "New Album").First(&updatedAlbum).Error; err != nil {
		t.Fatalf("expected album updated immediately: %v", err)
	}
	if updatedAlbum.EntryStatus != "open" || updatedAlbum.AlbumType != "album" || updatedAlbum.ReleaseDate.Format("2006-01-02") != "2026-06-17" {
		t.Fatalf("unexpected album fields: %#v", updatedAlbum)
	}
}

func TestSubmitEditAutoAppliesUpdateAlbumTracksForMainWikiFlow(t *testing.T) {
	svc, db, user := newMusicTestService(t)

	artist := model.Artist{Name: "Track Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	album := model.Album{Title: "Track Album", EntryStatus: "open", Status: "open"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	if err := db.Model(&album).Association("Artists").Append(&artist); err != nil {
		t.Fatalf("append artist: %v", err)
	}

	existingSong := model.Song{
		Title:       "Keep Me",
		TrackNumber: 1,
		Lyrics:      "old lyrics",
		AudioURL:    "https://cdn.example.com/old.mp3",
		AudioSource: "s3",
		Status:      "open",
		AlbumID:     &album.ID,
	}
	if err := db.Create(&existingSong).Error; err != nil {
		t.Fatalf("create existing song: %v", err)
	}

	removedSong := model.Song{
		Title:       "Remove Me",
		TrackNumber: 2,
		AudioURL:    "https://cdn.example.com/remove.mp3",
		AudioSource: "s3",
		Status:      "open",
		AlbumID:     &album.ID,
	}
	if err := db.Create(&removedSong).Error; err != nil {
		t.Fatalf("create removed song: %v", err)
	}

	edit, err := svc.SubmitEdit(user, SubmitEditRequest{
		Type:       "update_album",
		EntityType: "album",
		EntityID:   &album.ID,
		Changes: map[string]any{
			"title": "Track Album Revised",
			"tracks": []map[string]any{
				{
					"id":           existingSong.ID.String(),
					"title":        "Keep Me Better",
					"track_number": 3,
					"lyrics":       "new lyrics",
					"audio_url":    "https://cdn.example.com/new.mp3",
				},
				{
					"title":        "Brand New Song",
					"track_number": 4,
					"lyrics":       "brand new lyrics",
					"audio_url":    "https://cdn.example.com/brand-new.mp3",
				},
				{
					"id":      removedSong.ID.String(),
					"removed": true,
				},
			},
		},
		Reason: "update album tracks",
	})
	if err != nil {
		t.Fatalf("submit edit: %v", err)
	}

	if edit.Status != "applied" || !edit.AutoApplied {
		t.Fatalf("expected auto-applied update album edit, got %#v", edit)
	}

	var updatedSong model.Song
	if err := db.First(&updatedSong, "id = ?", existingSong.ID).Error; err != nil {
		t.Fatalf("reload existing song: %v", err)
	}
	if updatedSong.Title != "Keep Me Better" || updatedSong.TrackNumber != 3 || updatedSong.AudioURL != "https://cdn.example.com/new.mp3" || updatedSong.Lyrics != "new lyrics" {
		t.Fatalf("expected existing song updated, got %#v", updatedSong)
	}

	var createdSongs []model.Song
	if err := db.Where("album_id = ? AND title = ?", album.ID, "Brand New Song").Find(&createdSongs).Error; err != nil {
		t.Fatalf("load created songs: %v", err)
	}
	if len(createdSongs) != 1 {
		t.Fatalf("expected one created song, got %d", len(createdSongs))
	}
	if createdSongs[0].TrackNumber != 4 || createdSongs[0].AudioURL != "https://cdn.example.com/brand-new.mp3" {
		t.Fatalf("expected created song fields, got %#v", createdSongs[0])
	}

	var closedSong model.Song
	if err := db.First(&closedSong, "id = ?", removedSong.ID).Error; err != nil {
		t.Fatalf("reload removed song: %v", err)
	}
	if closedSong.Status != "closed" {
		t.Fatalf("expected removed song closed, got %#v", closedSong)
	}
}

func TestRecommendAlbumsByModeDiscoverKeepsLowHotScoreAlbums(t *testing.T) {
	svc, db, _ := newMusicTestService(t)

	oldAlbum := model.Album{
		Title:       "Low Heat Old Album",
		EntryStatus: "open",
		Status:      "open",
		HotScore:    1,
	}
	freshAlbum := model.Album{
		Title:       "Low Heat Fresh Album",
		EntryStatus: "open",
		Status:      "open",
		HotScore:    1,
	}

	if err := db.Create(&oldAlbum).Error; err != nil {
		t.Fatalf("create old album: %v", err)
	}
	if err := db.Create(&freshAlbum).Error; err != nil {
		t.Fatalf("create fresh album: %v", err)
	}
	if err := db.Model(&oldAlbum).Update("created_at", "2026-01-01 00:00:00").Error; err != nil {
		t.Fatalf("update old album created_at: %v", err)
	}
	if err := db.Model(&freshAlbum).Update("created_at", "2026-07-01 00:00:00").Error; err != nil {
		t.Fatalf("update fresh album created_at: %v", err)
	}

	items, total, err := svc.RecommendAlbumsByMode("discover", 1, 20)
	if err != nil {
		t.Fatalf("recommend albums: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected 2 discover items, got %d", total)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 discover results, got %d", len(items))
	}
	if items[0].Title != "Low Heat Fresh Album" {
		t.Fatalf("expected fresher low-heat album ranked first, got %#v", items)
	}
}
