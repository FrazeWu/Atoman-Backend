package music

import (
	"errors"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
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

func TestSubmitEditCreatesOpenEdit(t *testing.T) {
	svc, _, user := newMusicTestService(t)

	edit, err := svc.SubmitEdit(user, SubmitEditRequest{
		Type:       "create_artist",
		EntityType: "artist",
		Payload:    map[string]any{"name": "New Artist"},
		Reason:     "new artist",
		Sources:    []Source{{Type: "url", URL: "https://example.com", Title: "source"}},
	})
	if err != nil {
		t.Fatalf("submit edit: %v", err)
	}
	if edit.Status != "open" || edit.Type != "create_artist" || edit.SubmittedBy != user.ID {
		t.Fatalf("unexpected edit: %#v", edit)
	}
}

func TestApproveCreateArtistAppliesThroughEdit(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	moderator := createModerator(t, db)

	edit, err := svc.SubmitEdit(user, SubmitEditRequest{Type: "create_artist", EntityType: "artist", Payload: map[string]any{
		"name":       "New Artist",
		"birth_date": "1990-05-21",
	}, Reason: "new artist"})
	if err != nil {
		t.Fatalf("submit edit: %v", err)
	}

	applied, err := svc.ApproveEdit(moderator, edit.ID, "verified")
	if err != nil {
		t.Fatalf("approve edit: %v", err)
	}
	if applied.Status != "applied" {
		t.Fatalf("expected applied, got %s", applied.Status)
	}

	var artist model.Artist
	if err := db.Where("name = ?", "New Artist").First(&artist).Error; err != nil {
		t.Fatalf("expected artist created: %v", err)
	}
	if artist.BirthDate == nil || artist.BirthDate.Format("2006-01-02") != "1990-05-21" || artist.BirthYear != 1990 {
		t.Fatalf("expected full birth date and derived year, got %#v", artist)
	}

	var decisions int64
	db.Model(&model.MusicEditDecision{}).Where("edit_id = ?", edit.ID).Count(&decisions)
	if decisions != 1 {
		t.Fatalf("expected one decision, got %d", decisions)
	}

	var audits int64
	db.Model(&model.AuditLog{}).Where("action = ?", "music.edit.approve").Count(&audits)
	if audits != 1 {
		t.Fatalf("expected audit log, got %d", audits)
	}
}

func TestApproveCreateAlbumAppliesThroughEdit(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	moderator := createModerator(t, db)

	artist := model.Artist{Name: "Album Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}

	edit, err := svc.SubmitEdit(user, SubmitEditRequest{
		Type:       "create_album",
		EntityType: "album",
		Payload: map[string]any{
			"title":        "New Album",
			"artist_ids":   []any{artist.ID.String()},
			"release_date": "2026-06-17",
			"album_type":   "album",
			"description":  "release notes",
		},
		Reason: "new album",
	})
	if err != nil {
		t.Fatalf("submit edit: %v", err)
	}

	applied, err := svc.ApproveEdit(moderator, edit.ID, "verified")
	if err != nil {
		t.Fatalf("approve edit: %v", err)
	}
	if applied.Status != "applied" {
		t.Fatalf("expected applied, got %s", applied.Status)
	}

	var album model.Album
	if err := db.Preload("Artists").Where("title = ?", "New Album").First(&album).Error; err != nil {
		t.Fatalf("expected album created: %v", err)
	}
	if album.EntryStatus != "open" || album.AlbumType != "album" || album.ReleaseDate.Format("2006-01-02") != "2026-06-17" {
		t.Fatalf("unexpected album fields: %#v", album)
	}
	if album.HotScore != 0 {
		t.Fatalf("expected new album hot score to default to 0, got %f", album.HotScore)
	}
	if len(album.Artists) != 1 || album.Artists[0].ID != artist.ID {
		t.Fatalf("expected album linked to artist, got %#v", album.Artists)
	}
}

func TestUserCannotApproveEdit(t *testing.T) {
	svc, _, user := newMusicTestService(t)

	edit, err := svc.SubmitEdit(user, SubmitEditRequest{Type: "create_artist", EntityType: "artist", Payload: map[string]any{"name": "New Artist"}, Reason: "new artist"})
	if err != nil {
		t.Fatalf("submit edit: %v", err)
	}

	_, err = svc.ApproveEdit(user, edit.ID, "verified")
	if err == nil {
		t.Fatalf("expected user approval forbidden")
	}
}

func TestApproveInvalidCreateArtistPersistsFailureState(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	moderator := createModerator(t, db)

	edit, err := svc.SubmitEdit(user, SubmitEditRequest{Type: "create_artist", EntityType: "artist", Payload: map[string]any{}, Reason: "missing name"})
	if err != nil {
		t.Fatalf("submit edit: %v", err)
	}

	returned, err := svc.ApproveEdit(moderator, edit.ID, "verified")
	if err == nil {
		t.Fatal("expected approve error for invalid payload")
	}
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) || appErr.HTTPStatus != 400 {
		t.Fatalf("expected 400 app error, got %v", err)
	}
	if returned.Status != "failed_prerequisite" {
		t.Fatalf("expected returned failed status, got %#v", returned)
	}
	var persisted model.MusicEdit
	if err := db.First(&persisted, "id = ?", edit.ID).Error; err != nil {
		t.Fatalf("reload edit: %v", err)
	}
	if persisted.Status != "failed_prerequisite" || !strings.Contains(persisted.FailureReason, "artist name is required") {
		t.Fatalf("expected persisted failure state, got %#v", persisted)
	}
}

func TestApproveDeleteArtistNotFoundReturnsNotFound(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	moderator := createModerator(t, db)
	missingID := uuid.New()
	edit, err := svc.SubmitEdit(user, SubmitEditRequest{Type: "delete_artist", EntityType: "artist", EntityID: &missingID, Reason: "delete missing"})
	if err != nil {
		t.Fatalf("submit edit: %v", err)
	}
	_, err = svc.ApproveEdit(moderator, edit.ID, "verified")
	if err == nil {
		t.Fatal("expected not found error")
	}
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) || appErr.Code != "music.artist_not_found" {
		t.Fatalf("expected music.artist_not_found, got %v", err)
	}
}

