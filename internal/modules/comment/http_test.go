package comment

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func newCommentHTTPRouter(t *testing.T) (*gin.Engine, *Service) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.MediaAsset{}, &model.DiscussionTarget{}, &model.CommentEntry{},
		&model.CommentMention{}, &model.CommentAttachment{}, &model.CommentLike{},
		&model.CommentReport{}, &model.CommentTimeAnchor{}, &model.CommentPublishRecord{},
		&model.Notification{}, &model.AuditLog{},
	)
	service := NewService(db, &TargetRegistry{resolvers: map[string]TargetResolver{
		TargetKindBlogPost: targetResolverFunc(func(_ Viewer, id uuid.UUID) (ResolvedTarget, error) {
			return ResolvedTarget{Kind: TargetKindBlogPost, ResourceID: id, ResourceKey: id.String(), Visible: true, MarkLabel: "置顶"}, nil
		}),
	}})
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1"), service)
	return router, service
}

func TestCommentHTTPPublicRoutesAndMutationAuth(t *testing.T) {
	router, _ := newCommentHTTPRouter(t)
	resourceID := uuid.NewString()
	cases := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/api/v1/discussions/blog_post/" + resourceID + "/comments", http.StatusOK},
		{http.MethodGet, "/api/v1/comments/" + uuid.NewString() + "/replies", http.StatusNotFound},
		{http.MethodPost, "/api/v1/discussions/blog_post/" + resourceID + "/comments", http.StatusUnauthorized},
		{http.MethodPatch, "/api/v1/comments/" + uuid.NewString(), http.StatusUnauthorized},
		{http.MethodDelete, "/api/v1/comments/" + uuid.NewString(), http.StatusUnauthorized},
		{http.MethodPut, "/api/v1/comments/" + uuid.NewString() + "/like", http.StatusUnauthorized},
		{http.MethodDelete, "/api/v1/comments/" + uuid.NewString() + "/like", http.StatusUnauthorized},
		{http.MethodPut, "/api/v1/comments/" + uuid.NewString() + "/report", http.StatusUnauthorized},
		{http.MethodPut, "/api/v1/discussions/blog_post/" + resourceID + "/pinned-comment", http.StatusUnauthorized},
		{http.MethodDelete, "/api/v1/discussions/blog_post/" + resourceID + "/pinned-comment", http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/admin/comment-reports", http.StatusUnauthorized},
		{http.MethodPut, "/api/v1/admin/comments/" + uuid.NewString() + "/moderation", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			router.ServeHTTP(w, req)
			require.Equal(t, tc.want, w.Code, w.Body.String())
			if tc.want == http.StatusUnauthorized {
				require.Contains(t, w.Body.String(), `"code":"auth.unauthorized"`)
			}
			if tc.want != http.StatusNotFound {
				require.NotEqual(t, http.StatusNotFound, w.Code, "route was not mounted")
			}
		})
	}
}

func TestCommentHTTPRejectsInvalidPathPaginationAndUnknownJSON(t *testing.T) {
	router, _ := newCommentHTTPRouter(t)
	cases := []string{
		"/api/v1/discussions/unknown/" + uuid.NewString() + "/comments",
		"/api/v1/discussions/blog_post/not-a-uuid/comments",
		"/api/v1/discussions/blog_post/" + uuid.NewString() + "/comments?page=0",
		"/api/v1/discussions/blog_post/" + uuid.NewString() + "/comments?page=abc",
		"/api/v1/discussions/blog_post/" + uuid.NewString() + "/comments?sort=bad",
		"/api/v1/discussions/blog_post/" + uuid.NewString() + "/comments?page_size=0",
		"/api/v1/discussions/blog_post/" + uuid.NewString() + "/comments?page_size=-1",
		"/api/v1/discussions/blog_post/" + uuid.NewString() + "/comments?page_size=abc",
	}
	for _, path := range cases {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.NotEmpty(t, body.Error.Code)
	}
}

