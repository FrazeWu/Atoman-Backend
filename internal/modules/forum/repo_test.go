package forum

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/service"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestUpsertDraftOverwritesExistingDraftForSameUserAndContext(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.ForumDraft{})
	if err := service.RunForumMigrations(db); err != nil {
		t.Fatalf("run forum migrations: %v", err)
	}

	repo := NewRepo(db)
	userID := uuid.MustParse("44444444-4444-7444-8444-444444444444")

	first := model.ForumDraft{
		UserID:     userID,
		ContextKey: "reply:topic-2",
		Title:      "first",
		Content:    "first body",
		Tags:       "alpha",
	}
	if err := repo.UpsertDraft(&first); err != nil {
		t.Fatalf("upsert first draft: %v", err)
	}

	second := model.ForumDraft{
		UserID:     userID,
		ContextKey: "reply:topic-2",
		Title:      "second",
		Content:    "second body",
		Tags:       "beta",
	}
	if err := repo.UpsertDraft(&second); err != nil {
		t.Fatalf("upsert second draft: %v", err)
	}

	var drafts []model.ForumDraft
	if err := db.Where("user_id = ? AND context_key = ?", userID, "reply:topic-2").Find(&drafts).Error; err != nil {
		t.Fatalf("query drafts: %v", err)
	}
	if len(drafts) != 1 {
		t.Fatalf("expected 1 draft row, got %d", len(drafts))
	}
	if drafts[0].Title != "second" || drafts[0].Content != "second body" || drafts[0].Tags != "beta" {
		t.Fatalf("expected latest draft values, got %#v", drafts[0])
	}
}

func TestUpsertDraftUsesModelUniqueIndexForSameUserAndContext(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.ForumDraft{})

	repo := NewRepo(db)
	userID := uuid.MustParse("55555555-5555-7555-8555-555555555555")

	first := model.ForumDraft{
		UserID:     userID,
		ContextKey: "reply:topic-3",
		Title:      "first",
		Content:    "first body",
	}
	if err := repo.UpsertDraft(&first); err != nil {
		t.Fatalf("upsert first draft: %v", err)
	}

	second := model.ForumDraft{
		UserID:     userID,
		ContextKey: "reply:topic-3",
		Title:      "second",
		Content:    "second body",
	}
	if err := repo.UpsertDraft(&second); err != nil {
		t.Fatalf("upsert second draft: %v", err)
	}

	var count int64
	if err := db.Model(&model.ForumDraft{}).Where("user_id = ? AND context_key = ?", userID, "reply:topic-3").Count(&count).Error; err != nil {
		t.Fatalf("count drafts: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 draft row, got %d", count)
	}
}
