package music

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func newLyricsTestService(t *testing.T) (*Service, *gorm.DB, authctx.CurrentUser, model.Song) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Song{},
		&model.MusicSongLyric{},
		&model.MusicSongLyricLine{},
		&model.MusicSongLyricVersion{},
		&model.MusicLyricAnnotation{},
		&model.MusicLyricAnnotationVote{},
		&model.Notification{},
	)
	userModel := model.User{Username: "lyric-owner", Email: "lyric-owner@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&userModel).Error; err != nil {
		t.Fatal(err)
	}
	song := model.Song{Title: "Test Song", AudioURL: "/test.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatal(err)
	}
	user := authctx.CurrentUser{ID: userModel.UUID, Username: userModel.Username, Role: authctx.RoleUser}
	return NewService(db), db, user, song
}

func TestListAndRevertSongLyricVersionsPreserveHistory(t *testing.T) {
	svc, db, user, song := newLyricsTestService(t)
	if _, err := svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "first", Translation: "one", Format: "plain", EditSummary: "v1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "second", Translation: "two", Format: "plain", EditSummary: "v2"}); err != nil {
		t.Fatal(err)
	}

	versions, err := svc.ListSongLyricVersions(song.ID)
	if err != nil || len(versions) != 2 || versions[0].Version != 2 || versions[1].Version != 1 {
		t.Fatalf("unexpected versions: %#v, %v", versions, err)
	}
	if versions[1].Content != "first" || versions[1].Translation != "one" || versions[1].CreatedBy != user.ID || versions[1].ID == uuid.Nil || versions[1].CreatedAt.IsZero() {
		t.Fatalf("unstable version DTO: %#v", versions[1])
	}
	var legacy model.Song
	if err := db.First(&legacy, "id = ?", song.ID).Error; err != nil {
		t.Fatalf("reload legacy song: %v", err)
	}
	if legacy.Lyrics != "second" {
		t.Fatalf("legacy lyrics = %q, want mirrored wiki content", legacy.Lyrics)
	}

	reverted, err := svc.RevertSongLyrics(user, song.ID, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if reverted.Version != 3 || reverted.Content != "first" || reverted.Translation != "one" || reverted.EditSummary != "恢复到第 1 版" {
		t.Fatalf("unexpected reverted lyrics: %#v", reverted)
	}
	versions, err = svc.ListSongLyricVersions(song.ID)
	if err != nil || len(versions) != 3 || versions[0].Version != 3 || versions[1].Version != 2 || versions[2].Version != 1 {
		t.Fatalf("history was not appended: %#v, %v", versions, err)
	}
	var original model.MusicSongLyricVersion
	if err := db.Where("song_id = ? AND version = ?", song.ID, 1).First(&original).Error; err != nil {
		t.Fatal(err)
	}
	if original.Content != "first" || original.EditSummary != "v1" {
		t.Fatalf("target version was modified: %#v", original)
	}

	emptySong := model.Song{Title: "No Versions", AudioURL: "/none.mp3", Status: "open"}
	if err := db.Create(&emptySong).Error; err != nil {
		t.Fatal(err)
	}
	empty, err := svc.ListSongLyricVersions(emptySong.ID)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("expected empty version array, got %#v, %v", empty, err)
	}
	_, err = svc.ListSongLyricVersions(uuid.New())
	assertAppErrorCode(t, err, "music.song_not_found")
	_, err = svc.RevertSongLyrics(authctx.CurrentUser{}, song.ID, 1, "")
	assertAppErrorCode(t, err, "auth.unauthorized")
	_, err = svc.RevertSongLyrics(user, song.ID, 0, "")
	assertAppErrorCode(t, err, "validation.invalid_request")
	_, err = svc.RevertSongLyrics(user, song.ID, 99, "")
	assertAppErrorCode(t, err, "music.lyrics_version_not_found")
}

