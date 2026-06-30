package migrations

import (
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
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
