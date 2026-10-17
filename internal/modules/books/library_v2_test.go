package books

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func newBookLibraryTestReader(t *testing.T, service *Service) authctx.CurrentUser {
	t.Helper()
	id := uuid.New()
	user := model.User{UUID: id, Username: id.String(), Email: id.String() + "@example.test", Password: "hash", IsActive: true}
	require.NoError(t, service.db.Create(&user).Error)
	return authctx.CurrentUser{ID: id, Role: authctx.RoleUser}
}

func TestBookLibraryEntriesArePrivateWorkScopedAndVersioned(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	workID := uuid.MustParse(work.ID)
	input := SaveBookLibraryEntryInput{Status: "reading", Tags: []string{" History ", "history", "历史"}}
	entry, err := service.SaveBookLibraryEntry(ctx, user, workID, input)
	require.NoError(t, err)
	require.Equal(t, []string{"history", "历史"}, entry.Tags)
	retry, err := service.SaveBookLibraryEntry(ctx, user, workID, input)
	require.NoError(t, err)
	require.Equal(t, entry.ID, retry.ID)
	require.EqualValues(t, 1, retry.Version)
	items, total, err := service.ListBookLibraryEntries(ctx, user, "reading", "History", 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, items, 1)
	require.Equal(t, work.Title, items[0].WorkTitle)
	other := newBookLibraryTestReader(t, service)
	items, total, err = service.ListBookLibraryEntries(ctx, other, "", "", 20, 0)
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, items)
	_, err = service.DeleteBookLibraryEntry(ctx, other, entry.ID, 1)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	input.EntryID = entry.ID
	input.BaseVersion = 1
	input.Status = "read"
	updated, err := service.SaveBookLibraryEntry(ctx, user, workID, input)
	require.NoError(t, err)
	require.EqualValues(t, 2, updated.Version)
	input.Status = "dropped"
	_, err = service.SaveBookLibraryEntry(ctx, user, workID, input)
	require.Equal(t, "books.library_entry_conflict", apperr.FromError(err).Code)
	_, err = service.DeleteBookLibraryEntry(ctx, user, entry.ID, 1)
	require.Equal(t, 409, apperr.FromError(err).HTTPStatus)
	tombstone, err := service.DeleteBookLibraryEntry(ctx, user, entry.ID, 2)
	require.NoError(t, err)
	require.EqualValues(t, 3, tombstone.Version)
	items, total, err = service.ListBookLibraryEntries(ctx, user, "", "", 20, 0)
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, items)
	_, err = service.SaveBookLibraryEntry(ctx, user, workID, SaveBookLibraryEntryInput{Status: "reading"})
	require.Equal(t, 409, apperr.FromError(err).HTTPStatus)
	input.BaseVersion = 3
	input.Status = "want_to_read"
	entry, err = service.SaveBookLibraryEntry(ctx, user, workID, input)
	require.NoError(t, err)
	require.EqualValues(t, 4, entry.Version)
	var selections int64
	require.NoError(t, service.db.Model(&model.BookReadingSelection{}).Count(&selections).Error)
	require.Zero(t, selections, "shelving does not silently choose a file")
}

func TestBookPrivateNotesSurviveRevisionChangesAndStayPrivate(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	workID := uuid.MustParse(work.ID)
	asset, revision := readyBookReadingFile(t, service, user, workID, "zh", "txt")
	id := uuid.New()
	input := SaveBookPrivateNoteInput{WorkID: workID, SourceRevisionID: &revision.ID, Title: "读书思考", Body: "100% 私人内容", Tags: []string{"随笔", " History ", "history"}}
	note, err := service.SaveBookPrivateNote(ctx, user, id, input)
	require.NoError(t, err)
	require.Equal(t, []string{"history", "随笔"}, note.Tags)
	retry, err := service.SaveBookPrivateNote(ctx, user, id, input)
	require.NoError(t, err)
	require.EqualValues(t, 1, retry.Version)
	_, newRevision := readyBookReadingFile(t, service, user, workID, "en", "pdf")
	_, err = service.SetBookReadingSelection(ctx, user, workID, SetBookReadingSelectionInput{AssetID: newRevision.AssetID, RevisionID: newRevision.ID})
	require.NoError(t, err)
	items, total, err := service.ListBookPrivateNotes(ctx, user, BookPrivateNoteFilter{WorkID: workID, Tag: "HISTORY", Query: "100%"})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, items, 1)
	require.Equal(t, id, items[0].ID)
	other := newBookLibraryTestReader(t, service)
	_, err = service.GetBookPrivateNote(ctx, other, id)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	_, err = service.SaveBookPrivateNote(ctx, other, id, input)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	_, err = service.DeleteBookPrivateNote(ctx, other, id, 1)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	items, total, err = service.ListBookPrivateNotes(ctx, other, BookPrivateNoteFilter{})
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, items)
	input.BaseVersion = 1
	input.Body = "修改后的私有内容"
	note, err = service.SaveBookPrivateNote(ctx, user, id, input)
	require.NoError(t, err)
	require.EqualValues(t, 2, note.Version)
	input.Body = "旧设备内容"
	_, err = service.SaveBookPrivateNote(ctx, user, id, input)
	require.Equal(t, 409, apperr.FromError(err).HTTPStatus)
	require.NoError(t, service.db.Model(&asset).Update("status", "removed").Error)
	input.BaseVersion = 2
	input.Body = "文件下架后仍可整理笔记"
	_, err = service.SaveBookPrivateNote(ctx, user, id, input)
	require.NoError(t, err)
	encoded, err := json.Marshal(note)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), user.ID.String())
}

