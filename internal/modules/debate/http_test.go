package debate

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
)

func newDebateHTTPTestService(t *testing.T) (*Service, authctx.CurrentUser) {
	t.Helper()

	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Debate{}, &model.Argument{}, &model.DebateVote{})

	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	return NewService(db), authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role}
}

func newDebateHTTPRouter(service *Service, current *authctx.CurrentUser) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if current != nil {
			authctx.SetCurrentUser(c, *current)
		}
		c.Next()
	})
	RegisterRoutes(r.Group("/api/v1"), service)
	return r
}

func TestRegisterRoutesMountsListDetailSearchAndArgumentList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, user := newDebateHTTPTestService(t)
	debate, err := service.CreateDebate(user, CreateDebateRequest{Title: "Router Debate", Description: "Body"})
	if err != nil {
		t.Fatalf("create debate: %v", err)
	}
	if _, err := service.CreateArgument(user, CreateArgumentRequest{DebateID: debate.ID, Content: "Argument", ArgumentType: string(model.ArgumentTypeSupport)}); err != nil {
		t.Fatalf("create argument: %v", err)
	}

	r := newDebateHTTPRouter(service, &user)

	cases := []string{
		"/api/v1/debate/topics",
		"/api/v1/debate/topics/" + debate.ID.String(),
		"/api/v1/debate/topics/search?q=Router",
		"/api/v1/debate/topics/" + debate.ID.String() + "/arguments",
	}

	for _, path := range cases {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		r.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Fatalf("expected route %s to be mounted, got 404: %s", path, w.Body.String())
		}
	}
}

func TestListArgumentsReturnsCurrentUserVotes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, user := newDebateHTTPTestService(t)
	debate, err := service.CreateDebate(user, CreateDebateRequest{Title: "Voting Debate", Description: "Body"})
	if err != nil {
		t.Fatalf("create debate: %v", err)
	}
	argument, err := service.CreateArgument(user, CreateArgumentRequest{DebateID: debate.ID, Content: "Argument", ArgumentType: string(model.ArgumentTypeSupport)})
	if err != nil {
		t.Fatalf("create argument: %v", err)
	}
	if err := service.db.Create(&model.DebateVote{ArgumentID: argument.ID, UserID: user.ID, VoteType: 1}).Error; err != nil {
		t.Fatalf("create vote: %v", err)
	}

	r := newDebateHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/debate/topics/"+debate.ID.String()+"/arguments", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Meta struct {
			UserVotes map[string]int `json:"user_votes"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Meta.UserVotes[argument.ID.String()] != 1 {
		t.Fatalf("expected current user vote for %s, got %#v", argument.ID, response.Meta.UserVotes)
	}
}

func TestListDebatesFiltersByTag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, user := newDebateHTTPTestService(t)
	if _, err := service.CreateDebate(user, CreateDebateRequest{Title: "Science", Description: "Body", Tags: []string{"science"}}); err != nil {
		t.Fatalf("create science debate: %v", err)
	}
	if _, err := service.CreateDebate(user, CreateDebateRequest{Title: "History", Description: "Body", Tags: []string{"history"}}); err != nil {
		t.Fatalf("create history debate: %v", err)
	}

	r := newDebateHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/debate/topics?tag=science", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Data []model.Debate `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data) != 1 || response.Data[0].Title != "Science" {
		t.Fatalf("expected only science debate, got %#v", response.Data)
	}
}