func TestSaveAndRevertNotifyOnlyFirstNeedsRebindTransition(t *testing.T) {
	svc, db, editor, song := newLyricsTestService(t)
	ownerModel := model.User{Username: "annotation-owner", Email: "annotation-owner@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&ownerModel).Error; err != nil {
		t.Fatal(err)
	}
	owner := authctx.CurrentUser{ID: ownerModel.UUID, Username: ownerModel.Username, Role: authctx.RoleUser}
	lyrics, err := svc.SaveSongLyrics(editor, song.ID, SaveLyricsInput{Content: "hello", Format: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	annotation, err := svc.CreateLyricAnnotation(owner, song.ID, CreateAnnotationInput{LineID: lyrics.Lines[0].ID, SelectedText: "hello", StartOffset: 0, EndOffset: 5, Body: "note"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveSongLyrics(editor, song.ID, SaveLyricsInput{
		Content: "changed", Format: "plain",
		AnnotationResolutions: []AnnotationResolutionInput{{AnnotationID: annotation.ID, Action: "needs_rebind"}},
	}); err != nil {
		t.Fatal(err)
	}

	var notifications []model.Notification
	if err := db.Find(&notifications).Error; err != nil {
		t.Fatal(err)
	}
	if len(notifications) != 1 {
		t.Fatalf("expected one notification, got %#v", notifications)
	}
	n := notifications[0]
	if n.RecipientID != owner.ID || n.ActorID == nil || *n.ActorID != editor.ID || n.Type != "collaboration.required" || n.SourceType != "music_lyrics" || n.SourceID != annotation.ID {
		t.Fatalf("unexpected notification identity: %#v", n)
	}
	if n.Meta["song_id"] != song.ID.String() || n.Meta["annotation_id"] != annotation.ID.String() || n.Meta["title"] != "歌词修改影响了你的注释绑定" || n.Meta["body"] == "" || n.Meta["source_label"] == "" {
		t.Fatalf("unexpected notification meta: %#v", n.Meta)
	}
	if _, err := svc.SaveSongLyrics(editor, song.ID, SaveLyricsInput{Content: "changed again", Format: "plain"}); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&model.Notification{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("needs_rebind was notified repeatedly: %d, %v", count, err)
	}

	readAt := time.Now()
	if err := db.Model(&model.Notification{}).Where("id = ?", n.ID).Update("read_at", readAt).Error; err != nil {
		t.Fatal(err)
	}
	reboundLines, err := ParseLyricLines("restored", "", "plain")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveSongLyrics(editor, song.ID, SaveLyricsInput{
		Content: "restored", Format: "plain",
		AnnotationResolutions: []AnnotationResolutionInput{{
			AnnotationID: annotation.ID, Action: "rebind", LineKey: reboundLines[0].LineKey,
			SelectedText: "restored", StartOffset: 0, EndOffset: 8,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	secondEditorModel := model.User{Username: "second-editor", Email: "second-editor@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&secondEditorModel).Error; err != nil {
		t.Fatal(err)
	}
	secondEditor := authctx.CurrentUser{ID: secondEditorModel.UUID, Username: secondEditorModel.Username, Role: authctx.RoleUser}
	if _, err := svc.SaveSongLyrics(secondEditor, song.ID, SaveLyricsInput{
		Content: "broken", Format: "plain",
		AnnotationResolutions: []AnnotationResolutionInput{{AnnotationID: annotation.ID, Action: "needs_rebind"}},
	}); err != nil {
		t.Fatal(err)
	}
	var repeated model.Notification
	if err := db.Where("recipient_id = ? AND source_type = ? AND source_id = ?", owner.ID, "music_lyrics", annotation.ID).First(&repeated).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Notification{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("repeated transition should reuse one notification: %d, %v", count, err)
	}
	if repeated.ID != n.ID || repeated.SourceID != annotation.ID || repeated.ActorID == nil || *repeated.ActorID != secondEditor.ID || repeated.ReadAt != nil || repeated.Type != "collaboration.required" || repeated.Meta["annotation_id"] != annotation.ID.String() {
		t.Fatalf("notification was not refreshed: %#v", repeated)
	}

	secondSong := model.Song{Title: "Revert Notification", AudioURL: "/revert.mp3", Status: "open"}
	if err := db.Create(&secondSong).Error; err != nil {
		t.Fatal(err)
	}
	first, _ := svc.SaveSongLyrics(editor, secondSong.ID, SaveLyricsInput{Content: "anchor", Format: "plain"})
	secondAnnotation, _ := svc.CreateLyricAnnotation(owner, secondSong.ID, CreateAnnotationInput{LineID: first.Lines[0].ID, SelectedText: "anchor", StartOffset: 0, EndOffset: 6, Body: "note"})
	_, _ = svc.SaveSongLyrics(editor, secondSong.ID, SaveLyricsInput{Content: "anchor changed", Format: "plain"})
	reverted, err := svc.RevertSongLyrics(editor, secondSong.ID, 1, "restore")
	if err != nil {
		t.Fatal(err)
	}
	if len(reverted.Annotations) != 1 || reverted.Annotations[0].ID != secondAnnotation.ID || reverted.Annotations[0].Status != "active" {
		t.Fatalf("valid restored anchor should remain active: %#v", reverted.Annotations)
	}

	thirdSong := model.Song{Title: "Broken Revert Anchor", AudioURL: "/broken-revert.mp3", Status: "open"}
	if err := db.Create(&thirdSong).Error; err != nil {
		t.Fatal(err)
	}
	_, _ = svc.SaveSongLyrics(editor, thirdSong.ID, SaveLyricsInput{Content: "old", Format: "plain"})
	current, _ := svc.SaveSongLyrics(editor, thirdSong.ID, SaveLyricsInput{Content: "new anchor", Format: "plain"})
	brokenAnnotation, _ := svc.CreateLyricAnnotation(owner, thirdSong.ID, CreateAnnotationInput{LineID: current.Lines[0].ID, SelectedText: "anchor", StartOffset: 4, EndOffset: 10, Body: "note"})
	reverted, err = svc.RevertSongLyrics(editor, thirdSong.ID, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(reverted.Annotations) != 1 || reverted.Annotations[0].ID != brokenAnnotation.ID || reverted.Annotations[0].Status != "needs_rebind" {
		t.Fatalf("broken restored anchor should need rebind: %#v", reverted.Annotations)
	}
	if err := db.Model(&model.Notification{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("revert should add one notification: %d, %v", count, err)
	}
}

func TestSaveSongLyricsDoesNotNotifyAnnotationCreatorAboutOwnEdit(t *testing.T) {
	svc, db, user, song := newLyricsTestService(t)
	lyrics, err := svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "hello", Format: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	annotation, err := svc.CreateLyricAnnotation(user, song.ID, CreateAnnotationInput{LineID: lyrics.Lines[0].ID, SelectedText: "hello", StartOffset: 0, EndOffset: 5, Body: "note"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{
		Content: "changed", Format: "plain",
		AnnotationResolutions: []AnnotationResolutionInput{{AnnotationID: annotation.ID, Action: "needs_rebind"}},
	}); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&model.Notification{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("self edit created notification: %d, %v", count, err)
	}
}

func TestSaveSongLyricsDoesNotReuseAggregateNotification(t *testing.T) {
	svc, db, editor, song := newLyricsTestService(t)
	ownerModel := model.User{Username: "aggregate-owner", Email: "aggregate-owner@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&ownerModel).Error; err != nil {
		t.Fatal(err)
	}
	owner := authctx.CurrentUser{ID: ownerModel.UUID, Username: ownerModel.Username, Role: authctx.RoleUser}
	lyrics, _ := svc.SaveSongLyrics(editor, song.ID, SaveLyricsInput{Content: "hello", Format: "plain"})
	annotation, _ := svc.CreateLyricAnnotation(owner, song.ID, CreateAnnotationInput{LineID: lyrics.Lines[0].ID, SelectedText: "hello", StartOffset: 0, EndOffset: 5, Body: "note"})
	readAt := time.Now()
	aggregated := model.Notification{
		RecipientID: owner.ID, Type: "aggregate.type", SourceType: "music_lyrics", SourceID: annotation.ID,
		AggregationKey: "aggregate-key", ReadAt: &readAt,
	}
	if err := db.Create(&aggregated).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveSongLyrics(editor, song.ID, SaveLyricsInput{
		Content: "changed", Format: "plain",
		AnnotationResolutions: []AnnotationResolutionInput{{AnnotationID: annotation.ID, Action: "needs_rebind"}},
	}); err != nil {
		t.Fatal(err)
	}
	var notifications []model.Notification
	if err := db.Order("aggregation_key").Find(&notifications).Error; err != nil {
		t.Fatal(err)
	}
	if len(notifications) != 2 || notifications[0].AggregationKey != "" || notifications[1].AggregationKey != "aggregate-key" || notifications[1].Type != "aggregate.type" || notifications[1].ReadAt == nil {
		t.Fatalf("aggregate notification was reused: %#v", notifications)
	}
}

func TestFailedSaveRollsBackVersionAndNeedsRebindNotification(t *testing.T) {
	svc, db, editor, song := newLyricsTestService(t)
	ownerModel := model.User{Username: "rollback-owner", Email: "rollback-owner@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&ownerModel).Error; err != nil {
		t.Fatal(err)
	}
	owner := authctx.CurrentUser{ID: ownerModel.UUID, Username: ownerModel.Username, Role: authctx.RoleUser}
	lyrics, _ := svc.SaveSongLyrics(editor, song.ID, SaveLyricsInput{Content: "hello", Format: "plain"})
	annotation, _ := svc.CreateLyricAnnotation(owner, song.ID, CreateAnnotationInput{LineID: lyrics.Lines[0].ID, SelectedText: "hello", StartOffset: 0, EndOffset: 5, Body: "note"})

	_, err := svc.SaveSongLyrics(editor, song.ID, SaveLyricsInput{Content: "broken", Format: "plain"})
	assertAppErrorCode(t, err, "music.annotation_anchor_conflict")
	var versions, notifications int64
	_ = db.Model(&model.MusicSongLyricVersion{}).Count(&versions).Error
	_ = db.Model(&model.Notification{}).Count(&notifications).Error
	if versions != 1 || notifications != 0 {
		t.Fatalf("conflict appended state: versions=%d notifications=%d", versions, notifications)
	}

	callback := "test:fail-lyric-version-create"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "music_song_lyric_versions" {
			tx.AddError(errors.New("forced version failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
	_, err = svc.SaveSongLyrics(editor, song.ID, SaveLyricsInput{
		Content: "broken", Format: "plain",
		AnnotationResolutions: []AnnotationResolutionInput{{AnnotationID: annotation.ID, Action: "needs_rebind"}},
	})
	if err == nil {
		t.Fatal("expected forced version failure")
	}
	_ = db.Model(&model.MusicSongLyricVersion{}).Count(&versions).Error
	_ = db.Model(&model.Notification{}).Count(&notifications).Error
	var stored model.MusicLyricAnnotation
	_ = db.First(&stored, "id = ?", annotation.ID).Error
	if versions != 1 || notifications != 0 || stored.Status != "active" {
		t.Fatalf("transaction leaked state: versions=%d notifications=%d annotation=%#v", versions, notifications, stored)
	}
}

func assertAppErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func TestSaveSongLyricsCreatesAndVersionsCurrentLyrics(t *testing.T) {
	svc, db, user, song := newLyricsTestService(t)

	first, err := svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "alpha\nbeta", Translation: "甲\n乙", Format: "plain"})
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	if first.Version != 1 || first.EditSummary != "更新歌词" || len(first.Lines) != 2 {
		t.Fatalf("unexpected first lyrics: %#v", first)
	}
	second, err := svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "alpha\nbeta!", Format: "plain", EditSummary: "修正错字"})
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if second.Version != 2 || second.EditSummary != "修正错字" {
		t.Fatalf("unexpected second lyrics: %#v", second)
	}
	var versions []model.MusicSongLyricVersion
	if err := db.Order("version").Find(&versions).Error; err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0].Version != 1 || versions[1].Version != 2 {
		t.Fatalf("unexpected versions: %#v", versions)
	}
}

func TestSaveSongLyricsValidatesUserSongAndFormat(t *testing.T) {
	svc, _, user, song := newLyricsTestService(t)
	_, err := svc.SaveSongLyrics(authctx.CurrentUser{}, song.ID, SaveLyricsInput{Format: "plain"})
	assertAppErrorCode(t, err, "auth.unauthorized")
	_, err = svc.SaveSongLyrics(user, uuid.New(), SaveLyricsInput{Format: "plain"})
	assertAppErrorCode(t, err, "music.song_not_found")
	_, err = svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Format: "html"})
	assertAppErrorCode(t, err, "validation.invalid_request")
}

func TestSaveSongLyricsPreservesMatchingLineIDsAcrossInsert(t *testing.T) {
	svc, _, user, song := newLyricsTestService(t)
	first, err := svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "alpha\nbeta", Format: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "new\nalpha\nbeta", Format: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]uuid.UUID{}
	for _, line := range first.Lines {
		byKey[line.LineKey] = line.ID
	}
	for _, line := range second.Lines {
		if oldID, ok := byKey[line.LineKey]; ok && oldID != line.ID {
			t.Fatalf("line %s changed id from %s to %s", line.LineKey, oldID, line.ID)
		}
	}
}

func TestSaveSongLyricsPreservesRepeatedLRCLinesIDs(t *testing.T) {
	svc, _, user, song := newLyricsTestService(t)
	input := SaveLyricsInput{Content: "[00:01.00]A\n[00:01.00]A", Format: "lrc"}
	first, err := svc.SaveSongLyrics(user, song.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.SaveSongLyrics(user, song.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Lines) != 2 || first.Lines[0].ID == first.Lines[1].ID {
		t.Fatalf("expected two distinct persisted lines: %#v", first.Lines)
	}
	for index := range first.Lines {
		if first.Lines[index].LineKey != second.Lines[index].LineKey || first.Lines[index].ID != second.Lines[index].ID {
			t.Fatalf("line %d identity changed: first=%#v second=%#v", index, first.Lines, second.Lines)
		}
	}
}

func TestSaveSongLyricsAnchorConflictRollsBackAndRebindSucceeds(t *testing.T) {
	svc, db, user, song := newLyricsTestService(t)
	lyrics, err := svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "hello world\nreplacement", Format: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	annotation, err := svc.CreateLyricAnnotation(user, song.ID, CreateAnnotationInput{
		LineID: lyrics.Lines[0].ID, SelectedText: "world", StartOffset: 6, EndOffset: 11, Body: "note",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "goodbye\nreplacement", Format: "plain"})
	assertAppErrorCode(t, err, "music.annotation_anchor_conflict")
	current, err := svc.GetSongLyrics(authctx.CurrentUser{}, song.ID)
	if err != nil || current.Content != "hello world\nreplacement" || current.Version != 1 {
		t.Fatalf("save was not rolled back: %#v, %v", current, err)
	}

	reboundLines, err := ParseLyricLines("goodbye\nreplacement 😀", "", "plain")
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{
		Content: "goodbye\nreplacement 😀", Format: "plain",
		AnnotationResolutions: []AnnotationResolutionInput{{
			AnnotationID: annotation.ID, Action: "rebind", LineKey: reboundLines[1].LineKey,
			SelectedText: "😀", StartOffset: 12, EndOffset: 14,
		}},
	})
	if err != nil {
		t.Fatalf("rebind save: %v", err)
	}
	var rebound model.MusicLyricAnnotation
	if err := db.First(&rebound, "id = ?", annotation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if rebound.Status != "active" || rebound.SelectedText != "😀" || rebound.StartOffset != 12 || rebound.EndOffset != 14 {
		t.Fatalf("unexpected rebound annotation: %#v", rebound)
	}
}

func TestSaveSongLyricsAnchorConflictReportsAllUnresolvedAnnotationsBeforeResolving(t *testing.T) {
	svc, db, user, song := newLyricsTestService(t)
	lyrics, err := svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "alpha beta", Format: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.CreateLyricAnnotation(user, song.ID, CreateAnnotationInput{
		LineID: lyrics.Lines[0].ID, SelectedText: "alpha", StartOffset: 0, EndOffset: 5, Body: "first",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.CreateLyricAnnotation(user, song.ID, CreateAnnotationInput{
		LineID: lyrics.Lines[0].ID, SelectedText: "beta", StartOffset: 6, EndOffset: 10, Body: "second",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{
		Content: "changed", Format: "plain",
		AnnotationResolutions: []AnnotationResolutionInput{{AnnotationID: first.ID, Action: "needs_rebind"}},
	})
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) || appErr.Code != "music.annotation_anchor_conflict" {
		t.Fatalf("expected annotation conflict, got %v", err)
	}
	wantIDs := []string{second.ID.String()}
	if got, ok := appErr.Details["annotation_ids"].([]string); !ok || !slices.Equal(got, wantIDs) {
		t.Fatalf("unexpected unresolved IDs: %#v", appErr.Details)
	}
	var stored model.MusicLyricAnnotation
	if err := db.First(&stored, "id = ?", first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "active" {
		t.Fatalf("resolution ran before unresolved anchors were reported: %#v", stored)
	}

	_, err = svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "changed", Format: "plain"})
	if !errors.As(err, &appErr) {
		t.Fatalf("expected annotation conflict, got %v", err)
	}
	wantIDs = []string{first.ID.String(), second.ID.String()}
	slices.Sort(wantIDs)
	if got, ok := appErr.Details["annotation_ids"].([]string); !ok || !slices.Equal(got, wantIDs) {
		t.Fatalf("unexpected complete conflict IDs: got %#v want %#v", appErr.Details, wantIDs)
	}
}

func TestSaveSongLyricsDoesNotReopenResolvedNeedsRebindConflict(t *testing.T) {
	svc, _, user, song := newLyricsTestService(t)
	lyrics, _ := svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "hello", Format: "plain"})
	annotation, _ := svc.CreateLyricAnnotation(user, song.ID, CreateAnnotationInput{
		LineID: lyrics.Lines[0].ID, SelectedText: "hello", StartOffset: 0, EndOffset: 5, Body: "note",
	})
	first, err := svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{
		Content: "changed", Format: "plain",
		AnnotationResolutions: []AnnotationResolutionInput{{AnnotationID: annotation.ID, Action: "needs_rebind"}},
	})
	if err != nil || len(first.Annotations) != 1 || first.Annotations[0].Status != "needs_rebind" {
		t.Fatalf("mark needs rebind: %#v, %v", first, err)
	}
	second, err := svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "changed again", Format: "plain"})
	if err != nil || len(second.Annotations) != 1 || second.Annotations[0].Status != "needs_rebind" {
		t.Fatalf("save after resolved conflict: %#v, %v", second, err)
	}
}

func TestCreateLyricAnnotationValidatesAnchorAndCurrentSongLine(t *testing.T) {
	svc, _, user, song := newLyricsTestService(t)
	lyrics, err := svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "你好吗\na😀b", Format: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.CreateLyricAnnotation(user, song.ID, CreateAnnotationInput{
		LineKey: lyrics.Lines[0].LineKey, SelectedText: "好", StartOffset: 1, EndOffset: 2, Body: "unicode",
	})
	if err != nil || created.LineID != lyrics.Lines[0].ID {
		t.Fatalf("create unicode annotation: %#v, %v", created, err)
	}
	emoji, err := svc.CreateLyricAnnotation(user, song.ID, CreateAnnotationInput{
		LineID: lyrics.Lines[1].ID, SelectedText: "😀", StartOffset: 1, EndOffset: 3, Body: "emoji",
	})
	if err != nil || emoji.LineID != lyrics.Lines[1].ID {
		t.Fatalf("create emoji annotation: %#v, %v", emoji, err)
	}
	_, err = svc.CreateLyricAnnotation(user, song.ID, CreateAnnotationInput{
		LineID: lyrics.Lines[0].ID, SelectedText: "吗", StartOffset: 1, EndOffset: 2, Body: "bad",
	})
	assertAppErrorCode(t, err, "validation.invalid_request")

	other := model.Song{Title: "Other", AudioURL: "/other.mp3"}
	if err := svc.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	_, err = svc.CreateLyricAnnotation(user, other.ID, CreateAnnotationInput{
		LineID: lyrics.Lines[0].ID, SelectedText: "你", StartOffset: 0, EndOffset: 1, Body: "cross song",
	})
	assertAppErrorCode(t, err, "music.lyric_line_not_found")
}

func TestAnnotationPermissionsSoftDeleteAndAnonymousDTO(t *testing.T) {
	svc, db, user, song := newLyricsTestService(t)
	lyrics, _ := svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "hello", Format: "plain"})
	annotation, err := svc.CreateLyricAnnotation(user, song.ID, CreateAnnotationInput{
		LineID: lyrics.Lines[0].ID, SelectedText: "hello", StartOffset: 0, EndOffset: 5, Body: "first",
	})
	if err != nil {
		t.Fatal(err)
	}
	otherModel := model.User{Username: "other", Email: "other@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&otherModel).Error; err != nil {
		t.Fatal(err)
	}
	other := authctx.CurrentUser{ID: otherModel.UUID, Username: otherModel.Username, Role: authctx.RoleUser}
	_, err = svc.UpdateLyricAnnotation(other, song.ID, annotation.ID, "stolen")
	assertAppErrorCode(t, err, "music.annotation_forbidden")

	anonymous, err := svc.GetSongLyrics(authctx.CurrentUser{}, song.ID)
	if err != nil || len(anonymous.Annotations) != 1 || anonymous.Annotations[0].CanEdit || anonymous.Annotations[0].ViewerVote != "none" {
		t.Fatalf("unexpected anonymous dto: %#v, %v", anonymous, err)
	}
	if err := svc.DeleteLyricAnnotation(user, song.ID, annotation.ID); err != nil {
		t.Fatal(err)
	}
	afterDelete, _ := svc.GetSongLyrics(user, song.ID)
	if len(afterDelete.Annotations) != 0 {
		t.Fatalf("deleted annotation is visible: %#v", afterDelete.Annotations)
	}
	_, err = svc.SetLyricAnnotationVote(user, song.ID, annotation.ID, "up")
	assertAppErrorCode(t, err, "music.annotation_not_found")
}

func TestUpdateLyricAnnotationKeepsNeedsRebindStatus(t *testing.T) {
	svc, db, user, song := newLyricsTestService(t)
	lyrics, _ := svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "hello", Format: "plain"})
	annotation, _ := svc.CreateLyricAnnotation(user, song.ID, CreateAnnotationInput{
		LineID: lyrics.Lines[0].ID, SelectedText: "hello", StartOffset: 0, EndOffset: 5, Body: "before",
	})
	if err := db.Model(&model.MusicLyricAnnotation{}).Where("id = ?", annotation.ID).Update("status", "needs_rebind").Error; err != nil {
		t.Fatal(err)
	}
	updated, err := svc.UpdateLyricAnnotation(user, song.ID, annotation.ID, "after")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Body != "after" || updated.Status != "needs_rebind" {
		t.Fatalf("unexpected updated annotation: %#v", updated)
	}
}

