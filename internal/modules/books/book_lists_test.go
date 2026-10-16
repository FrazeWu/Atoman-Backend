package books

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func bookListTestInput(workID uuid.UUID) SaveBookListDraftInput {
	return SaveBookListDraftInput{BookListSnapshot: BookListSnapshot{Title: "历史入门书单", Description: "循序渐进的阅读建议",
		Items: []BookListEntry{{WorkID: workID, Recommendation: " 从这本开始 "}}}}
}

func TestBookListDraftAndPublicSnapshotsRemainSeparate(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	id := uuid.New()
	input := bookListTestInput(uuid.MustParse(work.ID))
	draft, err := service.SaveBookListDraft(ctx, user, id, input)
	require.NoError(t, err)
	require.EqualValues(t, 1, draft.Version)
	require.Equal(t, "private", draft.Visibility)
	require.Equal(t, "从这本开始", draft.Items[0].Recommendation)
	retry, err := service.SaveBookListDraft(ctx, user, id, input)
	require.NoError(t, err)
	require.Equal(t, draft.Version, retry.Version)
	_, err = service.GetBookList(ctx, authctx.CurrentUser{}, id)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	other := newBookLibraryTestReader(t, service)
	_, err = service.GetBookListDraft(ctx, other, id)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	_, err = service.SaveBookListDraft(ctx, other, id, input)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus, "a client UUID cannot overwrite another user's list")
	draft, err = service.PublishBookList(ctx, user, id, PublishBookListInput{BaseVersion: 1, Visibility: "public"})
	require.NoError(t, err)
	require.EqualValues(t, 2, draft.Version)
	require.EqualValues(t, 1, draft.PublishedVersion)
	retry, err = service.PublishBookList(ctx, user, id, PublishBookListInput{BaseVersion: 1, Visibility: "public"})
	require.NoError(t, err)
	require.EqualValues(t, 1, retry.PublishedVersion)
	before, err := service.GetBookList(ctx, authctx.CurrentUser{}, id)
	require.NoError(t, err)
	require.Equal(t, input.Title, before.Title)
	require.Equal(t, 1, before.Changes.ItemsAdded)
	input.BaseVersion = 2
	input.Title = "未公开的草稿标题"
	input.Description = "尚未公开的私人简介"
	input.Items[0].Recommendation = "尚未公开的私人推荐语"
	draft, err = service.SaveBookListDraft(ctx, user, id, input)
	require.NoError(t, err)
	require.EqualValues(t, 3, draft.Version)
	after, err := service.GetBookList(ctx, other, id)
	require.NoError(t, err)
	require.Equal(t, before, after, "saving a draft must not change any public projection field")
	encoded, err := json.Marshal(after)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "未公开")
	require.NotContains(t, string(encoded), "updated_at")
	require.NotContains(t, string(encoded), `"version"`)
	search, err := service.SearchBookLists(ctx, "未公开", 20, 0)
	require.NoError(t, err)
	require.Zero(t, search.Total)
	_, err = service.PublishBookList(ctx, user, id, PublishBookListInput{BaseVersion: 2, Visibility: "public"})
	require.Equal(t, 409, apperr.FromError(err).HTTPStatus)
	draft, err = service.PublishBookList(ctx, user, id, PublishBookListInput{BaseVersion: 3, Visibility: "public"})
	require.NoError(t, err)
	require.EqualValues(t, 4, draft.Version)
	after, err = service.GetBookList(ctx, other, id)
	require.NoError(t, err)
	require.EqualValues(t, 2, after.PublishedVersion)
	require.Equal(t, 1, after.Changes.RecommendationsChanged)
	require.True(t, after.Changes.MetadataChanged)
	search, err = service.SearchBookLists(ctx, "未公开", 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, search.Total)
}

