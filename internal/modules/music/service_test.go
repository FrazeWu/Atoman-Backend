package music

import (
	"testing"

	"atoman/internal/model"
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
			"name": "Instant Artist",
			"bio":  "created immediately",
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
			"title":      "Instant Album",
			"artist_ids": []string{artist.ID.String()},
			"album_type": "album",
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
