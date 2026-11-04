package books

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestWorkNotesAcrossConcreteFilesAndPrivateChapterNotes(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	workID := uuid.MustParse(work.ID)
	assetOld, oldRevision := readyBookReadingFile(t, service, user, workID, "zh", "txt")
	_, newRevision := readyBookReadingFile(t, service, user, workID, "zh", "txt")
	oldMark, err := service.SaveBookReadingMark(ctx, user, oldRevision.ID, uuid.New(), txtBookHighlight(1, 8, "旧电子书的批注"))
	require.NoError(t, err)
	newMark, err := service.SaveBookReadingMark(ctx, user, newRevision.ID, uuid.New(), txtBookHighlight(10, 20, "另一个文件的批注"))
	require.NoError(t, err)
	_, err = service.SaveBookReadingMark(ctx, user, newRevision.ID, uuid.New(), SaveBookReadingMarkInput{Kind: "bookmark", Label: "书签", Anchor: BookReadingMarkAnchor{TXTOffsetStart: 15}})
	require.NoError(t, err)
	chapterID := uuid.New()
	_, err = service.SaveBookPrivateNote(ctx, user, chapterID, SaveBookPrivateNoteInput{WorkID: workID, Title: "第二章独立笔记", Body: "不依附具体划线的思考", SourceRevisionID: &oldRevision.ID})
	require.NoError(t, err)
	timeline, err := service.ListWorkNotes(ctx, user, workID, WorkNotesFilter{})
	require.NoError(t, err)
	require.EqualValues(t, 3, timeline.Total, "two highlights and one independent note, not bookmarks")
	require.Len(t, timeline.Items, 3)
	found := map[uuid.UUID]WorkNoteItem{}
	for _, item := range timeline.Items {
		found[item.ID] = item
	}
	require.Equal(t, "highlight", found[oldMark.ID].Type)
	require.Equal(t, oldRevision.ID, found[oldMark.ID].Source.RevisionID)
	require.Equal(t, assetOld.ID, found[oldMark.ID].Source.AssetID)
	require.NotNil(t, found[oldMark.ID].Anchor)
	require.True(t, found[oldMark.ID].Source.CanOpen)
	require.Equal(t, "highlight", found[newMark.ID].Type)
	require.Equal(t, newRevision.ID, found[newMark.ID].Source.RevisionID)
	require.Equal(t, "chapter_note", found[chapterID].Type)
	require.Equal(t, "第二章独立笔记", found[chapterID].Title)
	require.Nil(t, found[chapterID].Anchor, "independent note has no invented precise text anchor")
	page, err := service.ListWorkNotes(ctx, user, workID, WorkNotesFilter{Kind: "highlight", Limit: 1})
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total)
	require.Len(t, page.Items, 1)
	rest, err := service.ListWorkNotes(ctx, user, workID, WorkNotesFilter{Kind: "highlight", Limit: 1, Offset: 1})
	require.NoError(t, err)
	require.Len(t, rest.Items, 1)
	require.NotEqual(t, page.Items[0].ID, rest.Items[0].ID)
	onlyNew, err := service.ListWorkNotes(ctx, user, workID, WorkNotesFilter{RevisionID: newRevision.ID})
	require.NoError(t, err)
	require.EqualValues(t, 1, onlyNew.Total)
	require.Equal(t, newMark.ID, onlyNew.Items[0].ID)
	onlyIndependent, err := service.ListWorkNotes(ctx, user, workID, WorkNotesFilter{Kind: "chapter_note"})
	require.NoError(t, err)
	require.EqualValues(t, 1, onlyIndependent.Total)
	require.Equal(t, chapterID, onlyIndependent.Items[0].ID)
	// A historical book can disappear without erasing the reader's own notes.
	require.NoError(t, service.db.Model(&assetOld).Update("status", "removed").Error)
	afterRemoval, err := service.ListWorkNotes(ctx, user, workID, WorkNotesFilter{})
	require.NoError(t, err)
	require.EqualValues(t, 3, afterRemoval.Total)
	for _, item := range afterRemoval.Items {
		if item.Source != nil && item.Source.RevisionID == oldRevision.ID {
			require.False(t, item.Source.CanOpen)
		}
	}
	other := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	isolated, err := service.ListWorkNotes(ctx, other, workID, WorkNotesFilter{})
	require.NoError(t, err)
	require.Zero(t, isolated.Total)
	require.Empty(t, isolated.Items)
	_, err = service.ListWorkNotes(ctx, user, workID, WorkNotesFilter{Kind: "invalid"})
	require.Error(t, err)
}