func TestBookListBookmarksOnlyAcknowledgePublicationsAndDoNotGrantAccess(t *testing.T) {
	service, owner, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	id := uuid.New()
	input := bookListTestInput(uuid.MustParse(work.ID))
	_, err := service.SaveBookListDraft(ctx, owner, id, input)
	require.NoError(t, err)
	_, err = service.PublishBookList(ctx, owner, id, PublishBookListInput{BaseVersion: 1, Visibility: "public"})
	require.NoError(t, err)
	reader := newBookLibraryTestReader(t, service)
	one := int64(1)
	for range 2 {
		bookmark, err := service.SaveBookListBookmark(ctx, reader, id, SaveBookListBookmarkInput{SeenVersion: &one})
		require.NoError(t, err)
		require.False(t, bookmark.HasUpdates)
	}
	input.BaseVersion = 2
	input.Description = "私下修改，尚未发布"
	_, err = service.SaveBookListDraft(ctx, owner, id, input)
	require.NoError(t, err)
	bookmarks, err := service.ListBookListBookmarks(ctx, reader, 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, bookmarks.Total)
	require.False(t, bookmarks.Items[0].HasUpdates)
	require.NotEqual(t, input.Description, bookmarks.Items[0].List.Description)
	_, err = service.PublishBookList(ctx, owner, id, PublishBookListInput{BaseVersion: 3, Visibility: "public"})
	require.NoError(t, err)
	bookmarks, err = service.ListBookListBookmarks(ctx, reader, 20, 0)
	require.NoError(t, err)
	require.True(t, bookmarks.Items[0].HasUpdates)
	require.True(t, bookmarks.Items[0].List.Changes.MetadataChanged)
	for _, invalid := range []int64{-1, 3} {
		_, err = service.SaveBookListBookmark(ctx, reader, id, SaveBookListBookmarkInput{SeenVersion: &invalid})
		require.Equal(t, 400, apperr.FromError(err).HTTPStatus)
	}
	two := int64(2)
	_, err = service.SaveBookListBookmark(ctx, reader, id, SaveBookListBookmarkInput{SeenVersion: &two})
	require.NoError(t, err)
	bookmark, err := service.SaveBookListBookmark(ctx, reader, id, SaveBookListBookmarkInput{SeenVersion: &one})
	require.NoError(t, err)
	require.EqualValues(t, 2, bookmark.SeenVersion)
	require.False(t, bookmark.HasUpdates, "a delayed acknowledgment must not move the reader backwards")
	bookmarks, err = service.ListBookListBookmarks(ctx, owner, 20, 0)
	require.NoError(t, err)
	require.Empty(t, bookmarks.Items, "bookmarks are private")
	require.NoError(t, service.WithdrawBookList(ctx, owner, id, 4, false))
	_, err = service.GetBookList(ctx, reader, id)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	_, err = service.SaveBookListBookmark(ctx, reader, id, SaveBookListBookmarkInput{})
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	bookmarks, err = service.ListBookListBookmarks(ctx, reader, 20, 0)
	require.NoError(t, err)
	require.Empty(t, bookmarks.Items)
	_, err = service.GetBookList(ctx, owner, id)
	require.NoError(t, err, "withdrawal keeps the owner's published copy")
	search, err := service.SearchBookLists(ctx, "", 20, 0)
	require.NoError(t, err)
	require.Empty(t, search.Items)
	require.NoError(t, service.DeleteBookListBookmark(ctx, reader, id))
}

