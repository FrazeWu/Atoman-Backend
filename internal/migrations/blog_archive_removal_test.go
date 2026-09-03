package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunBlogArchiveRemovalMigrationConvertsOnlyBlogStatuses(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{}, &model.Post{}, &model.PodcastEpisode{}, &model.ContentEntry{})

	blogUser := model.User{Username: "blog-migration-user", Email: "blog-migration@example.com", Password: "hash"}
	if err := db.Create(&blogUser).Error; err != nil {
		t.Fatalf("create blog user: %v", err)
	}
	podcastUser := model.User{Username: "podcast-migration-user", Email: "podcast-migration@example.com", Password: "hash"}
	if err := db.Create(&podcastUser).Error; err != nil {
		t.Fatalf("create podcast user: %v", err)
	}
	blogChannel := model.Channel{UserID: &blogUser.UUID, Name: "Blog", Slug: "blog-migration"}
	if err := db.Create(&blogChannel).Error; err != nil {
		t.Fatalf("create blog channel: %v", err)
	}
	podcastChannel := model.Channel{UserID: &podcastUser.UUID, Name: "Podcast", Slug: "podcast-migration"}
	if err := db.Create(&podcastChannel).Error; err != nil {
		t.Fatalf("create podcast channel: %v", err)
	}

	blogPost := model.Post{UserID: blogUser.UUID, ChannelID: &blogChannel.ID, Title: "Blog", Content: "body", Status: "archived", Visibility: "public"}
	if err := db.Create(&blogPost).Error; err != nil {
		t.Fatalf("create blog post: %v", err)
	}
	podcastPost := model.Post{UserID: podcastUser.UUID, ChannelID: &podcastChannel.ID, Title: "Podcast", Content: "body", Status: "archived", Visibility: "public"}
	if err := db.Create(&podcastPost).Error; err != nil {
		t.Fatalf("create podcast post: %v", err)
	}
	if err := db.Create(&model.PodcastEpisode{PostID: podcastPost.ID, ChannelID: *podcastPost.ChannelID, AudioURL: "audio"}).Error; err != nil {
		t.Fatalf("create podcast episode: %v", err)
	}

	blogEntry := model.ContentEntry{AuthorID: &blogPost.UserID, ChannelID: *blogPost.ChannelID, Kind: "blog", Title: "Blog", Status: "archived", Visibility: "public"}
	if err := db.Create(&blogEntry).Error; err != nil {
		t.Fatalf("create blog entry: %v", err)
	}
	podcastEntry := model.ContentEntry{AuthorID: &podcastPost.UserID, ChannelID: *podcastPost.ChannelID, Kind: "podcast", Title: "Podcast", Status: "archived", Visibility: "public"}
	if err := db.Create(&podcastEntry).Error; err != nil {
		t.Fatalf("create podcast entry: %v", err)
	}

	if err := RunBlogArchiveRemovalMigration(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}
	if err := RunBlogArchiveRemovalMigration(db); err != nil {
		t.Fatalf("rerun migration: %v", err)
	}

	var gotBlogPost, gotPodcastPost model.Post
	if err := db.First(&gotBlogPost, "id = ?", blogPost.ID).Error; err != nil {
		t.Fatalf("load blog post: %v", err)
	}
	if err := db.First(&gotPodcastPost, "id = ?", podcastPost.ID).Error; err != nil {
		t.Fatalf("load podcast post: %v", err)
	}
	if gotBlogPost.Status != "draft" {
		t.Fatalf("blog post status = %q, want draft", gotBlogPost.Status)
	}
	if gotPodcastPost.Status != "archived" {
		t.Fatalf("podcast post status = %q, want archived", gotPodcastPost.Status)
	}

	var gotBlogEntry, gotPodcastEntry model.ContentEntry
	if err := db.First(&gotBlogEntry, "id = ?", blogEntry.ID).Error; err != nil {
		t.Fatalf("load blog entry: %v", err)
	}
	if err := db.First(&gotPodcastEntry, "id = ?", podcastEntry.ID).Error; err != nil {
		t.Fatalf("load podcast entry: %v", err)
	}
	if gotBlogEntry.Status != "draft" {
		t.Fatalf("blog entry status = %q, want draft", gotBlogEntry.Status)
	}
	if gotPodcastEntry.Status != "archived" {
		t.Fatalf("podcast entry status = %q, want archived", gotPodcastEntry.Status)
	}
}