func TestCommentHTTPRootPageSizeIsReturnedAndCapped(t *testing.T) {
	router, _ := newCommentHTTPRouter(t)
	resourceID := uuid.NewString()
	for raw, want := range map[string]int{"5": 5, "21": 20} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/discussions/blog_post/"+resourceID+"/comments?page_size="+raw, nil))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var body struct {
			Data struct {
				PerPage int `json:"per_page"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, want, body.Data.PerPage)
	}
}

func TestCommentHTTPStrictJSONDecoderRejectsUnknownAndTrailingValues(t *testing.T) {
	var input CreateCommentInput
	require.Error(t, decodeJSONStrict(newStrictJSONTestContext(`{"content":"ok","root_id":"`+uuid.NewString()+`"}`, "application/json"), &input))
	require.Error(t, decodeJSONStrict(newStrictJSONTestContext(`{"content":"ok"}{"content":"extra"}`, "application/json"), &input))
}

func TestCommentHTTPStrictJSONUsesMentionSnakeCase(t *testing.T) {
	userID := uuid.NewString()
	var create CreateCommentInput
	require.NoError(t, decodeJSONStrict(newStrictJSONTestContext(`{"content":"@alice","mentions":[{"user_id":"`+userID+`","start":0,"end":6}]}`, "application/json; charset=utf-8"), &create))
	require.Equal(t, uuid.MustParse(userID), create.Mentions[0].UserID)
	require.Error(t, decodeJSONStrict(newStrictJSONTestContext(`{"content":"@alice","mentions":[{"userID":"`+userID+`","start":0,"end":6}]}`, "application/json"), &create))
	var edit EditCommentInput
	require.NoError(t, decodeJSONStrict(newStrictJSONTestContext(`{"content":"@alice","mentions":[{"user_id":"`+userID+`","start":0,"end":6}]}`, "application/vnd.api+json"), &edit))
}

func newStrictJSONTestContext(body, contentType string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	if contentType != "" {
		c.Request.Header.Set("Content-Type", contentType)
	}
	return c
}

func TestCommentHTTPStrictJSONRejectsMediaTypeAndLargeBody(t *testing.T) {
	var input CreateCommentInput
	require.ErrorIs(t, decodeJSONStrict(newStrictJSONTestContext(`{"content":"ok"}`, ""), &input), ErrUnsupportedMediaType)
	require.ErrorIs(t, decodeJSONStrict(newStrictJSONTestContext(`{"content":"ok"}`, "text/plain"), &input), ErrUnsupportedMediaType)
	large := `{"content":"` + strings.Repeat("x", 70*1024) + `"}`
	require.ErrorIs(t, decodeJSONStrict(newStrictJSONTestContext(large, "application/json"), &input), ErrRequestTooLarge)

	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	h := &HTTP{service: ctx.service}
	router := gin.New()
	router.Use(func(c *gin.Context) { authctx.SetCurrentUser(c, ctx.users[0]); c.Next() })
	router.POST("/discussions/:kind/:resource_id/comments", h.create)
	cases := []struct {
		contentType, body string
		status            int
	}{
		{"", `{"content":"ok"}`, http.StatusUnsupportedMediaType},
		{"text/plain", `{"content":"ok"}`, http.StatusUnsupportedMediaType},
		{"application/json", large, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/discussions/"+ctx.target.Kind+"/"+ctx.target.ResourceID.String()+"/comments", bytes.NewBufferString(tc.body))
		if tc.contentType != "" {
			req.Header.Set("Content-Type", tc.contentType)
		}
		router.ServeHTTP(w, req)
		require.Equal(t, tc.status, w.Code, w.Body.String())
	}
}

func TestCommentHTTPInvalidCommentUUIDUsesStableInvalidID(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	h := &HTTP{service: ctx.service}
	router := gin.New()
	router.Use(func(c *gin.Context) { authctx.SetCurrentUser(c, ctx.users[0]); c.Next() })
	router.GET("/comments/:root_comment_id/replies", h.listReplies)
	router.PATCH("/comments/:comment_id", h.edit)
	router.DELETE("/comments/:comment_id", h.delete)
	router.PUT("/comments/:comment_id/like", h.like)
	router.DELETE("/comments/:comment_id/like", h.unlike)
	router.PUT("/comments/:comment_id/report", h.report)
	router.PUT("/admin/comments/:comment_id/moderation", h.moderate)
	cases := []struct{ method, path string }{
		{http.MethodGet, "/comments/bad/replies"},
		{http.MethodPatch, "/comments/bad"},
		{http.MethodDelete, "/comments/bad"},
		{http.MethodPut, "/comments/bad/like"},
		{http.MethodDelete, "/comments/bad/like"},
		{http.MethodPut, "/comments/bad/report"},
		{http.MethodPut, "/admin/comments/bad/moderation"},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(`{}`))
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code, "%s %s: %s", tc.method, tc.path, w.Body.String())
		require.Contains(t, w.Body.String(), `"code":"comment.invalid_id"`)
	}

	missing := uuid.NewString()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/comments/"+missing+"/replies", nil))
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"code":"comment.not_found"`)
}