func TestBookListConcurrentDraftsDeletionAndValidation(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	id := uuid.New()
	input := bookListTestInput(uuid.MustParse(work.ID))
	_, err := service.SaveBookListDraft(ctx, user, id, input)
	require.NoError(t, err)
	start := make(chan struct{})
	errors := make(chan error, 2)
	for _, title := range []string{"设备 A 草稿", "设备 B 草稿"} {
		go func(title string) {
			<-start
			local := input
			local.BaseVersion, local.Title = 1, title
			_, err := service.SaveBookListDraft(ctx, user, id, local)
			errors <- err
		}(title)
	}
	close(start)
	success, conflict := 0, 0
	for range 2 {
		if err := <-errors; err == nil {
			success++
		} else {
			require.Equal(t, 409, apperr.FromError(err).HTTPStatus)
			conflict++
		}
	}
	require.Equal(t, 1, success)
	require.Equal(t, 1, conflict)
	require.Equal(t, " 从这本开始 ", input.Items[0].Recommendation, "normalization must not mutate the caller's slice")
	for _, invalid := range []BookListSnapshot{
		{Title: " "}, {Title: "无效图书", Items: []BookListEntry{{WorkID: uuid.New()}}},
		{Title: "重复图书", Items: []BookListEntry{{WorkID: uuid.MustParse(work.ID)}, {WorkID: uuid.MustParse(work.ID)}}},
	} {
		_, err := service.SaveBookListDraft(ctx, user, uuid.New(), SaveBookListDraftInput{BookListSnapshot: invalid})
		require.Equal(t, 400, apperr.FromError(err).HTTPStatus)
	}
	_, err = service.PublishBookList(ctx, user, id, PublishBookListInput{BaseVersion: 2, Visibility: "unknown"})
	require.Equal(t, 400, apperr.FromError(err).HTTPStatus)
	require.Equal(t, 409, apperr.FromError(service.WithdrawBookList(ctx, user, id, 1, true)).HTTPStatus)
	require.NoError(t, service.WithdrawBookList(ctx, user, id, 2, true))
	_, err = service.GetBookListDraft(ctx, user, id)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	_, err = service.SaveBookListDraft(ctx, user, id, input)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus, "deleted client IDs must not resurrect a list")
	items, count, err := service.ListMyBookLists(ctx, user, 20, 0)
	require.NoError(t, err)
	require.Zero(t, count)
	require.Empty(t, items)
}

func TestBookListEntriesKeepRecommendationsAcrossMergeUndoAndRemoval(t *testing.T) {
	service, user, source, target := newBookMergeTestService(t)
	ctx := context.Background()
	sourceID, targetID := uuid.MustParse(source.ID), uuid.MustParse(target.ID)
	id := uuid.New()
	input := SaveBookListDraftInput{BookListSnapshot: BookListSnapshot{Title: "作品合并验证", Items: []BookListEntry{
		{WorkID: sourceID, Recommendation: "原书推荐语"}, {WorkID: targetID, Recommendation: "目标书推荐语"},
	}}}
	_, err := service.SaveBookListDraft(ctx, user, id, input)
	require.NoError(t, err)
	_, err = service.PublishBookList(ctx, user, id, PublishBookListInput{BaseVersion: 1, Visibility: "public"})
	require.NoError(t, err)
	merge := mergeBookTestWorks(t, service, user, source, target)
	detail, err := service.GetBookList(ctx, authctx.CurrentUser{}, id)
	require.NoError(t, err)
	require.Len(t, detail.Items, 2, "a later work merge must not silently discard an endorsement")
	require.Equal(t, sourceID, detail.Items[0].WorkID)
	require.Equal(t, targetID, *detail.Items[0].CanonicalWorkID)
	require.Equal(t, "原书推荐语", detail.Items[0].Recommendation)
	input.BaseVersion = 2
	_, err = service.SaveBookListDraft(ctx, user, id, input)
	require.Equal(t, "books.duplicate_list_work", apperr.FromError(err).Code)
	_, err = service.RevertBookWorkMerge(ctx, user, merge.ID)
	require.NoError(t, err)
	detail, err = service.GetBookList(ctx, authctx.CurrentUser{}, id)
	require.NoError(t, err)
	require.Equal(t, sourceID, *detail.Items[0].CanonicalWorkID)
	require.NoError(t, service.db.Model(&model.BookWork{}).Where("id = ?", sourceID).Update("lifecycle_status", model.BookLifecycleStatusRetired).Error)
	detail, err = service.GetBookList(ctx, authctx.CurrentUser{}, id)
	require.NoError(t, err)
	require.False(t, detail.Items[0].Available)
	require.Nil(t, detail.Items[0].CanonicalWorkID)
	require.Empty(t, detail.Items[0].Title)
	require.Equal(t, "原书推荐语", detail.Items[0].Recommendation)
}

