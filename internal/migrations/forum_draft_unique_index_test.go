package migrations

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestRunForumDraftUniqueIndexDeduplicatesAndCreatesIndex(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.ForumDraft{})

	userID := uuid.MustParse("11111111-1111-7111-8111-111111111111")
	contextKey := "reply:topic-1"
	older := model.ForumDraft{
		Base: model.Base{
			ID:        uuid.MustParse("22222222-2222-7222-8222-222222222222"),
			CreatedAt: time.Date(2026, 6, 29, 8, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 6, 29, 8, 0, 0, 0, time.UTC),
		},
		UserID:     userID,
		ContextKey: contextKey,
		Title:      "old",
		Content:    "old body",
		Tags:       "alpha",
	}
	newer := model.ForumDraft{
		Base: model.Base{
			ID:        uuid.MustParse("33333333-3333-7333-8333-333333333333"),
			CreatedAt: time.Date(2026, 6, 29, 9, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 6, 29, 9, 0, 0, 0, time.UTC),
		},
		UserID:     userID,
		ContextKey: contextKey,
		Title:      "new",
		Content:    "new body",
		Tags:       "beta",
	}

	if err := db.Create(&older).Error; err != nil {
		t.Fatalf("create older draft: %v", err)
	}
	if err := db.Create(&newer).Error; err != nil {
		t.Fatalf("create newer draft: %v", err)
	}

	if err := RunForumDraftUniqueIndex(db); err != nil {
		t.Fatalf("run forum draft unique index migration: %v", err)
	}

	assertIndexExists(t, db, "forum_drafts", "idx_forum_drafts_user_context")

	var drafts []model.ForumDraft
	if err := db.Where("user_id = ? AND context_key = ?", userID, contextKey).Order("updated_at DESC, created_at DESC, id DESC").Find(&drafts).Error; err != nil {
		t.Fatalf("query deduplicated drafts: %v", err)
	}
	if len(drafts) != 1 {
		t.Fatalf("expected 1 forum draft after dedupe, got %d", len(drafts))
	}
	if drafts[0].ID != newer.ID {
		t.Fatalf("expected newest draft %s to survive, got %s", newer.ID, drafts[0].ID)
	}
}
