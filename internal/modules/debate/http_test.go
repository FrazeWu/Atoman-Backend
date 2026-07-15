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
