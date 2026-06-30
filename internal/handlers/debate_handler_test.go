package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func newDebateVoteTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db := testdb.Open(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Debate{},
		&model.Argument{},
		&model.DebateVote{},
		&model.VoteHistory{},
		&model.DebateConcludeVote{},
	)
	return db
}

func seedDebateVoteTestUser(t *testing.T, db *gorm.DB, username string) model.User {
	t.Helper()

	user := model.User{
		Username: username,
		Email:    username + "@example.com",
		Password: "hash",
		Role:     "user",
		IsActive: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func seedDebateVoteTestTopic(t *testing.T, db *gorm.DB, user model.User) model.Debate {
	t.Helper()

	debate := model.Debate{
		UserID:            user.UUID,
		Title:             "Debate",
		Status:            "open",
		ConcludeThreshold: 100,
	}
	if err := db.Create(&debate).Error; err != nil {
		t.Fatalf("create debate: %v", err)
	}
	return debate
}

func seedDebateVoteTestArgument(t *testing.T, db *gorm.DB, debate model.Debate, user model.User) model.Argument {
	t.Helper()

	argument := model.Argument{
		DebateID:     debate.ID,
		UserID:       user.UUID,
		Content:      "Argument",
		ArgumentType: model.ArgumentTypeSupport,
	}
	if err := db.Create(&argument).Error; err != nil {
		t.Fatalf("create argument: %v", err)
	}
	return argument
}

func withDebateVoteAuth(userID uuid.UUID, h gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("user_id", userID)
		c.Set("role", "user")
		h(c)
	}
}

func runDebateVoteRequest(userID uuid.UUID, h gin.HandlerFunc, method, path, paramID, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: paramID}}
	withDebateVoteAuth(userID, h)(c)
	return w
}

func TestDebateVoteDuplicateUserDoesNotCreateMultipleRowsOrDoubleCount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newDebateVoteTestDB(t)
	user := seedDebateVoteTestUser(t, db, "voter")
	debate := seedDebateVoteTestTopic(t, db, user)
	argument := seedDebateVoteTestArgument(t, db, debate, user)

	first := runDebateVoteRequest(user.UUID, VoteArgument(db), http.MethodPost, "/arguments/"+argument.ID.String()+"/vote", argument.ID.String(), `{"vote_type":1}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first vote status = %d, body = %s", first.Code, first.Body.String())
	}
	second := runDebateVoteRequest(user.UUID, VoteArgument(db), http.MethodPost, "/arguments/"+argument.ID.String()+"/vote", argument.ID.String(), `{"vote_type":1}`)
	if second.Code != http.StatusOK {
		t.Fatalf("second vote status = %d, body = %s", second.Code, second.Body.String())
	}

	var votes int64
	if err := db.Model(&model.DebateVote{}).Where("argument_id = ? AND user_id = ?", argument.ID, user.UUID).Count(&votes).Error; err != nil {
		t.Fatalf("count votes: %v", err)
	}
	var saved model.Argument
	if err := db.First(&saved, "id = ?", argument.ID).Error; err != nil {
		t.Fatalf("load argument: %v", err)
	}
	if votes != 0 || saved.VoteCount != 0 {
		t.Fatalf("same vote twice should toggle off once, votes=%d vote_count=%d", votes, saved.VoteCount)
	}
}

func TestDebateVoteConcurrentDifferentUsersCountsEveryVote(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newDebateVoteTestDB(t)
	owner := seedDebateVoteTestUser(t, db, "owner")
	debate := seedDebateVoteTestTopic(t, db, owner)
	argument := seedDebateVoteTestArgument(t, db, debate, owner)

	const workers = 6
	users := make([]model.User, 0, workers)
	for i := 0; i < workers; i++ {
		users = append(users, seedDebateVoteTestUser(t, db, "voter"+uuid.NewString()[:8]))
	}

	var wg sync.WaitGroup
	for _, user := range users {
		wg.Add(1)
		go func(user model.User) {
			defer wg.Done()
			w := runDebateVoteRequest(user.UUID, VoteArgument(db), http.MethodPost, "/arguments/"+argument.ID.String()+"/vote", argument.ID.String(), `{"vote_type":1}`)
			if w.Code != http.StatusOK {
				t.Errorf("vote status = %d, body = %s", w.Code, w.Body.String())
			}
		}(user)
	}
	wg.Wait()

	var votes int64
	if err := db.Model(&model.DebateVote{}).Where("argument_id = ?", argument.ID).Count(&votes).Error; err != nil {
		t.Fatalf("count votes: %v", err)
	}
	var saved model.Argument
	if err := db.First(&saved, "id = ?", argument.ID).Error; err != nil {
		t.Fatalf("load argument: %v", err)
	}
	if votes != workers || saved.VoteCount != workers {
		t.Fatalf("expected %d votes and count, votes=%d vote_count=%d", workers, votes, saved.VoteCount)
	}
}

func TestDebateConclusionVoteDuplicateUserDoesNotCreateMultipleRowsOrDoubleCount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newDebateVoteTestDB(t)
	user := seedDebateVoteTestUser(t, db, "concluder")
	debate := seedDebateVoteTestTopic(t, db, user)

	first := runDebateVoteRequest(user.UUID, VoteToConclude(db), http.MethodPost, "/topics/"+debate.ID.String()+"/conclude-vote", debate.ID.String(), "")
	if first.Code != http.StatusOK {
		t.Fatalf("first conclude vote status = %d, body = %s", first.Code, first.Body.String())
	}
	second := runDebateVoteRequest(user.UUID, VoteToConclude(db), http.MethodPost, "/topics/"+debate.ID.String()+"/conclude-vote", debate.ID.String(), "")
	if second.Code != http.StatusConflict {
		t.Fatalf("second conclude vote status = %d, body = %s", second.Code, second.Body.String())
	}

	var votes int64
	if err := db.Model(&model.DebateConcludeVote{}).Where("debate_id = ? AND user_id = ?", debate.ID, user.UUID).Count(&votes).Error; err != nil {
		t.Fatalf("count conclude votes: %v", err)
	}
	var saved model.Debate
	if err := db.First(&saved, "id = ?", debate.ID).Error; err != nil {
		t.Fatalf("load debate: %v", err)
	}
	if votes != 1 || saved.ConcludeVoteCount != 1 {
		t.Fatalf("expected one conclude vote and count, votes=%d conclude_vote_count=%d", votes, saved.ConcludeVoteCount)
	}
}
