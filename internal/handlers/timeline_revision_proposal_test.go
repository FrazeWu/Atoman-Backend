package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	proposalservice "atoman/internal/service"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func timelineProposalHandlerContext(t *testing.T) (*gin.Engine, *proposalservice.TimelineRevisionProposalService, authctx.CurrentUser, model.TimelineEvent) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.MediaAsset{}, &model.TimelineEvent{}, &model.TimelinePerson{}, &model.TimelineRevision{}, &model.Revision{},
		&model.DiscussionTarget{}, &model.CommentEntry{}, &model.CommentMention{}, &model.CommentAttachment{}, &model.CommentLike{},
		&model.CommentReport{}, &model.CommentTimeAnchor{}, &model.CommentPublishRecord{}, &model.Notification{}, &model.AuditLog{}, &model.TimelineRevisionProposal{},
	)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_timeline_handler_target ON discussion_targets (kind, resource_key)`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX uq_timeline_handler_floor ON comment_entries (target_id, floor_number) WHERE floor_number IS NOT NULL AND deleted_at IS NULL`).Error)
	stored := model.User{Username: "timeline-handler", Email: "timeline-handler@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	require.NoError(t, db.Create(&stored).Error)
	user := authctx.CurrentUser{ID: stored.UUID, Username: stored.Username, Role: stored.Role}
	event := model.TimelineEvent{UserID: user.ID, Title: "Old", Location: "Paris", Source: "source", IsPublic: true}
	require.NoError(t, db.Create(&event).Error)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { authctx.SetCurrentUser(c, user); c.Next() })
	svc := proposalservice.NewTimelineRevisionProposalService(db)
	router.POST("/api/v1/timeline/events/:id/revision-proposals", CreateTimelineEventProposal(svc))
	router.GET("/api/v1/timeline/events/:id/revision-proposals", ListTimelineEventProposals(svc))
	router.PUT("/api/v1/timeline/revision-proposals/:comment_id/decision", DecideTimelineRevisionProposal(svc))
	return router, svc, user, event
}

func TestTimelineProposalHandlerCreatesAndAcceptsEventProposal(t *testing.T) {
	router, svc, _, event := timelineProposalHandlerContext(t)
	create := httptest.NewRequest(http.MethodPost, "/api/v1/timeline/events/"+event.ID.String()+"/revision-proposals", bytes.NewBufferString(`{"content":"change","evidence":"archive","patch":{"location":"Berlin"},"mentions":[],"attachment_ids":[]}`))
	create.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	router.ServeHTTP(created, create)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	listed := httptest.NewRecorder()
	router.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/api/v1/timeline/events/"+event.ID.String()+"/revision-proposals", nil))
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	require.Contains(t, listed.Body.String(), `"evidence":"archive"`)
	guestRouter := gin.New()
	guestRouter.GET("/api/v1/timeline/events/:id/revision-proposals", ListTimelineEventProposals(svc))
	guestList := httptest.NewRecorder()
	guestRouter.ServeHTTP(guestList, httptest.NewRequest(http.MethodGet, "/api/v1/timeline/events/"+event.ID.String()+"/revision-proposals", nil))
	require.Equal(t, http.StatusOK, guestList.Code, guestList.Body.String())
	var commentID string
	require.Regexp(t, `"id":"([^"]+)"`, created.Body.String())
	parts := bytes.Split(created.Body.Bytes(), []byte(`"id":"`))
	commentID = string(bytes.Split(parts[1], []byte(`"`))[0])

	decision := httptest.NewRequest(http.MethodPut, "/api/v1/timeline/revision-proposals/"+commentID+"/decision", bytes.NewBufferString(`{"decision":"accept"}`))
	decision.Header.Set("Content-Type", "application/json")
	decided := httptest.NewRecorder()
	router.ServeHTTP(decided, decision)
	require.Equal(t, http.StatusOK, decided.Code, decided.Body.String())
	require.Contains(t, decided.Body.String(), `"status":"accepted"`)
	require.Contains(t, decided.Body.String(), `"content":"change"`)
	require.Contains(t, decided.Body.String(), `"username":"timeline-handler"`)
}

func TestTimelineProposalHandlerRejectsInvalidIDAndUnknownField(t *testing.T) {
	router, _, _, event := timelineProposalHandlerContext(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/timeline/events/"+event.ID.String()+"/revision-proposals", bytes.NewBufferString(`{"content":"change","evidence":"archive","patch":{"user_id":"forbidden"}}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)

	decision := httptest.NewRequest(http.MethodPut, "/api/v1/timeline/revision-proposals/not-a-uuid/decision", bytes.NewBufferString(`{"decision":"accept"}`))
	decision.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, decision)
	require.Equal(t, http.StatusBadRequest, response.Code)
}
