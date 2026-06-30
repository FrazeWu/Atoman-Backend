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
		&model.Album{},
		&model.Song{},
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
