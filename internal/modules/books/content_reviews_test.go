package books

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/modules/blog"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func createBookTestReviewContent(t *testing.T, service *Service, user authctx.CurrentUser, channel model.Channel, status, visibility string) blog.BlogContent {
	t.Helper()
	content, err := blog.NewService(service.db).CreateBlogContent(user, blog.CreatePostRequest{Title: "长评标题", Content: "唯一一份的长评正文", Summary: "长评摘要", ChannelID: channel.ID, Status: status, Visibility: visibility})
	require.NoError(t, err)
	return content
}

func TestBookContentReviewsUseCanonicalPublicationStateAndNoCopiedBody(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	channel := prepareBookNotePublicationTables(t, service, user)
	content := createBookTestReviewContent(t, service, user, channel, "draft", "public")
	workID := uuid.MustParse(work.ID)
	link, err := service.LinkBookContentReview(ctx, user, workID, content.ID, LinkBookContentReviewInput{Spoiler: true})
	require.NoError(t, err)
	require.Equal(t, content.ID, link.ContentID)
	list, err := service.ListBookContentReviews(ctx, workID, 20, 0)
	require.NoError(t, err)
	require.Empty(t, list.Items, "linking a draft must not publish it")
	require.NoError(t, service.db.Model(&model.ContentEntry{}).Where("id = ?", content.ID).Updates(map[string]any{"status": "published", "visibility": "public"}).Error)
	list, err = service.ListBookContentReviews(ctx, workID, 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, list.Total)
	require.True(t, list.Items[0].Spoiler)
	require.Equal(t, "blog_post", list.Items[0].TargetKind)
	require.Equal(t, "/post/"+content.ID.String(), list.Items[0].TargetPath)
	require.Equal(t, "/api/v1/discussions/blog_post/"+content.ID.String()+"/comments", list.Items[0].CommentsPath)
	require.NoError(t, service.db.Model(&model.ContentEntry{}).Where("id = ?", content.ID).Update("title", "直接编辑统一内容标题").Error)
	list, err = service.ListBookContentReviews(ctx, workID, 20, 0)
	require.NoError(t, err)
	require.Equal(t, "直接编辑统一内容标题", list.Items[0].Title)
	for _, changes := range []map[string]any{{"visibility": "private"}, {"visibility": "followers"}, {"visibility": "public", "status": "draft"}} {
		require.NoError(t, service.db.Model(&model.ContentEntry{}).Where("id = ?", content.ID).Updates(changes).Error)
		list, err = service.ListBookContentReviews(ctx, workID, 20, 0)
		require.NoError(t, err)
		require.Empty(t, list.Items)
	}
	var count int64
	require.NoError(t, service.db.Model(&model.Post{}).Count(&count).Error)
	require.Zero(t, count, "no legacy blog authority is created")
	require.NoError(t, service.db.Model(&model.ContentBlogExtension{}).Where("content_id = ?", content.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, service.db.Delete(&model.ContentEntry{}, "id = ?", content.ID).Error)
	list, err = service.ListBookContentReviews(ctx, workID, 20, 0)
	require.NoError(t, err)
	require.Empty(t, list.Items)
	require.NoError(t, service.UnlinkBookContentReview(ctx, user, workID, content.ID))
	require.NoError(t, service.db.Model(&model.BookContentReview{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestBookContentReviewsOwnershipConcurrencyAndMergeUndo(t *testing.T) {
	service, user, source, target := newBookMergeTestService(t)
	ctx := context.Background()
	channel := prepareBookNotePublicationTables(t, service, user)
	content := createBookTestReviewContent(t, service, user, channel, "published", "public")
	sourceID, targetID := uuid.MustParse(source.ID), uuid.MustParse(target.ID)
	other := newBookLibraryTestReader(t, service)
	_, err := service.LinkBookContentReview(ctx, other, sourceID, content.ID, LinkBookContentReviewInput{})
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	start := make(chan struct{})
	errors := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := service.LinkBookContentReview(ctx, user, sourceID, content.ID, LinkBookContentReviewInput{})
			errors <- err
		}()
	}
	close(start)
	for range 2 {
		require.NoError(t, <-errors)
	}
	_, err = service.LinkBookContentReview(ctx, user, targetID, content.ID, LinkBookContentReviewInput{})
	require.Equal(t, 409, apperr.FromError(err).HTTPStatus)
	require.NoError(t, service.UnlinkBookContentReview(ctx, other, sourceID, content.ID))
	list, err := service.ListBookContentReviews(ctx, sourceID, 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, list.Total)
	merge := mergeBookTestWorks(t, service, user, source, target)
	list, err = service.ListBookContentReviews(ctx, targetID, 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, list.Total)
	link, err := service.LinkBookContentReview(ctx, user, targetID, content.ID, LinkBookContentReviewInput{Spoiler: true})
	require.NoError(t, err)
	require.Equal(t, sourceID, link.WorkID, "merge aliases do not relocate the review")
	_, err = service.RevertBookWorkMerge(ctx, user, merge.ID)
	require.NoError(t, err)
	list, err = service.ListBookContentReviews(ctx, sourceID, 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, list.Total)
	require.True(t, list.Items[0].Spoiler, "undo must not roll back independent content metadata")
	list, err = service.ListBookContentReviews(ctx, targetID, 20, 0)
	require.NoError(t, err)
	require.Empty(t, list.Items)
	require.NoError(t, service.UnlinkBookContentReview(ctx, user, sourceID, content.ID))
	var entry model.ContentEntry
	require.NoError(t, service.db.First(&entry, "id = ?", content.ID).Error, "unlink must not delete content")
}

func TestBookContentReviewHTTP(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	channel := prepareBookNotePublicationTables(t, service, user)
	content := createBookTestReviewContent(t, service, user, channel, "published", "public")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { authctx.SetCurrentUser(c, user) })
	RegisterRoutes(router.Group("/api/v1/books"), service)
	url := "/api/v1/books/works/" + work.ID + "/reviews/" + content.ID.String()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	response := request(http.MethodPut, url, `{"spoiler":true}`)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
	require.NotContains(t, response.Body.String(), user.ID.String())
	response = request(http.MethodGet, "/api/v1/books/works/"+work.ID+"/reviews", "")
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), content.ID.String())
	require.Equal(t, 200, request(http.MethodDelete, url, "").Code)
}