func TestCommentHTTPLegalMissingCommentUUIDRemainsNotFound(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	h := &HTTP{service: ctx.service}
	moderator := ctx.users[0]
	moderator.Role = authctx.RoleModerator
	router := gin.New()
	router.Use(func(c *gin.Context) { authctx.SetCurrentUser(c, moderator); c.Next() })
	router.GET("/comments/:root_comment_id/replies", h.listReplies)
	router.PATCH("/comments/:comment_id", h.edit)
	router.DELETE("/comments/:comment_id", h.delete)
	router.PUT("/comments/:comment_id/like", h.like)
	router.DELETE("/comments/:comment_id/like", h.unlike)
	router.PUT("/comments/:comment_id/report", h.report)
	router.PUT("/admin/comments/:comment_id/moderation", h.moderate)
	id := uuid.NewString()
	cases := []struct{ method, path, body string }{
		{http.MethodGet, "/comments/" + id + "/replies", ""},
		{http.MethodPatch, "/comments/" + id, `{"content":"edit"}`},
		{http.MethodDelete, "/comments/" + id, ""},
		{http.MethodPut, "/comments/" + id + "/like", ""},
		{http.MethodDelete, "/comments/" + id + "/like", ""},
		{http.MethodPut, "/comments/" + id + "/report", `{"reason":"spam","note":""}`},
		{http.MethodPut, "/admin/comments/" + id + "/moderation", `{"action":"hide","reason":"review"}`},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusNotFound, w.Code, "%s %s: %s", tc.method, tc.path, w.Body.String())
		require.Contains(t, w.Body.String(), `"code":"comment.not_found"`)
	}
}

func TestCommentHTTPRejectsOverflowingPageAcrossLists(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	root := ctx.create(t, 0, "overflow root", nil)
	h := &HTTP{service: ctx.service}
	moderator := ctx.users[0]
	moderator.Role = authctx.RoleModerator
	router := gin.New()
	router.Use(func(c *gin.Context) { authctx.SetCurrentUser(c, moderator); c.Next() })
	router.GET("/discussions/:kind/:resource_id/comments", h.list)
	router.GET("/comments/:root_comment_id/replies", h.listReplies)
	router.GET("/admin/comment-reports", h.listReports)
	page := strconv.FormatInt(math.MaxInt64, 10)
	paths := []string{
		"/discussions/" + ctx.target.Kind + "/" + ctx.target.ResourceID.String() + "/comments?page=" + page,
		"/comments/" + root.ID.String() + "/replies?page=" + page,
		"/admin/comment-reports?page=" + page,
	}
	for _, path := range paths {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusBadRequest, w.Code, "%s: %s", path, w.Body.String())
		require.Contains(t, w.Body.String(), `"code":"comment.invalid_list"`)
	}
}

func TestListRepliesPaginatesVisibleChildrenAndRejectsChildRoot(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	root := ctx.create(t, 0, "root", nil)
	first := ctx.create(t, 1, "first", &root.ID)
	ctx.create(t, 2, "second", &root.ID)
	ctx.create(t, 3, "third", &root.ID)

	page, err := ctx.service.ListReplies(Viewer{UserID: &ctx.users[0].ID}, root.ID, 1, 2)
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	require.Equal(t, first.ID, page.Items[0].ID)
	require.Equal(t, int64(3), page.Total)
	require.True(t, page.HasMore)

	_, err = ctx.service.ListReplies(Viewer{UserID: &ctx.users[0].ID}, first.ID, 1, 20)
	require.ErrorIs(t, err, ErrInvalidReply)
}

func TestListReportsRequiresModeratorAndReturnsPublicTargetIdentity(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	root := ctx.create(t, 0, "reported", nil)
	require.NoError(t, ctx.service.Report(ctx.users[1], root.ID, ReportInput{Reason: ReportReasonSpam}))

	_, err := ctx.service.ListReports(ctx.users[1], ReportStatusPending, 1, 20)
	require.ErrorIs(t, err, ErrCommentForbidden)

	moderator := ctx.users[2]
	moderator.Role = "moderator"
	result, err := ctx.service.ListReports(moderator, ReportStatusPending, 1, 20)
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	require.Equal(t, root.ID, result.Items[0].RootID)
	require.Equal(t, ctx.resolved.ResourceID, result.Items[0].ResourceID)
	require.Equal(t, ctx.resolved.Kind, result.Items[0].TargetKind)
	require.Equal(t, ctx.users[1].Username, result.Items[0].Username)
}

func TestModerateAllowsModeratorToReviewAfterTargetBecomesPrivate(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBlogPost, 0)
	root := ctx.create(t, 0, "private later", nil)
	private := ctx.resolved
	private.Visible = false
	ctx.service.registry.resolvers[TargetKindBlogPost] = targetResolverFunc(func(_ Viewer, id uuid.UUID) (ResolvedTarget, error) {
		if id != private.ResourceID {
			return ResolvedTarget{}, ErrTargetNotFound
		}
		return private, nil
	})
	moderator := ctx.users[2]
	moderator.Role = "moderator"
	require.NoError(t, ctx.service.Moderate(moderator, root.ID, ModerateInput{Action: ModerationHide, Reason: "reviewed"}))
	var stored model.CommentEntry
	require.NoError(t, ctx.db.First(&stored, "id = ?", root.ID).Error)
	require.Equal(t, CommentStatusModeratorHidden, stored.Status)
}