func TestListAndGetDebateReturnOnlyActiveArgumentVotes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, user := newDebateHTTPTestService(t)
	debate, err := service.CreateDebate(user, CreateDebateRequest{Title: "Vote totals", Description: "Body"})
	if err != nil {
		t.Fatalf("create debate: %v", err)
	}
	activeArgument, err := service.CreateArgument(user, CreateArgumentRequest{DebateID: debate.ID, Content: "Active argument", ArgumentType: string(model.ArgumentTypeSupport)})
	if err != nil {
		t.Fatalf("create active argument: %v", err)
	}
	deletedArgument, err := service.CreateArgument(user, CreateArgumentRequest{DebateID: debate.ID, Content: "Deleted argument", ArgumentType: string(model.ArgumentTypeOppose)})
	if err != nil {
		t.Fatalf("create argument to delete: %v", err)
	}

	voter := model.User{Username: "bob", Email: "bob@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := service.db.Create(&voter).Error; err != nil {
		t.Fatalf("create voter: %v", err)
	}
	deletedVoter := model.User{Username: "carol", Email: "carol@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := service.db.Create(&deletedVoter).Error; err != nil {
		t.Fatalf("create voter for deleted vote: %v", err)
	}

	if err := service.db.Create(&model.DebateVote{ArgumentID: activeArgument.ID, UserID: voter.UUID, VoteType: 1}).Error; err != nil {
		t.Fatalf("create active vote: %v", err)
	}
	deletedVote := model.DebateVote{ArgumentID: activeArgument.ID, UserID: deletedVoter.UUID, VoteType: -1}
	if err := service.db.Create(&deletedVote).Error; err != nil {
		t.Fatalf("create vote to delete: %v", err)
	}
	if err := service.db.Delete(&deletedVote).Error; err != nil {
		t.Fatalf("soft delete vote: %v", err)
	}
	if err := service.db.Create(&model.DebateVote{ArgumentID: deletedArgument.ID, UserID: voter.UUID, VoteType: 1}).Error; err != nil {
		t.Fatalf("create vote for deleted argument: %v", err)
	}
	if err := service.db.Delete(&deletedArgument).Error; err != nil {
		t.Fatalf("soft delete argument: %v", err)
	}

	router := newDebateHTTPRouter(service, nil)
	listRecorder := httptest.NewRecorder()
	listRequest := httptest.NewRequest(http.MethodGet, "/api/v1/debate/topics?page=1&limit=20", nil)
	router.ServeHTTP(listRecorder, listRequest)
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("expected list status 200, got %d: %s", listRecorder.Code, listRecorder.Body.String())
	}
	var listPayload struct {
		Data []model.Debate `json:"data"`
		Meta struct {
			Page     int   `json:"page"`
			PageSize int   `json:"page_size"`
			Total    int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listPayload.Data) != 1 || listPayload.Meta.Page != 1 || listPayload.Meta.PageSize != 20 || listPayload.Meta.Total != 1 {
		t.Fatalf("expected unchanged list envelope, got %s", listRecorder.Body.String())
	}
	listed := listPayload.Data[0]
	if listed.Title != debate.Title || listed.Status != "open" || listed.User == nil || listed.User.Username != user.Username {
		t.Fatalf("expected original debate fields and user, got %#v", listed)
	}
	if listed.VoteCount != 1 {
		t.Fatalf("expected list vote_count=1, got %d: %s", listed.VoteCount, listRecorder.Body.String())
	}

	detailRecorder := httptest.NewRecorder()
	detailRequest := httptest.NewRequest(http.MethodGet, "/api/v1/debate/topics/"+debate.ID.String(), nil)
	router.ServeHTTP(detailRecorder, detailRequest)
	if detailRecorder.Code != http.StatusOK {
		t.Fatalf("expected detail status 200, got %d: %s", detailRecorder.Code, detailRecorder.Body.String())
	}
	var detailPayload struct {
		Data model.Debate `json:"data"`
	}
	if err := json.Unmarshal(detailRecorder.Body.Bytes(), &detailPayload); err != nil {
		t.Fatalf("decode detail response: %v", err)
	}
	if detailPayload.Data.Title != debate.Title || detailPayload.Data.Status != "open" || detailPayload.Data.User == nil || detailPayload.Data.User.Username != user.Username {
		t.Fatalf("expected original detail fields and user, got %#v", detailPayload.Data)
	}
	if detailPayload.Data.VoteCount != 1 {
		t.Fatalf("expected detail vote_count=1, got %d: %s", detailPayload.Data.VoteCount, detailRecorder.Body.String())
	}
}