func TestWorkNotesPrivateHTTP(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	_, revision := readyBookReadingFile(t, service, user, uuid.MustParse(work.ID), "zh", "txt")
	_, err := service.SaveBookReadingMark(context.Background(), user, revision.ID, uuid.New(), txtBookHighlight(1, 3, "私有记录"))
	require.NoError(t, err)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { authctx.SetCurrentUser(c, user); c.Next() })
	RegisterRoutes(router.Group("/api/v1/books"), service)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/books/me/works/"+work.ID+"/notes?kind=highlight&limit=10", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
	require.Contains(t, response.Body.String(), "私有记录")
	require.NotContains(t, response.Body.String(), user.ID.String())
}

func TestWorkNotesRevocationAndDeletedSourceRemainPrivate(t *testing.T) {
	service, owner, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	_, revision := readyBookReadingFile(t, service, owner, uuid.MustParse(work.ID), "zh", "txt")
	viewerModel := model.User{UUID: uuid.New(), Username: "work-notes-reader", Email: "work-notes-reader@example.test", Password: "hash", IsActive: true}
	require.NoError(t, service.db.Create(&viewerModel).Error)
	viewer := authctx.CurrentUser{ID: viewerModel.UUID, Role: authctx.RoleUser}
	_, err := service.SetBookFileVisibility(ctx, owner, revision.ID, true, true)
	require.NoError(t, err)
	recorded, err := service.SaveBookReadingMark(ctx, viewer, revision.ID, uuid.New(), txtBookHighlight(4, 12, "曾公开文件的标注"))
	require.NoError(t, err)
	_, err = service.SetBookFileVisibility(ctx, owner, revision.ID, false, false)
	require.NoError(t, err)
	result, err := service.ListWorkNotes(ctx, viewer, uuid.MustParse(work.ID), WorkNotesFilter{})
	require.NoError(t, err)
	require.EqualValues(t, 1, result.Total)
	require.Equal(t, recorded.ID, result.Items[0].ID)
	require.NotNil(t, result.Items[0].Source)
	require.False(t, result.Items[0].Source.CanOpen)
	require.Equal(t, "来源文件不可访问", result.Items[0].Source.FileName)
	require.Empty(t, result.Items[0].Source.Format)
	_, err = service.SaveBookReadingMark(ctx, viewer, revision.ID, uuid.New(), txtBookHighlight(15, 20, "不应创建"))
	require.Error(t, err, "revoked private file cannot acquire new marks")
	_, err = service.SaveBookPrivateNote(ctx, viewer, uuid.New(), SaveBookPrivateNoteInput{WorkID: uuid.MustParse(work.ID), SourceRevisionID: &revision.ID, Body: "尝试绑定私有文件"})
	require.Error(t, err, "private note cannot attach a file the viewer may not read")
	_, err = service.SaveBookReadingMark(ctx, owner, revision.ID, uuid.New(), txtBookHighlight(22, 31, "文件所有者自己的标注"))
	require.NoError(t, err)
	require.NoError(t, service.db.Model(&model.BookAssetRevision{}).Where("id = ?", revision.ID).Update("deleted_at", time.Now()).Error)
	ownerTimeline, err := service.ListWorkNotes(ctx, owner, uuid.MustParse(work.ID), WorkNotesFilter{})
	require.NoError(t, err)
	require.EqualValues(t, 1, ownerTimeline.Total)
	require.False(t, ownerTimeline.Items[0].Source.CanOpen, "soft-deleted file cannot be opened")
}
