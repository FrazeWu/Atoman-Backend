package authctx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestSetAndGetCurrentUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	id := uuid.New()
	SetCurrentUser(c, CurrentUser{ID: id, Username: "alice", Role: RoleUser})
	current, ok := Current(c)
	if !ok {
		t.Fatalf("expected current user")
	}
	if current.ID != id || current.Username != "alice" || current.Role != RoleUser {
		t.Fatalf("unexpected current user: %#v", current)
	}
	legacyID, ok := c.Get("user_id")
	if !ok || legacyID != id {
		t.Fatalf("expected legacy user_id %s, got %#v", id, legacyID)
	}
	if got := CurrentUserIDString(c); got != id.String() {
		t.Fatalf("expected legacy user_id string %s, got %q", id, got)
	}
	legacyAltID, ok := c.Get("userID")
	if !ok || legacyAltID != id {
		t.Fatalf("expected legacy userID %s, got %#v", id, legacyAltID)
	}
	if got := c.GetString("username"); got != "alice" {
		t.Fatalf("expected legacy username alice, got %q", got)
	}
	if got := c.GetString("role"); got != RoleUser {
		t.Fatalf("expected legacy role user, got %q", got)
	}
}

func TestCurrentUserIDStringReturnsUUIDStringAfterSetCurrentUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	id := uuid.New()
	SetCurrentUser(c, CurrentUser{ID: id, Username: "alice", Role: RoleUser})

	if got := c.GetString("user_id"); got != "" {
		t.Fatalf("expected Gin GetString(user_id) to stay empty for uuid.UUID legacy value, got %q", got)
	}
	if got := CurrentUserIDString(c); got != id.String() {
		t.Fatalf("expected CurrentUserIDString to return %s, got %q", id.String(), got)
	}
}

func TestNamedLegacyHandlersDoNotReadUserIDWithGetString(t *testing.T) {
	files := []string{
		"lyric_annotation_handler.go",
		"artist_wiki_handler.go",
		"revision_handler.go",
		"protection_handler.go",
	}
	for _, file := range files {
		t.Run(file, func(t *testing.T) {
			path := filepath.Join("..", "..", "handlers", file)
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			if strings.Contains(string(content), `GetString("user_id")`) {
				t.Fatalf("%s still reads user_id with Gin GetString", path)
			}
		})
	}
}

func TestCurrentReturnsAnonymousWhenMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	current, ok := Current(c)
	if ok {
		t.Fatalf("expected no current user")
	}
	if current.Role != RoleAnonymous {
		t.Fatalf("expected anonymous role, got %q", current.Role)
	}
}

func TestRequireRole(t *testing.T) {
	if !RoleAtLeast(RoleAdmin, RoleModerator) {
		t.Fatalf("expected admin to satisfy moderator")
	}
	if RoleAtLeast(RoleUser, RoleModerator) {
		t.Fatalf("expected user not to satisfy moderator")
	}
	if !RoleAtLeast(RoleOwner, RoleAdmin) {
		t.Fatalf("expected owner to satisfy admin")
	}
	if !RoleAtLeast(RoleUser, RoleAnonymous) {
		t.Fatalf("expected user to satisfy anonymous")
	}
}