func TestRegisterRoutesMountsTopicMutationAndArgumentCreate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, user := newDebateHTTPTestService(t)
	r := newDebateHTTPRouter(service, &user)

	createBody := map[string]any{
		"title":       "Create Debate",
		"description": "Body",
		"content":     "Content",
		"tags":        []string{"tag1"},
	}
	raw, _ := json.Marshal(createBody)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/debate/topics", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected topic create route to be mounted, got 404: %s", w.Body.String())
	}

	var created struct {
		Data model.Debate `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Data.ID.String() == "" {
		t.Fatalf("expected debate id in create response, got %s", w.Body.String())
	}

	updateRaw := bytes.NewBufferString(`{"title":"Updated","description":"Updated body","content":"Updated content","tags":["tag2"]}`)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/debate/topics/"+created.Data.ID.String(), updateRaw)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected topic update route to be mounted, got 404: %s", w.Body.String())
	}

	argumentRaw := bytes.NewBufferString(`{"content":"Argument","argument_type":"support"}`)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/debate/topics/"+created.Data.ID.String()+"/arguments", argumentRaw)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected argument create route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/debate/topics/"+created.Data.ID.String(), nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected topic delete route to be mounted, got 404: %s", w.Body.String())
	}
}

func TestRegisterRoutesMountsConcludeReopenAndArgumentMutation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, user := newDebateHTTPTestService(t)
	debate, err := service.CreateDebate(user, CreateDebateRequest{Title: "Debate", Description: "Body"})
	if err != nil {
		t.Fatalf("create debate: %v", err)
	}
	argument, err := service.CreateArgument(user, CreateArgumentRequest{DebateID: debate.ID, Content: "Argument", ArgumentType: string(model.ArgumentTypeSupport)})
	if err != nil {
		t.Fatalf("create argument: %v", err)
	}

	r := newDebateHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/debate/topics/"+debate.ID.String()+"/conclude", bytes.NewBufferString(`{"conclusion_type":"inconclusive","conclusion_summary":"done"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected conclude route to be mounted, got 404: %s", w.Body.String())
	}

	updateArgumentRaw := bytes.NewBufferString(`{"content":"Updated argument","argument_type":"support","source_url":"https://example.com"}`)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/debate/arguments/"+argument.ID.String(), updateArgumentRaw)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected argument update route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/debate/arguments/"+argument.ID.String(), nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected argument delete route to be mounted, got 404: %s", w.Body.String())
	}

	adminRouter := newDebateHTTPRouter(service, &authctx.CurrentUser{ID: user.ID, Username: user.Username, Role: authctx.RoleAdmin})
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/debate/topics/"+debate.ID.String()+"/reopen", nil)
	adminRouter.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected reopen route to be mounted, got 404: %s", w.Body.String())
	}
}

func TestRegisterRoutesMountsReferenceAndFoldOperations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, user := newDebateHTTPTestService(t)
	debate, err := service.CreateDebate(user, CreateDebateRequest{Title: "Debate", Description: "Body"})
	if err != nil {
		t.Fatalf("create debate: %v", err)
	}
	argument, err := service.CreateArgument(user, CreateArgumentRequest{DebateID: debate.ID, Content: "Argument", ArgumentType: string(model.ArgumentTypeSupport)})
	if err != nil {
		t.Fatalf("create argument: %v", err)
	}
	refArgument, err := service.CreateArgument(user, CreateArgumentRequest{DebateID: debate.ID, Content: "Ref", ArgumentType: string(model.ArgumentTypeSupport)})
	if err != nil {
		t.Fatalf("create ref argument: %v", err)
	}

	adminRouter := newDebateHTTPRouter(service, &authctx.CurrentUser{ID: user.ID, Username: user.Username, Role: authctx.RoleAdmin})
	userRouter := newDebateHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/debate/arguments/"+argument.ID.String()+"/reference", bytes.NewBufferString(`{"reference_id":"`+refArgument.ID.String()+`"}`))
	req.Header.Set("Content-Type", "application/json")
	userRouter.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected add reference route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/debate/arguments/"+argument.ID.String()+"/reference/"+refArgument.ID.String(), nil)
	userRouter.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected remove reference route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/debate/arguments/"+argument.ID.String()+"/debate-reference", bytes.NewBufferString(`{"debate_id":"`+debate.ID.String()+`"}`))
	req.Header.Set("Content-Type", "application/json")
	userRouter.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected add debate reference route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/debate/arguments/"+argument.ID.String()+"/debate-reference/"+debate.ID.String(), nil)
	userRouter.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected remove debate reference route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/debate/arguments/"+argument.ID.String()+"/fold", bytes.NewBufferString(`{"fold_note":"note"}`))
	req.Header.Set("Content-Type", "application/json")
	adminRouter.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected fold route to be mounted, got 404: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/debate/arguments/"+argument.ID.String()+"/fold", nil)
	adminRouter.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("expected unfold route to be mounted, got 404: %s", w.Body.String())
	}
}
