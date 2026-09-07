package migrationrunner

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunCreatesCoreSchema(t *testing.T) {
	db := testdb.Open(t)

	if err := Run(db); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, schemaModel := range []any{
		&model.User{},
		&model.AuthSession{},
		&model.Artist{},
		&model.Album{},
		&model.Song{},
		&model.SongCorrection{},
		&model.AlbumCorrection{},
		&model.ArtistCorrection{},
		&model.ArtistAlias{},
		&model.ArtistMerge{},
		&model.BlogDraft{},
		&model.Like{},
		&model.Follow{},
		&model.ActivityLog{},
		&model.TimelineEvent{},
		&model.TimelinePerson{},
		&model.PersonLocation{},
		&model.FeedSource{},
		&model.DMConversation{},
		&model.DiscussionTarget{},
	} {
		if !db.Migrator().HasTable(schemaModel) {
			t.Fatalf("expected table for %T", schemaModel)
		}
	}
	if !db.Migrator().HasIndex("content_publication_events", "idx_content_publication_events_dispatch_candidates") {
		t.Fatal("expected content publication dispatch candidate index")
	}
}

func TestRunCreatesShortNoteSchema(t *testing.T) {
	db := testdb.Open(t)

	if err := Run(db); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, schemaModel := range []any{
		&model.ShortNote{},
		&model.ShortNoteMedia{},
	} {
		if !db.Migrator().HasTable(schemaModel) {
			t.Fatalf("expected table for %T", schemaModel)
		}
	}
	if !db.Migrator().HasIndex("short_note_media", "idx_short_note_media_short_note_id") {
		t.Fatal("expected short_note_media short_note_id index")
	}

	user := model.User{Username: "short-note-owner", Email: "short-note-owner@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create short note owner: %v", err)
	}
	note := model.ShortNote{UserID: user.UUID, Content: "short note"}
	if err := db.Create(&note).Error; err != nil {
		t.Fatalf("create short note: %v", err)
	}
	media := model.ShortNoteMedia{ShortNoteID: note.ID, URL: "https://example.com/media.jpg", Position: 1}
	if err := db.Create(&media).Error; err != nil {
		t.Fatalf("create short note media: %v", err)
	}

	var loaded model.ShortNote
	if err := db.Preload("Media").First(&loaded, "id = ?", note.ID).Error; err != nil {
		t.Fatalf("load short note with media: %v", err)
	}
	if len(loaded.Media) != 1 {
		t.Fatalf("expected 1 media item, got %d", len(loaded.Media))
	}
	if loaded.Media[0].ShortNoteID != note.ID || loaded.Media[0].URL != media.URL || loaded.Media[0].Position != media.Position {
		t.Fatalf("unexpected preloaded media: %+v", loaded.Media[0])
	}
}

func TestBackfillUserDefaultResourcesUsesDisplayName(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.UserSettings{}, &model.Channel{}, &model.UserStudioState{},
		&model.ContentCollection{}, &model.FeedSource{}, &model.SubscriptionGroup{}, &model.Subscription{},
		&model.BookmarkFolder{},
	)
	user := model.User{Username: "legacy-user", DisplayName: "存量用户", Email: "legacy-user@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	channel := model.Channel{UserID: &user.UUID, Name: user.Username, Slug: user.Username, Description: "默认合集"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create legacy channel: %v", err)
	}
	collection := model.ContentCollection{ChannelID: channel.ID, CreatedBy: &user.UUID, Name: "legacy-user的合集", Description: "默认合集", IsDefault: true}
	if err := db.Create(&collection).Error; err != nil {
		t.Fatalf("create legacy collection: %v", err)
	}
	if err := db.Create(&model.UserStudioState{UserID: user.UUID, ChannelID: &channel.ID}).Error; err != nil {
		t.Fatalf("create studio state: %v", err)
	}

	if err := backfillUserDefaultResources(db); err != nil {
		t.Fatalf("backfillUserDefaultResources: %v", err)
	}

	var migratedChannel model.Channel
	if err := db.First(&migratedChannel, "id = ?", channel.ID).Error; err != nil {
		t.Fatalf("load migrated channel: %v", err)
	}
	if migratedChannel.Name != user.DisplayName {
		t.Fatalf("channel name = %q, want %q", migratedChannel.Name, user.DisplayName)
	}
	var migratedCollection model.ContentCollection
	if err := db.First(&migratedCollection, "id = ?", collection.ID).Error; err != nil {
		t.Fatalf("load migrated collection: %v", err)
	}
	if migratedCollection.Name != user.DisplayName+"的合集" {
		t.Fatalf("collection name = %q, want %q", migratedCollection.Name, user.DisplayName+"的合集")
	}
	var source model.FeedSource
	if err := db.Where("source_type = ? AND source_id = ?", "internal_user", user.UUID).First(&source).Error; err != nil {
		t.Fatalf("load migrated source: %v", err)
	}
	if source.Title != user.DisplayName {
		t.Fatalf("source title = %q, want %q", source.Title, user.DisplayName)
	}
}
