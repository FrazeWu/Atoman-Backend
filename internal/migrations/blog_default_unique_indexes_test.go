package migrations

import (
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestRunBlogDefaultUniqueIndexesCreatesExpectedIndexes(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{}, &model.Collection{})

	if err := RunBlogDefaultUniqueIndexes(db); err != nil {
		t.Fatalf("run blog default unique indexes migration: %v", err)
	}

	assertIndexExists(t, db, "channels", "idx_channels_user_default")
	assertIndexExists(t, db, "collections", "idx_collections_channel_default")

	var channelDefinition string
	if err := db.Raw(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, "idx_channels_user_default").Scan(&channelDefinition).Error; err != nil {
		t.Fatalf("load channel index definition: %v", err)
	}
	if !strings.Contains(strings.ToLower(channelDefinition), "where user_id is not null and is_default = true") {
		t.Fatalf("expected partial channel default index, got %q", channelDefinition)
	}

	var collectionDefinition string
	if err := db.Raw(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, "idx_collections_channel_default").Scan(&collectionDefinition).Error; err != nil {
		t.Fatalf("load collection index definition: %v", err)
	}
	if !strings.Contains(strings.ToLower(collectionDefinition), "where is_default = true") {
		t.Fatalf("expected partial collection default index, got %q", collectionDefinition)
	}
}

func TestRunBlogDefaultUniqueIndexesDeduplicatesDefaultChannelsAndCollections(t *testing.T) {
	db := testdb.Open(t)
	if err := db.Exec(`
CREATE TABLE channels (
	id TEXT PRIMARY KEY,
	created_at DATETIME,
	updated_at DATETIME,
	deleted_at DATETIME,
	user_id TEXT,
	name TEXT NOT NULL,
	slug TEXT,
	description TEXT,
	cover_url TEXT,
	is_default NUMERIC DEFAULT 0,
	is_anonymous NUMERIC DEFAULT 0,
	ban_until DATETIME,
	ban_reason TEXT
)`).Error; err != nil {
		t.Fatalf("create legacy channels table: %v", err)
	}
	if err := db.Exec(`
CREATE TABLE collections (
	id TEXT PRIMARY KEY,
	created_at DATETIME,
	updated_at DATETIME,
	deleted_at DATETIME,
	channel_id TEXT NOT NULL,
	created_by TEXT,
	name TEXT NOT NULL,
	description TEXT,
	cover_url TEXT,
	is_default NUMERIC DEFAULT 0
)`).Error; err != nil {
		t.Fatalf("create legacy collections table: %v", err)
	}

	userID := uuid.NewString()
	keepChannelID := uuid.NewString()
	dropChannelID := uuid.NewString()
	keepCollectionID := uuid.NewString()
	dropCollectionID := uuid.NewString()
	oldCreatedAt := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	newCreatedAt := oldCreatedAt.Add(time.Hour)

	if err := db.Exec(`
INSERT INTO channels (id, created_at, updated_at, user_id, name, slug, is_default)
VALUES (?, ?, ?, ?, ?, ?, 1),
       (?, ?, ?, ?, ?, ?, 1)`,
		keepChannelID, oldCreatedAt, oldCreatedAt, userID, "Alice", "alice", 
		dropChannelID, newCreatedAt, newCreatedAt, userID, "Alice 2", "alice-2",
	).Error; err != nil {
		t.Fatalf("seed duplicate default channels: %v", err)
	}

	if err := db.Exec(`
INSERT INTO collections (id, created_at, updated_at, channel_id, name, is_default)
VALUES (?, ?, ?, ?, ?, 1),
       (?, ?, ?, ?, ?, 1)`,
		keepCollectionID, oldCreatedAt, oldCreatedAt, keepChannelID, "默认专栏",
		dropCollectionID, newCreatedAt, newCreatedAt, keepChannelID, "默认专栏 2",
	).Error; err != nil {
		t.Fatalf("seed duplicate default collections: %v", err)
	}

	if err := RunBlogDefaultUniqueIndexes(db); err != nil {
		t.Fatalf("run blog default unique indexes migration: %v", err)
	}

	var liveDefaultChannels []string
	if err := db.Raw(`
SELECT id FROM channels
WHERE user_id = ? AND is_default = 1
ORDER BY created_at ASC`, userID).Scan(&liveDefaultChannels).Error; err != nil {
		t.Fatalf("load default channels: %v", err)
	}
	if len(liveDefaultChannels) != 1 || liveDefaultChannels[0] != keepChannelID {
		t.Fatalf("expected oldest default channel %s only, got %#v", keepChannelID, liveDefaultChannels)
	}

	var liveDefaultCollections []string
	if err := db.Raw(`
SELECT id FROM collections
WHERE channel_id = ? AND is_default = 1
ORDER BY created_at ASC`, keepChannelID).Scan(&liveDefaultCollections).Error; err != nil {
		t.Fatalf("load default collections: %v", err)
	}
	if len(liveDefaultCollections) != 1 || liveDefaultCollections[0] != keepCollectionID {
		t.Fatalf("expected oldest default collection %s only, got %#v", keepCollectionID, liveDefaultCollections)
	}
}
