package migrations

import (
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"
	"github.com/google/uuid"
)

func TestRunSubscriptionUniqueIndexCreatesExpectedIndex(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.FeedSource{}, &model.Subscription{})

	if err := RunSubscriptionUniqueIndex(db); err != nil {
		t.Fatalf("run subscription unique index migration: %v", err)
	}

	assertIndexExists(t, db, "subscriptions", "idx_subscriptions_user_source")

	var definition string
	if err := db.Raw(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, "idx_subscriptions_user_source").Scan(&definition).Error; err != nil {
		t.Fatalf("load sqlite index definition: %v", err)
	}
	if !strings.Contains(strings.ToLower(definition), "where deleted_at is null") {
		t.Fatalf("expected partial unique index on live subscriptions, got %q", definition)
	}
}

func TestRunSubscriptionUniqueIndexDeduplicatesOnlyLiveRows(t *testing.T) {
	db := testdb.Open(t)
	if err := db.Exec(`
CREATE TABLE subscriptions (
	id text PRIMARY KEY,
	created_at datetime,
	updated_at datetime,
	deleted_at datetime,
	user_id text NOT NULL,
	feed_source_id text NOT NULL,
	subscription_group_id text
);
`).Error; err != nil {
		t.Fatalf("create legacy subscriptions table: %v", err)
	}

	userID := uuid.NewString()
	feedSourceID := uuid.NewString()
	oldLiveID := uuid.NewString()
	newLiveID := uuid.NewString()
	deletedID := uuid.NewString()
	oldCreatedAt := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	newCreatedAt := oldCreatedAt.Add(time.Hour)
	deletedAt := newCreatedAt.Add(time.Hour)

	if err := db.Exec(`
INSERT INTO subscriptions (id, created_at, updated_at, deleted_at, user_id, feed_source_id)
VALUES (?, ?, ?, NULL, ?, ?),
       (?, ?, ?, NULL, ?, ?),
       (?, ?, ?, ?, ?, ?)`,
		oldLiveID, oldCreatedAt, oldCreatedAt, userID, feedSourceID,
		newLiveID, newCreatedAt, newCreatedAt, userID, feedSourceID,
		deletedID, oldCreatedAt, deletedAt, deletedAt, userID, feedSourceID,
	).Error; err != nil {
		t.Fatalf("seed duplicate subscriptions: %v", err)
	}

	if err := RunSubscriptionUniqueIndex(db); err != nil {
		t.Fatalf("run subscription unique index migration: %v", err)
	}

	var activeIDs []string
	if err := db.Raw(`
SELECT id FROM subscriptions
WHERE user_id = ? AND feed_source_id = ? AND deleted_at IS NULL
ORDER BY created_at ASC`, userID, feedSourceID).Scan(&activeIDs).Error; err != nil {
		t.Fatalf("load active subscription ids: %v", err)
	}
	if len(activeIDs) != 1 || activeIDs[0] != newLiveID {
		t.Fatalf("expected newest live subscription %s only, got %#v", newLiveID, activeIDs)
	}

	var totalCount int64
	if err := db.Raw(`SELECT COUNT(*) FROM subscriptions WHERE user_id = ? AND feed_source_id = ?`, userID, feedSourceID).Scan(&totalCount).Error; err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	if totalCount != 2 {
		t.Fatalf("expected newest live row plus soft-deleted history, got %d rows", totalCount)
	}

	if err := db.Exec(`
INSERT INTO subscriptions (id, created_at, updated_at, user_id, feed_source_id)
VALUES (?, ?, ?, ?, ?)`, uuid.NewString(), deletedAt, deletedAt, userID, feedSourceID).Error; err == nil {
		t.Fatal("expected duplicate live subscription insert to fail")
	}
	if err := db.Exec(`
INSERT INTO subscriptions (id, created_at, updated_at, deleted_at, user_id, feed_source_id)
VALUES (?, ?, ?, ?, ?, ?)`, uuid.NewString(), deletedAt, deletedAt, deletedAt, userID, feedSourceID).Error; err != nil {
		t.Fatalf("expected soft-deleted duplicate insert to succeed: %v", err)
	}
}

func TestRunSubscriptionGroupUniqueIndexDeduplicatesExistingRows(t *testing.T) {
	db := testdb.Open(t)
	if err := db.Exec(`
CREATE TABLE subscription_groups (
	id text PRIMARY KEY,
	created_at datetime,
	updated_at datetime,
	deleted_at datetime,
	user_id text NOT NULL,
	name text NOT NULL
);
`).Error; err != nil {
		t.Fatalf("create legacy subscription_groups table: %v", err)
	}
	if err := db.Exec(`
CREATE TABLE subscriptions (
	id text PRIMARY KEY,
	created_at datetime,
	updated_at datetime,
	deleted_at datetime,
	user_id text NOT NULL,
	feed_source_id text NOT NULL,
	subscription_group_id text
);
`).Error; err != nil {
		t.Fatalf("create legacy subscriptions table: %v", err)
	}

	userID := uuid.NewString()
	keepID := uuid.NewString()
	duplicateID := uuid.NewString()
	subscriptionID := uuid.NewString()
	feedSourceID := uuid.NewString()
	oldCreatedAt := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	newCreatedAt := oldCreatedAt.Add(time.Hour)
	if err := db.Exec(`
INSERT INTO subscription_groups (id, created_at, updated_at, user_id, name)
VALUES (?, ?, ?, ?, ?), (?, ?, ?, ?, ?)`,
		keepID, oldCreatedAt, oldCreatedAt, userID, "默认分组",
		duplicateID, newCreatedAt, newCreatedAt, userID, "默认分组",
	).Error; err != nil {
		t.Fatalf("seed duplicate subscription groups: %v", err)
	}
	if err := db.Exec(`
INSERT INTO subscriptions (id, created_at, updated_at, user_id, feed_source_id, subscription_group_id)
VALUES (?, ?, ?, ?, ?, ?)`,
		subscriptionID, newCreatedAt, newCreatedAt, userID, feedSourceID, duplicateID,
	).Error; err != nil {
		t.Fatalf("seed subscription for duplicate group: %v", err)
	}

	if err := RunSubscriptionGroupUniqueIndex(db); err != nil {
		t.Fatalf("run subscription group unique index migration: %v", err)
	}

	var groups []model.SubscriptionGroup
	if err := db.Find(&groups).Error; err != nil {
		t.Fatalf("load subscription groups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups=%d", len(groups))
	}
	if groups[0].ID.String() != keepID {
		t.Fatalf("expected oldest duplicate retained, got %s", groups[0].ID)
	}

	var subscriptionGroupID string
	if err := db.Raw(`SELECT subscription_group_id FROM subscriptions WHERE id = ?`, subscriptionID).Scan(&subscriptionGroupID).Error; err != nil {
		t.Fatalf("load reassigned subscription group id: %v", err)
	}
	if subscriptionGroupID != keepID {
		t.Fatalf("expected subscription reassigned to %s, got %s", keepID, subscriptionGroupID)
	}

	assertIndexExists(t, db, "subscription_groups", "idx_subscription_groups_user_name")
}