func TestUpdateAndDeleteLyricAnnotationLockSongBeforeAnnotation(t *testing.T) {
	for _, action := range []string{"update", "delete"} {
		t.Run(action, func(t *testing.T) {
			svc, db, user, song := newLyricsTestService(t)
			lyrics, _ := svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "hello", Format: "plain"})
			annotation, _ := svc.CreateLyricAnnotation(user, song.ID, CreateAnnotationInput{
				LineID: lyrics.Lines[0].ID, SelectedText: "hello", StartOffset: 0, EndOffset: 5, Body: "before",
			})

			var lockedTables []string
			callbackName := "test:lyrics_annotation_lock_order:" + action
			if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if _, locked := tx.Statement.Clauses["FOR"]; locked {
					lockedTables = append(lockedTables, tx.Statement.Table)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

			if action == "update" {
				_, err := svc.UpdateLyricAnnotation(user, song.ID, annotation.ID, "after")
				if err != nil {
					t.Fatal(err)
				}
			} else if err := svc.DeleteLyricAnnotation(user, song.ID, annotation.ID); err != nil {
				t.Fatal(err)
			}
			if len(lockedTables) < 2 || lockedTables[0] != "Songs" || lockedTables[1] != "music_lyric_annotations" {
				t.Fatalf("expected Song then annotation locks, got %#v", lockedTables)
			}
		})
	}
}

