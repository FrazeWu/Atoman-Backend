package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	proposalservice "atoman/internal/service"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func timelineProposalHandlerContext(t *testing.T) (*gin.Engine, *proposalservice.TimelineRevisionProposalService, *gorm.DB, authctx.CurrentUser, model.TimelineEvent) {
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
	return router, svc, db, user, event
}

func TestTimelineProposalHandlerCreatesAndAcceptsEventProposal(t *testing.T) {
	router, svc, _, _, event := timelineProposalHandlerContext(t)
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
	router, _, _, _, event := timelineProposalHandlerContext(t)
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

func TestTimelineProposalRoutesUseOptionalAuthForLikedState(t *testing.T) {
	t.Setenv("JWT_SECRET", "timeline-secret")
	_, svc, db, user, event := timelineProposalHandlerContext(t)
	created, err := svc.CreateEventProposal(user, event.ID, proposalservice.TimelineProposalInput{Content: "liked", Evidence: "archive", Patch: map[string]any{"location": "Berlin"}})
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.CommentLike{CommentID: created.Comment.ID, UserID: user.ID}).Error)
	require.NoError(t, db.Model(&model.CommentEntry{}).Where("id = ?", created.Comment.ID).Update("like_count", 1).Error)
	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"user_id": user.ID.String(), "exp": time.Now().Add(time.Hour).Unix()}).SignedString([]byte("timeline-secret"))
	require.NoError(t, err)
	router := gin.New()
	SetupTimelineRoutes(router, db)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/timeline/events/"+event.ID.String()+"/revision-proposals", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"liked":true`)
}

func TestRevertTimelineEventRestoresAcceptedCoordinatesAndTags(t *testing.T) {
	_, svc, db, user, event := timelineProposalHandlerContext(t)
	proposal, err := svc.CreateEventProposal(user, event.ID, proposalservice.TimelineProposalInput{
		Content: "map", Evidence: "archive", Patch: map[string]any{"latitude": 52.52, "longitude": 13.405, "tags": []string{"berlin", "history"}},
	})
	require.NoError(t, err)
	accepted, err := svc.Decide(user, proposal.Comment.ID, "accept")
	require.NoError(t, err)
	require.NotNil(t, accepted.AppliedRevisionID)
	require.NoError(t, db.Model(&model.TimelineEvent{}).Where("id = ?", event.ID).Updates(map[string]any{"latitude": nil, "longitude": nil, "tags": pq.StringArray{}}).Error)

	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
	router.POST("/events/:id/revert/:revision_id", RevertTimelineEvent(db))
	request := httptest.NewRequest(http.MethodPost, "/events/"+event.ID.String()+"/revert/"+accepted.AppliedRevisionID.String(), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var reverted model.TimelineEvent
	require.NoError(t, db.First(&reverted, "id = ?", event.ID).Error)
	require.NotNil(t, reverted.Latitude)
	require.NotNil(t, reverted.Longitude)
	require.Equal(t, 52.52, *reverted.Latitude)
	require.Equal(t, 13.405, *reverted.Longitude)
	require.Equal(t, []string{"berlin", "history"}, []string(reverted.Tags))

	empty := model.TimelineRevision{EventID: event.ID, EditorID: user.ID, Title: event.Title, EventDate: event.EventDate.Format("2006-01-02"), Location: event.Location, Source: event.Source, Tags: pq.StringArray{}}
	require.NoError(t, db.Create(&empty).Error)
	request = httptest.NewRequest(http.MethodPost, "/events/"+event.ID.String()+"/revert/"+empty.ID.String(), nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	reverted = model.TimelineEvent{}
	require.NoError(t, db.First(&reverted, "id = ?", event.ID).Error)
	require.Nil(t, reverted.Latitude)
	require.Nil(t, reverted.Longitude)
	require.Empty(t, reverted.Tags)
}