func TestBookPrivateNoteTrashExpiresWithoutOfflineResurrection(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	id := uuid.New()
	input := SaveBookPrivateNoteInput{WorkID: uuid.MustParse(work.ID), Title: "回收站笔记", Body: "到期应清除的正文", Tags: []string{"private"}}
	_, err := service.SaveBookPrivateNote(ctx, user, id, input)
	require.NoError(t, err)
	deleted, err := service.DeleteBookPrivateNote(ctx, user, id, 1)
	require.NoError(t, err)
	require.EqualValues(t, 2, deleted.Version)
	retry, err := service.DeleteBookPrivateNote(ctx, user, id, 1)
	require.NoError(t, err)
	require.Equal(t, deleted, retry)
	_, total, err := service.ListBookPrivateNotes(ctx, user, BookPrivateNoteFilter{})
	require.NoError(t, err)
	require.Zero(t, total)
	trash, total, err := service.ListBookPrivateNotes(ctx, user, BookPrivateNoteFilter{Trash: true})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.NotNil(t, trash[0].TrashedAt)
	_, err = service.SaveBookPrivateNote(ctx, user, id, input)
	require.Equal(t, 409, apperr.FromError(err).HTTPStatus)
	restored, err := service.RestoreBookPrivateNote(ctx, user, id, 2)
	require.NoError(t, err)
	require.EqualValues(t, 3, restored.Version)
	_, err = service.RestoreBookPrivateNote(ctx, user, id, 2)
	require.NoError(t, err)
	_, err = service.DeleteBookPrivateNote(ctx, user, id, 3)
	require.NoError(t, err)
	require.NoError(t, service.db.Unscoped().Model(&model.BookPrivateNote{}).Where("id = ?", id).Update("deleted_at", time.Now().UTC().Add(-31*24*time.Hour)).Error)
	_, err = service.RestoreBookPrivateNote(ctx, user, id, 4)
	require.Equal(t, 410, apperr.FromError(err).HTTPStatus)
	_, err = service.GetBookPrivateNote(ctx, user, id)
	require.Equal(t, 410, apperr.FromError(err).HTTPStatus)
	var stored model.BookPrivateNote
	require.NoError(t, service.db.Unscoped().First(&stored, "id = ?", id).Error)
	require.Empty(t, stored.Body)
	require.Empty(t, stored.Title)
	require.JSONEq(t, "[]", stored.TagsJSON)
	require.NotNil(t, stored.PurgedAt)
	_, err = service.SaveBookPrivateNote(ctx, user, id, input)
	require.Equal(t, 409, apperr.FromError(err).HTTPStatus)
	trash, total, err = service.ListBookPrivateNotes(ctx, user, BookPrivateNoteFilter{Trash: true})
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, trash)
}

