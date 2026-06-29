package service_test

import (
	"context"
	"testing"

	"atoman/internal/model"
	"atoman/internal/service"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func setupSiteNamespaceTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{})
	return db
}

func TestSiteNamespaceReservedNames(t *testing.T) {
	db := setupSiteNamespaceTestDB(t)
	ns := service.NewSiteNamespaceService(db)

	reserved := []string{
		"feed", "music", "blog", "forum", "debate", "timeline", "podcast", "video", "media",
		"www", "api", "admin", "auth", "login", "register", "setting", "settings",
		"user", "users", "channel", "channels", "post", "posts", "collection", "collections",
		"article", "articles", "topic", "topics", "comment", "comments", "notification", "notifications",
		"inbox", "dm", "search", "explore", "upload", "static", "assets", "cdn", "status",
	}

	for _, name := range reserved {
		t.Run(name, func(t *testing.T) {
			result, err := ns.Resolve(context.Background(), name)
			if err != nil {
				t.Fatalf("resolve reserved handle: %v", err)
			}
			if result.Type != "module" {
				t.Fatalf("expected module, got %q", result.Type)
			}
			if result.Handle != name {
				t.Fatalf("expected handle %q, got %q", name, result.Handle)
			}
		})
	}
}

func TestSiteNamespaceUserChannelConflictChecks(t *testing.T) {
	db := setupSiteNamespaceTestDB(t)
	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	channel := model.Channel{Name: "Design", Slug: "design", UserID: &user.UUID}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	ns := service.NewSiteNamespaceService(db)
	if err := ns.ValidateUsernameAvailable(context.Background(), "feed"); err != service.ErrSiteHandleReserved {
		t.Fatalf("expected reserved username error, got %v", err)
	}
	if err := ns.ValidateUsernameAvailable(context.Background(), "design"); err != service.ErrSiteHandleTaken {
		t.Fatalf("expected taken username error, got %v", err)
	}
	if err := ns.ValidateChannelSlugAvailable(context.Background(), "music", nil); err != service.ErrSiteHandleReserved {
		t.Fatalf("expected reserved channel slug error, got %v", err)
	}
	if err := ns.ValidateChannelSlugAvailable(context.Background(), "alice", nil); err != service.ErrSiteHandleTaken {
		t.Fatalf("expected taken channel slug error, got %v", err)
	}
}

func TestSiteNamespaceChannelSlugAllowsExcludedChannel(t *testing.T) {
	db := setupSiteNamespaceTestDB(t)
	ownerID := uuid.New()
	channel := model.Channel{Name: "Design", Slug: "design", UserID: &ownerID}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	ns := service.NewSiteNamespaceService(db)
	if err := ns.ValidateChannelSlugAvailable(context.Background(), "design", &channel.ID); err != nil {
		t.Fatalf("expected excluded channel slug to be available, got %v", err)
	}
}

func TestSiteNamespaceResolveUserChannelUnknown(t *testing.T) {
	db := setupSiteNamespaceTestDB(t)
	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	channel := model.Channel{Name: "Design", Slug: "design", UserID: &user.UUID}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	ns := service.NewSiteNamespaceService(db)

	userResult, err := ns.Resolve(context.Background(), "alice")
	if err != nil {
		t.Fatalf("resolve user: %v", err)
	}
	if userResult.Type != "user" || userResult.Username != "alice" {
		t.Fatalf("unexpected user result: %#v", userResult)
	}

	channelResult, err := ns.Resolve(context.Background(), "design")
	if err != nil {
		t.Fatalf("resolve channel: %v", err)
	}
	if channelResult.Type != "channel" || channelResult.Slug != "design" {
		t.Fatalf("unexpected channel result: %#v", channelResult)
	}

	unknownResult, err := ns.Resolve(context.Background(), "missing")
	if err != nil {
		t.Fatalf("resolve unknown: %v", err)
	}
	if unknownResult.Type != "unknown" || unknownResult.Handle != "missing" {
		t.Fatalf("unexpected unknown result: %#v", unknownResult)
	}
}