func TestSetLyricAnnotationVoteReplacesCancelsAndAllowsRevote(t *testing.T) {
	svc, _, user, song := newLyricsTestService(t)
	lyrics, _ := svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "hello", Format: "plain"})
	annotation, _ := svc.CreateLyricAnnotation(user, song.ID, CreateAnnotationInput{LineID: lyrics.Lines[0].ID, SelectedText: "hello", StartOffset: 0, EndOffset: 5, Body: "note"})

	up, err := svc.SetLyricAnnotationVote(user, song.ID, annotation.ID, "up")
	if err != nil || up.Upvotes != 1 || up.Downvotes != 0 || up.ViewerVote != "up" {
		t.Fatalf("upvote: %#v, %v", up, err)
	}
	down, err := svc.SetLyricAnnotationVote(user, song.ID, annotation.ID, "down")
	if err != nil || down.Upvotes != 0 || down.Downvotes != 1 || down.ViewerVote != "down" {
		t.Fatalf("replace vote: %#v, %v", down, err)
	}
	none, err := svc.SetLyricAnnotationVote(user, song.ID, annotation.ID, "none")
	if err != nil || none.Upvotes != 0 || none.Downvotes != 0 || none.ViewerVote != "none" {
		t.Fatalf("cancel vote: %#v, %v", none, err)
	}
	revote, err := svc.SetLyricAnnotationVote(user, song.ID, annotation.ID, "up")
	if err != nil || revote.Upvotes != 1 || revote.ViewerVote != "up" {
		t.Fatalf("revote: %#v, %v", revote, err)
	}
}