func TestCancelEditRequiresLogin(t *testing.T) {
	svc, _, user := newMusicTestService(t)
	edit, err := svc.SubmitEdit(user, SubmitEditRequest{Type: "create_artist", EntityType: "artist", Payload: map[string]any{"name": "New Artist"}, Reason: "new artist"})
	if err != nil {
		t.Fatalf("submit edit: %v", err)
	}
	_, err = svc.CancelEdit(authctx.CurrentUser{}, edit.ID, "")
	if err == nil {
		t.Fatal("expected unauthorized cancel")
	}
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) || appErr.HTTPStatus != 401 {
		t.Fatalf("expected 401 app error, got %v", err)
	}
}

func TestVoteRequiresOpenEditAndUpdatesCurrentVote(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	edit, err := svc.SubmitEdit(user, SubmitEditRequest{Type: "create_artist", EntityType: "artist", Payload: map[string]any{"name": "New Artist"}, Reason: "new artist"})
	if err != nil {
		t.Fatalf("submit edit: %v", err)
	}
	if err := svc.Vote(user, edit.ID, VoteRequest{Vote: "yes", Comment: "first"}); err != nil {
		t.Fatalf("first vote: %v", err)
	}
	if err := svc.Vote(user, edit.ID, VoteRequest{Vote: "no", Comment: "second"}); err != nil {
		t.Fatalf("update vote: %v", err)
	}
	var votes []model.MusicEditVote
	if err := db.Where("edit_id = ? AND user_id = ?", edit.ID, user.ID).Find(&votes).Error; err != nil {
		t.Fatalf("query votes: %v", err)
	}
	if len(votes) != 1 || votes[0].Vote != "no" || votes[0].Comment != "second" {
		t.Fatalf("expected single updated vote, got %#v", votes)
	}

	moderator := createModerator(t, db)
	if _, err := svc.RejectEdit(moderator, edit.ID, "reject"); err != nil {
		t.Fatalf("reject edit: %v", err)
	}
	if err := svc.Vote(user, edit.ID, VoteRequest{Vote: "yes", Comment: "late"}); err == nil {
		t.Fatal("expected vote on closed edit to fail")
	}
}

func TestRejectEditPersistsDecisionAndAudit(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	moderator := createModerator(t, db)
	edit, err := svc.SubmitEdit(user, SubmitEditRequest{Type: "create_artist", EntityType: "artist", Payload: map[string]any{"name": "Reject Me"}, Reason: "new artist"})
	if err != nil {
		t.Fatalf("submit edit: %v", err)
	}
	rejected, err := svc.RejectEdit(moderator, edit.ID, "invalid")
	if err != nil {
		t.Fatalf("reject edit: %v", err)
	}
	if rejected.Status != "rejected" {
		t.Fatalf("expected rejected status, got %#v", rejected)
	}
	var decisions int64
	db.Model(&model.MusicEditDecision{}).Where("edit_id = ? AND decision = ?", edit.ID, "reject").Count(&decisions)
	if decisions != 1 {
		t.Fatalf("expected one reject decision, got %d", decisions)
	}
	var audits int64
	db.Model(&model.AuditLog{}).Where("action = ?", "music.edit.reject").Count(&audits)
	if audits != 1 {
		t.Fatalf("expected one reject audit, got %d", audits)
	}
}
