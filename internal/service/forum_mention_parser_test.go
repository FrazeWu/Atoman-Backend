package service

import (
	"sort"
	"testing"

	"gorm.io/gorm"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestParseMentionsSupportsPlainAndMarkdownMentions(t *testing.T) {
	db := setupMentionParserTestDB(t)
	seedMentionParserUsers(t, db,
		model.User{Username: "alice", Email: "alice@example.com", Password: "pw", IsActive: true},
		model.User{Username: "bob", Email: "bob@example.com", Password: "pw", IsActive: true},
		model.User{Username: "ignored", Email: "ignored@example.com", Password: "pw", IsActive: true},
	)

	content := "hello [@艾丽丝](/user/ALICE), ping @bob, duplicate [@别名](/user/alice), `@ignored`, ```[@忽略](/user/ignored)```"

	users, err := ParseMentions(db, content)
	if err != nil {
		t.Fatalf("ParseMentions returned error: %v", err)
	}

	got := usernamesFromUsers(users)
	want := []string{"alice", "bob"}
	if len(got) != len(want) {
		t.Fatalf("expected %d users, got %d (%v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected usernames %v, got %v", want, got)
		}
	}
}

func TestParseMentionsUsesMarkdownTargetUsernameNotDisplayName(t *testing.T) {
	db := setupMentionParserTestDB(t)
	seedMentionParserUsers(t, db,
		model.User{Username: "alias", Email: "alias@example.com", Password: "pw", IsActive: true},
		model.User{Username: "bob", Email: "bob@example.com", Password: "pw", IsActive: true},
	)

	users, err := ParseMentions(db, "ping [@alias](/user/bob)")
	if err != nil {
		t.Fatalf("ParseMentions returned error: %v", err)
	}

	got := usernamesFromUsers(users)
	want := []string{"bob"}
	if len(got) != len(want) {
		t.Fatalf("expected %d users, got %d (%v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected usernames %v, got %v", want, got)
		}
	}
}

func TestParseMentionsIgnoresPlainUserLinksWithoutAtPrefix(t *testing.T) {
	db := setupMentionParserTestDB(t)
	seedMentionParserUsers(t, db,
		model.User{Username: "bob", Email: "bob@example.com", Password: "pw", IsActive: true},
	)

	users, err := ParseMentions(db, "请先[查看资料](/user/bob)再继续")
	if err != nil {
		t.Fatalf("ParseMentions returned error: %v", err)
	}

	if len(users) != 0 {
		t.Fatalf("expected no mentioned users, got %v", usernamesFromUsers(users))
	}
}

func setupMentionParserTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{})
	return db
}

func seedMentionParserUsers(t *testing.T, db *gorm.DB, users ...model.User) {
	t.Helper()
	for _, user := range users {
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("create user %s: %v", user.Username, err)
		}
	}
}

func usernamesFromUsers(users []model.User) []string {
	usernames := make([]string, 0, len(users))
	for _, user := range users {
		usernames = append(usernames, user.Username)
	}
	sort.Strings(usernames)
	return usernames
}
