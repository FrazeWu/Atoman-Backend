package debate

import (
	"errors"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"gorm.io/gorm"
)

func TestCreateArgumentRollsBackWhenDebateCountUpdateFails(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Debate{}, &model.Argument{})

	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	debate := model.Debate{UserID: user.UUID, Title: "Topic", Status: "open"}
	if err := db.Create(&debate).Error; err != nil {
		t.Fatalf("create debate: %v", err)
	}

	callbackName := "fail_debate_count_" + strings.ReplaceAll(t.Name(), "/", "_")
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "debates" {
			tx.AddError(errors.New("count update failed"))
		}
	}); err != nil {
		t.Fatalf("register update callback: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Update().Remove(callbackName)
	})

	_, err := NewService(db).CreateArgument(authctx.CurrentUser{
		ID:       user.UUID,
		Username: user.Username,
		Role:     authctx.RoleUser,
	}, CreateArgumentRequest{
		DebateID:     debate.ID,
		Content:      "Argument",
		ArgumentType: string(model.ArgumentTypeSupport),
	})
	if err == nil {
		t.Fatal("expected count update failure to be returned")
	}

	var count int64
	if err := db.Model(&model.Argument{}).Where("debate_id = ?", debate.ID).Count(&count).Error; err != nil {
		t.Fatalf("count arguments: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected argument insert to roll back, got %d rows", count)
	}
}

func TestCreateArgumentRejectsQuotedArgumentFromAnotherDebate(t *testing.T) {
	service, db, author, firstDebate := newDebateServiceTest(t)
	secondDebate := model.Debate{UserID: author.ID, Title: "Other topic", Status: "open"}
	if err := db.Create(&secondDebate).Error; err != nil {
		t.Fatalf("create second debate: %v", err)
	}
	quoted := model.Argument{
		DebateID:     secondDebate.ID,
		UserID:       author.ID,
		Content:      "Other argument",
		ArgumentType: model.ArgumentTypeSupport,
	}
	if err := db.Create(&quoted).Error; err != nil {
		t.Fatalf("create quoted argument: %v", err)
	}

	_, err := service.CreateArgument(author, CreateArgumentRequest{
		DebateID:     firstDebate.ID,
		ParentID:     &quoted.ID,
		Content:      "Cross-debate quote",
		ArgumentType: string(model.ArgumentTypeCounter),
	})
	if err == nil {
		t.Fatal("expected cross-debate quoted argument to be rejected")
	}
}

func TestCreateArgumentRejectsInvalidArgumentType(t *testing.T) {
	service, _, author, debate := newDebateServiceTest(t)

	_, err := service.CreateArgument(author, CreateArgumentRequest{
		DebateID:     debate.ID,
		Content:      "Invalid type",
		ArgumentType: "invalid",
	})
	if err == nil {
		t.Fatal("expected invalid argument type to be rejected")
	}
}

func TestAddArgumentReferenceRejectsUnrelatedUser(t *testing.T) {
	service, db, author, debate := newDebateServiceTest(t)
	argument := model.Argument{DebateID: debate.ID, UserID: author.ID, Content: "Owned", ArgumentType: model.ArgumentTypeSupport}
	reference := model.Argument{DebateID: debate.ID, UserID: author.ID, Content: "Reference", ArgumentType: model.ArgumentTypeEvidence}
	if err := db.Create(&argument).Error; err != nil {
		t.Fatalf("create argument: %v", err)
	}
	if err := db.Create(&reference).Error; err != nil {
		t.Fatalf("create reference: %v", err)
	}
	attacker := model.User{Username: "mallory", Email: "mallory@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&attacker).Error; err != nil {
		t.Fatalf("create unrelated user: %v", err)
	}

	err := service.AddArgumentReference(authctx.CurrentUser{
		ID:       attacker.UUID,
		Username: attacker.Username,
		Role:     authctx.RoleUser,
	}, argument.ID, reference.ID)
	if err == nil {
		t.Fatal("expected unrelated user to be forbidden")
	}
}

func newDebateServiceTest(t *testing.T) (*Service, *gorm.DB, authctx.CurrentUser, model.Debate) {
	t.Helper()

	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Debate{}, &model.Argument{})
	user := model.User{Username: "author", Email: "author@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	author := authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: authctx.RoleUser}
	debate := model.Debate{UserID: user.UUID, Title: "Topic", Status: "open"}
	if err := db.Create(&debate).Error; err != nil {
		t.Fatalf("create debate: %v", err)
	}
	return NewService(db), db, author, debate
}
