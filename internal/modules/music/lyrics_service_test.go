package music

import (
	"errors"
	"sync"
	"testing"

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