func TestBookListPrivatePublicationSearchAndEmptyDraft(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	id := uuid.New()
	input := bookListTestInput(uuid.MustParse(work.ID))
	input.Title = "包含 100%_字样"
	_, err := service.SaveBookListDraft(ctx, user, id, input)
	require.NoError(t, err)
	_, err = service.PublishBookList(ctx, user, id, PublishBookListInput{BaseVersion: 1, Visibility: "private"})
	require.NoError(t, err)
	_, err = service.GetBookList(ctx, authctx.CurrentUser{}, id)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	_, err = service.GetBookList(ctx, user, id)
	require.NoError(t, err)
	search, err := service.SearchBookLists(ctx, "", 20, 0)
	require.NoError(t, err)
	require.Zero(t, search.Total)
	_, err = service.PublishBookList(ctx, user, id, PublishBookListInput{BaseVersion: 2, Visibility: "public"})
	require.NoError(t, err)
	search, err = service.SearchBookLists(ctx, "%_", 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, search.Total)
	search, err = service.SearchBookLists(ctx, "%other", 20, 0)
	require.NoError(t, err)
	require.Zero(t, search.Total)
	search, err = service.SearchBookLists(ctx, "", 1, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, search.Total)
	require.Empty(t, search.Items)
	emptyID := uuid.New()
	_, err = service.SaveBookListDraft(ctx, user, emptyID, SaveBookListDraftInput{BookListSnapshot: BookListSnapshot{Title: "空草稿"}})
	require.NoError(t, err)
	_, err = service.PublishBookList(ctx, user, emptyID, PublishBookListInput{BaseVersion: 1, Visibility: "public"})
	require.Equal(t, 400, apperr.FromError(err).HTTPStatus)
}

func TestBookListHTTPPrivacyAndWorkflow(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	id := uuid.New()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if c.GetHeader("X-Test-User") != "" {
			authctx.SetCurrentUser(c, user)
		}
	})
	RegisterRoutes(router.Group("/api/v1/books"), service)
	request := func(method, path, body string, login bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if login {
			req.Header.Set("X-Test-User", "1")
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	base := "/api/v1/books/me/booklists/" + id.String()
	public := "/api/v1/books/booklists/" + id.String()
	body, err := json.Marshal(bookListTestInput(uuid.MustParse(work.ID)))
	require.NoError(t, err)
	require.Equal(t, 401, request(http.MethodPut, base+"/draft", string(body), false).Code)
	response := request(http.MethodPut, base+"/draft", string(body), true)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
	require.Equal(t, 401, request(http.MethodGet, base+"/draft", "", false).Code)
	require.Equal(t, 404, request(http.MethodGet, public, "", false).Code)
	response = request(http.MethodPost, base+"/publish", `{"base_version":1,"visibility":"public"}`, true)
	require.Equal(t, 200, response.Code, response.Body.String())
	response = request(http.MethodGet, public, "", false)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.NotContains(t, response.Body.String(), "draft")
	for _, path := range []string{"/api/v1/books/me/booklists", "/api/v1/books/me/booklist-bookmarks", "/api/v1/books/booklists?q=历史"} {
		require.Equal(t, 200, request(http.MethodGet, path, "", true).Code)
	}
	require.Equal(t, 200, request(http.MethodPut, public+"/bookmark", `{"seen_version":1}`, true).Code)
	require.Equal(t, 200, request(http.MethodDelete, public+"/bookmark", "", true).Code)
	require.Equal(t, 200, request(http.MethodPost, base+"/unpublish", `{"base_version":2}`, true).Code)
	require.Equal(t, 404, request(http.MethodGet, public, "", false).Code)
	require.Equal(t, 200, request(http.MethodDelete, base+"?base_version=3", "", true).Code)
}