func TestConcurrentSetLyricAnnotationVoteIsIdempotent(t *testing.T) {
	svc, db, user, song := newLyricsTestService(t)
	lyrics, _ := svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "hello", Format: "plain"})
	annotation, _ := svc.CreateLyricAnnotation(user, song.ID, CreateAnnotationInput{
		LineID: lyrics.Lines[0].ID, SelectedText: "hello", StartOffset: 0, EndOffset: 5, Body: "note",
	})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.SetLyricAnnotationVote(user, song.ID, annotation.ID, "up")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent vote: %v", err)
		}
	}
	var votes int64
	if err := db.Model(&model.MusicLyricAnnotationVote{}).Where("annotation_id = ? AND user_id = ?", annotation.ID, user.ID).Count(&votes).Error; err != nil {
		t.Fatal(err)
	}
	if votes != 1 {
		t.Fatalf("expected one vote, got %d", votes)
	}
}

func TestGetSongLyricsSortsAnnotationsByScoreThenUpvotes(t *testing.T) {
	svc, db, owner, song := newLyricsTestService(t)
	lyrics, _ := svc.SaveSongLyrics(owner, song.ID, SaveLyricsInput{Content: "abcdef", Format: "plain"})
	first, _ := svc.CreateLyricAnnotation(owner, song.ID, CreateAnnotationInput{LineID: lyrics.Lines[0].ID, SelectedText: "a", StartOffset: 0, EndOffset: 1, Body: "one up"})
	second, _ := svc.CreateLyricAnnotation(owner, song.ID, CreateAnnotationInput{LineID: lyrics.Lines[0].ID, SelectedText: "b", StartOffset: 1, EndOffset: 2, Body: "two up one down"})
	users := make([]authctx.CurrentUser, 3)
	for i := range users {
		m := model.User{Username: "voter-" + string(rune('a'+i)), Email: "voter-" + string(rune('a'+i)) + "@example.com", Password: "hash", IsActive: true}
		if err := db.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
		users[i] = authctx.CurrentUser{ID: m.UUID, Username: m.Username, Role: authctx.RoleUser}
	}
	_, _ = svc.SetLyricAnnotationVote(users[0], song.ID, first.ID, "up")
	_, _ = svc.SetLyricAnnotationVote(users[0], song.ID, second.ID, "up")
	_, _ = svc.SetLyricAnnotationVote(users[1], song.ID, second.ID, "up")
	_, _ = svc.SetLyricAnnotationVote(users[2], song.ID, second.ID, "down")

	dto, err := svc.GetSongLyrics(authctx.CurrentUser{}, song.ID)
	if err != nil || len(dto.Annotations) != 2 || dto.Annotations[0].ID != second.ID {
		t.Fatalf("unexpected order: %#v, %v", dto.Annotations, err)
	}
}

