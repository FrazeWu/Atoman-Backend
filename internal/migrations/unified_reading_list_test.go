package migrations

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

type legacyReadingListItem struct {
	UserID     uuid.UUID `gorm:"type:uuid;not null;primaryKey"`
	FeedItemID uuid.UUID `gorm:"type:uuid;not null;primaryKey"`
	CreatedAt  time.Time
}

func (legacyReadingListItem) TableName() string { return "reading_list_items" }

func TestRunUnifiedReadingListMigrationPreservesLegacyFeedItems(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &legacyReadingListItem{})

	createdAt := time.Date(2026, 7, 10, 12, 30, 0, 0, time.UTC)
	legacy := legacyReadingListItem{UserID: uuid.New(), FeedItemID: uuid.New(), CreatedAt: createdAt}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("create legacy reading list item: %v", err)
	}

	if err := RunUnifiedReadingListMigration(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}
	if err := RunUnifiedReadingListMigration(db); err != nil {
		t.Fatalf("rerun migration: %v", err)
	}

	var migrated model.ReadingListItem
	if err := db.First(&migrated, "user_id = ? AND target_type = ? AND target_id = ?", legacy.UserID, "feed_item", legacy.FeedItemID).Error; err != nil {
		t.Fatalf("load migrated reading list item: %v", err)
	}
	if !migrated.CreatedAt.Equal(createdAt) {
		t.Fatalf("expected created_at=%s, got %s", createdAt, migrated.CreatedAt)
	}
	if db.Migrator().HasColumn("reading_list_items", "feed_item_id") {
		t.Fatal("expected legacy feed_item_id column to be removed")
	}
}

func TestUnifiedReadingListDoesNotCreateMutuallyExclusiveTargetForeignKeys(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Channel{}, &model.Collection{}, &model.Post{},
		&model.FeedSource{}, &model.FeedItem{}, &model.ReadingListItem{},
	)

	if db.Migrator().HasConstraint(&model.ReadingListItem{}, "fk_reading_list_items_feed_item") {
		t.Fatal("target_id must not require a feed item for post targets")
	}
	if db.Migrator().HasConstraint(&model.ReadingListItem{}, "fk_reading_list_items_post") {
		t.Fatal("target_id must not require a post for feed item targets")
	}
}