func TestBookPrivateNoteImportExportIsAtomicPrivateAndNonDestructive(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	id := uuid.New()
	_, err := service.SaveBookPrivateNote(ctx, user, id, SaveBookPrivateNoteInput{WorkID: uuid.MustParse(work.ID), Title: "可导出笔记", Body: "私人正文", Tags: []string{"tag"}})
	require.NoError(t, err)
	export, err := service.ExportBookPrivateNotes(ctx, user)
	require.NoError(t, err)
	require.Len(t, export.Notes, 1)
	encoded, err := json.Marshal(export)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), user.ID.String())
	input := ImportBookPrivateNotesInput{RequestID: uuid.New(), SchemaVersion: export.SchemaVersion, Notes: export.Notes}
	result, err := service.ImportBookPrivateNotes(ctx, user, input)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{id}, result.Skipped)
	require.Empty(t, result.Created)
	other := newBookLibraryTestReader(t, service)
	result, err = service.ImportBookPrivateNotes(ctx, other, input)
	require.NoError(t, err)
	require.Len(t, result.Created, 1)
	require.NotEqual(t, id, result.Created[0])
	retried, err := service.ImportBookPrivateNotes(ctx, other, input)
	require.NoError(t, err)
	require.True(t, retried.Duplicate)
	require.Equal(t, result.Created, retried.Created)
	input.Notes[0].Body = "修改后的导入正文"
	_, err = service.ImportBookPrivateNotes(ctx, other, input)
	require.Equal(t, "books.note_import_request_reused", apperr.FromError(err).Code)
	input.RequestID = uuid.New()
	conflict, err := service.ImportBookPrivateNotes(ctx, other, input)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{id}, conflict.Conflicts)
	kept, err := service.GetBookPrivateNote(ctx, other, result.Created[0])
	require.NoError(t, err)
	require.Equal(t, "私人正文", kept.Body)
	invalid := ImportBookPrivateNotesInput{RequestID: uuid.New(), SchemaVersion: 1, Notes: []BookNoteTransferItem{
		{ID: uuid.New(), WorkID: uuid.MustParse(work.ID), Title: "应回滚的条目"},
		{ID: uuid.New(), WorkID: uuid.New(), Title: "不存在的作品"},
	}}
	_, err = service.ImportBookPrivateNotes(ctx, other, invalid)
	require.Error(t, err)
	_, total, err := service.ListBookPrivateNotes(ctx, other, BookPrivateNoteFilter{})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
}

func TestBookLibraryMergePreservesNewPrivateNotesAndRejectsStaleEntry(t *testing.T) {
	service, user, source, target := newBookMergeTestService(t)
	ctx := context.Background()
	sourceID, targetID := uuid.MustParse(source.ID), uuid.MustParse(target.ID)
	a, err := service.SaveBookLibraryEntry(ctx, user, sourceID, SaveBookLibraryEntryInput{Status: "reading"})
	require.NoError(t, err)
	b, err := service.SaveBookLibraryEntry(ctx, user, targetID, SaveBookLibraryEntryInput{Status: "read"})
	require.NoError(t, err)
	id := uuid.New()
	input := SaveBookPrivateNoteInput{WorkID: sourceID, Title: "合并前", Body: "私人笔记"}
	_, err = service.SaveBookPrivateNote(ctx, user, id, input)
	require.NoError(t, err)
	merge := mergeBookTestWorks(t, service, user, source, target)
	items, total, err := service.ListBookLibraryEntries(ctx, user, "", "", 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, b.ID, items[0].ID)
	_, err = service.SaveBookLibraryEntry(ctx, user, sourceID, SaveBookLibraryEntryInput{EntryID: a.ID, BaseVersion: a.Version, Status: "dropped"})
	require.Equal(t, 409, apperr.FromError(err).HTTPStatus)
	input.WorkID = targetID
	input.BaseVersion = 1
	input.Body = "合并期间写的新笔记"
	_, err = service.SaveBookPrivateNote(ctx, user, id, input)
	require.NoError(t, err)
	notes, total, err := service.ListBookPrivateNotes(ctx, user, BookPrivateNoteFilter{WorkID: targetID})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, sourceID, notes[0].WorkID)
	_, err = service.RevertBookWorkMerge(ctx, user, merge.ID)
	require.NoError(t, err)
	notes, total, err = service.ListBookPrivateNotes(ctx, user, BookPrivateNoteFilter{WorkID: sourceID})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, "合并期间写的新笔记", notes[0].Body)
	items, total, err = service.ListBookLibraryEntries(ctx, user, "", "", 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	require.Len(t, items, 2)
}

func TestBookLibraryAndPrivateNotesHTTP(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { authctx.SetCurrentUser(c, user); c.Next() })
	RegisterRoutes(router.Group("/api/v1/books"), service)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1/books"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
		return w
	}
	w := request(http.MethodPut, "/me/library/works/"+work.ID, `{"status":"reading","tags":["历史"]}`)
	require.Equal(t, 200, w.Code, w.Body.String())
	w = request(http.MethodGet, "/me/library?status=reading", "")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), "历史")
	id := uuid.NewString()
	url := "/me/private-notes/" + id
	w = request(http.MethodPut, url, `{"work_id":"`+work.ID+`","title":"我的思考","body":"不应公开"}`)
	require.Equal(t, 200, w.Code, w.Body.String())
	w = request(http.MethodGet, url, "")
	require.Equal(t, 200, w.Code)
	w = request(http.MethodGet, "/me/private-notes/export", "")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), "不应公开")
	w = request(http.MethodDelete, url+"?base_version=1", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	w = request(http.MethodGet, "/me/private-notes?trash=true", "")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), id)
	w = request(http.MethodPost, url+"/restore", `{"base_version":2}`)
	require.Equal(t, 200, w.Code, w.Body.String())
	w = request(http.MethodGet, "/me/private-notes?trash=bad", "")
	require.Equal(t, 400, w.Code)
}