func TestGetSongLyricsReturnsEmptyDTOAndMissingSong(t *testing.T) {
	svc, _, _, song := newLyricsTestService(t)
	dto, err := svc.GetSongLyrics(authctx.CurrentUser{}, song.ID)
	if err != nil || dto.SongID != song.ID || dto.Format != "plain" || dto.Lines == nil || dto.Annotations == nil {
		t.Fatalf("unexpected empty dto: %#v, %v", dto, err)
	}
	_, err = svc.GetSongLyrics(authctx.CurrentUser{}, uuid.New())
	assertAppErrorCode(t, err, "music.song_not_found")
}

func TestConcurrentSaveSongLyricsProducesUniqueVersions(t *testing.T) {
	svc, db, user, song := newLyricsTestService(t)
	const saves = 4
	errs := make(chan error, saves)
	var wg sync.WaitGroup
	for i := 0; i < saves; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := svc.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "line", Format: "plain", EditSummary: "concurrent"})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent save: %v", err)
		}
	}
	var versions []model.MusicSongLyricVersion
	if err := db.Where("song_id = ?", song.ID).Order("version").Find(&versions).Error; err != nil {
		t.Fatal(err)
	}
	if len(versions) != saves {
		t.Fatalf("expected %d versions, got %#v", saves, versions)
	}
	for i, version := range versions {
		if version.Version != i+1 {
			t.Fatalf("unexpected versions: %#v", versions)
		}
	}
}
