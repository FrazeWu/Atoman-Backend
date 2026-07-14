package migrations

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestRunBlogSingleCollectionMigrationBackfillsAndRemovesLegacyTables(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.Collection{},
		&model.Post{},
		&model.PostCollection{},
		&model.BlogDraft{},
		&legacyBlogPostRating{},
	)

	user := model.User{Username: "migration-author", Email: "migration@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	channel := model.Channel{UserID: &user.UUID, Name: "Migration", Slug: "migration", ContentType: "blog", IsDefault: true}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	defaultCollection := model.Collection{ChannelID: channel.ID, CreatedBy: &user.UUID, Name: "默认专栏", IsDefault: true}
	secondaryCollection := model.Collection{ChannelID: channel.ID, CreatedBy: &user.UUID, Name: "Secondary"}
	if err := db.Create(&defaultCollection).Error; err != nil {
		t.Fatalf("create default collection: %v", err)
	}
	if err := db.Create(&secondaryCollection).Error; err != nil {
		t.Fatalf("create secondary collection: %v", err)
	}

	publishedAt := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	post := model.Post{Base: model.Base{ID: uuid.New(), CreatedAt: publishedAt}, UserID: user.UUID, ChannelID: &channel.ID, Title: "Published", Content: "body", Status: "published", Visibility: "public"}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	if err := db.Create(&model.PostCollection{PostID: post.ID, CollectionID: defaultCollection.ID, Position: 1}).Error; err != nil {
		t.Fatalf("create default link: %v", err)
	}
	if err := db.Create(&model.PostCollection{PostID: post.ID, CollectionID: secondaryCollection.ID, Position: 0}).Error; err != nil {
		t.Fatalf("create secondary link: %v", err)
	}
	if err := db.Create(&legacyBlogPostRating{PostID: post.ID, UserID: user.UUID, Score: 8}).Error; err != nil {
		t.Fatalf("create rating: %v", err)
	}

	unassigned := model.Post{UserID: user.UUID, ChannelID: &channel.ID, Title: "Unassigned", Content: "body", Status: "draft", Visibility: "public"}
	if err := db.Create(&unassigned).Error; err != nil {
		t.Fatalf("create unassigned post: %v", err)
	}

	if err := RunBlogSingleCollectionMigration(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}
	if err := RunBlogSingleCollectionMigration(db); err != nil {
		t.Fatalf("rerun migration: %v", err)
	}

	type migratedPost struct {
		ID                 uuid.UUID
		CollectionID       *uuid.UUID
		CollectionPosition int
		PublishedAt        *time.Time
	}
	var migrated migratedPost
	if err := db.Table("posts").Where("id = ?", post.ID).First(&migrated).Error; err != nil {
		t.Fatalf("load migrated post: %v", err)
	}
	if migrated.CollectionID == nil || *migrated.CollectionID != secondaryCollection.ID || migrated.CollectionPosition != 0 {
		t.Fatalf("expected first ordered collection, got %#v", migrated)
	}
	if migrated.PublishedAt == nil || !migrated.PublishedAt.Equal(publishedAt) {
		t.Fatalf("expected published_at=%s, got %#v", publishedAt, migrated.PublishedAt)
	}

	var migratedUnassigned migratedPost
	if err := db.Table("posts").Where("id = ?", unassigned.ID).First(&migratedUnassigned).Error; err != nil {
		t.Fatalf("load migrated unassigned post: %v", err)
	}
	if migratedUnassigned.CollectionID == nil || *migratedUnassigned.CollectionID != defaultCollection.ID {
		t.Fatalf("expected default collection, got %#v", migratedUnassigned.CollectionID)
	}
	if db.Migrator().HasTable("post_collections") || db.Migrator().HasTable("blog_post_ratings") {
		t.Fatal("expected legacy collection and rating tables to be removed")
	}
}
