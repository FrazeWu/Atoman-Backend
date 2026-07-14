package comment

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
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

func TestCommentHTTPStrictJSONDecoderRejectsUnknownAndTrailingValues(t *testing.T) {
	var input CreateCommentInput
	require.Error(t, decodeJSONStrict(bytes.NewBufferString(`{"content":"ok","root_id":"`+uuid.NewString()+`"}`), &input))
	require.Error(t, decodeJSONStrict(bytes.NewBufferString(`{"content":"ok"}{"content":"extra"}`), &input))
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
