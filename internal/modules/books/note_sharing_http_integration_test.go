package books

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"atoman/internal/platform/authctx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBookNoteSharingAndTrashRealHTTPHandlers(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	workID := uuid.MustParse(work.ID)
	note, err := service.SaveBookPrivateNote(ctx, user, uuid.New(), SaveBookPrivateNoteInput{WorkID: workID, Title: "私人的读后感", Body: "这是本人原创的精简见解"})
	require.NoError(t, err)
	_, rev := readyBookReadingFile(t, service, user, workID, "zh", "txt")
	mark, err := service.SaveBookReadingMark(ctx, user, rev.ID, uuid.New(), txtBookHighlight(4, 12, "恢复原始注释"))
	require.NoError(t, err)
	collectionID := uuid.New()
	_, err = service.SaveNoteCollection(ctx, user, collectionID, SaveNoteCollectionInput{WorkID: workID, NoteCollectionDraft: NoteCollectionDraft{Title: "短篇摘录", Description: "即将发布", Items: []BookNoteReference{{Kind: "chapter_note", ID: note.ID}}}})
	require.NoError(t, err)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	other := authctx.CurrentUser{ID: uuid.New()}
	router.Use(func(c *gin.Context) {
		switch c.GetHeader("X-Test-Auth") {
		case "owner":
			authctx.SetCurrentUser(c, user)
		case "other":
			authctx.SetCurrentUser(c, other)
		}
		c.Next()
	})
	RegisterRoutes(router.Group("/api/v1/books"), service)
	request := func(method, path, body, identity string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1/books"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Test-Auth", identity)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	payload := `{"work_id":"` + work.ID + `","title":"短篇摘录","description":"即将发布","items":[{"kind":"chapter_note","id":"` + note.ID.String() + `"}]}`
	w := request(http.MethodPost, "/me/note-collections/preview", payload, "owner")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
	var preview struct {
		Data NotePreviewResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &preview))
	require.Len(t, preview.Data.Digest, 64)
	w = request(http.MethodPost, "/me/note-collections/preview", payload, "other")
	require.NotEqual(t, 200, w.Code)
	publish := func(digest string) string {
		return `{"base_version":1,"request_id":"` + uuid.NewString() + `","visibility":"link","confirm_publish":true,"preview_digest":"` + digest + `"}`
	}
	path := "/me/note-collections/" + collectionID.String()
	w = request(http.MethodPost, path+"/publish", publish("invalid"), "owner")
	require.Equal(t, 409, w.Code, w.Body.String())
	w = request(http.MethodPost, path+"/publish", publish(preview.Data.Digest), "owner")
	require.Equal(t, 200, w.Code, w.Body.String())
	var published struct {
		Data NoteCollectionDTO `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &published))
	require.NotEmpty(t, published.Data.SharePath)
	shareKey := strings.TrimPrefix(published.Data.SharePath, "/books/note-shares/")
	w = request(http.MethodGet, "/note-shares/"+shareKey, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, "noindex, nofollow", w.Header().Get("X-Robots-Tag"))
	require.Contains(t, w.Body.String(), "这是本人原创的精简见解")
	require.NotContains(t, w.Body.String(), note.ID.String())
	w = request(http.MethodGet, path+"/history/1", "", "other")
	require.Equal(t, 404, w.Code, w.Body.String())
	w = request(http.MethodGet, path+"/history/1", "", "owner")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
	w = request(http.MethodPost, path+"/revoke", `{"base_version":2}`, "owner")
	require.Equal(t, 200, w.Code, w.Body.String())
	w = request(http.MethodGet, "/note-shares/"+shareKey, "", "")
	require.Equal(t, 404, w.Code, w.Body.String())
	w = request(http.MethodGet, path+"/history/1", "", "owner")
	require.Equal(t, 200, w.Code, "history stays owner-only after revoke")
	deletedNote, err := service.DeleteBookPrivateNote(ctx, user, note.ID, note.Version)
	require.NoError(t, err)
	noteErase := "/me/private-notes/" + note.ID.String() + "/erase"
	w = request(http.MethodPost, noteErase, `{"base_version":2}`, "owner")
	require.Equal(t, 400, w.Code)
	w = request(http.MethodPost, noteErase, `{"base_version":`+strconv.FormatInt(deletedNote.Version, 10)+`,"confirm_erase":true}`, "owner")
	require.Equal(t, 200, w.Code, w.Body.String())
	w = request(http.MethodGet, path+"/history/1", "", "owner")
	require.Equal(t, 200, w.Code, "old frozen history is still private and immutable")
	_, err = service.DeleteBookReadingMark(ctx, user, rev.ID, mark.ID, mark.Version)
	require.NoError(t, err)
	w = request(http.MethodGet, "/me/reading-marks/trash", "", "owner")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "恢复原始注释")
	markRoute := "/me/reading-marks/" + rev.ID.String() + "/" + mark.ID.String()
	w = request(http.MethodPost, markRoute+"/restore", `{"base_version":2}`, "other")
	require.Equal(t, 404, w.Code)
	w = request(http.MethodPost, markRoute+"/restore", `{"base_version":2}`, "owner")
	require.Equal(t, 200, w.Code, w.Body.String())
	_, err = service.DeleteBookReadingMark(ctx, user, rev.ID, mark.ID, 3)
	require.NoError(t, err)
	w = request(http.MethodPost, markRoute+"/erase", `{"base_version":4}`, "owner")
	require.Equal(t, 400, w.Code)
	w = request(http.MethodPost, markRoute+"/erase", `{"base_version":4,"confirm_erase":true}`, "owner")
	require.Equal(t, 200, w.Code, w.Body.String())
	w = request(http.MethodGet, "/me/reading-marks/trash", "", "owner")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), "恢复原始注释")
}
