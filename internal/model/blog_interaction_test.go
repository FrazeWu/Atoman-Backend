package model

import (
	"testing"

	"atoman/internal/testdb"
)

func TestBlogLikeAndBookmarkAreUniquePerUserTarget(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &User{}, &Post{}, &Like{}, &Bookmark{})

	user := User{Username: "blog-interaction-user", Email: "blog-interaction@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	post := Post{UserID: user.UUID, Title: "Post", Content: "Body", Status: "published", Visibility: "public", AllowComments: true}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}

	if err := db.Create(&Like{UserID: user.UUID, TargetType: "post", TargetID: post.ID}).Error; err != nil {
		t.Fatalf("create like: %v", err)
	}
	if err := db.Create(&Like{UserID: user.UUID, TargetType: "post", TargetID: post.ID}).Error; err == nil {
		t.Fatal("expected duplicate like to fail")
	}

	if err := db.Create(&Bookmark{UserID: user.UUID, PostID: post.ID}).Error; err != nil {
		t.Fatalf("create bookmark: %v", err)
	}
	if err := db.Create(&Bookmark{UserID: user.UUID, PostID: post.ID}).Error; err == nil {
		t.Fatal("expected duplicate bookmark to fail")
	}
}

func TestMediaBookmarksAreUniquePerUserTarget(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&User{},
		&Channel{},
		&Post{},
		&Video{},
		&PodcastEpisode{},
		&VideoBookmark{},
		&PodcastEpisodeBookmark{},
		&ChannelBookmark{},
	)

	user := User{Username: "media-bookmark-user", Email: "media-bookmark@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	channel := Channel{Name: "Media Channel", Slug: "media-channel"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	video := Video{
		UserID:      user.UUID,
		ChannelID:   &channel.ID,
		Title:       "Video",
		StorageType: "external",
		VideoURL:    "https://example.com/video.mp4",
		Status:      "published",
		Visibility:  "public",
	}
	if err := db.Create(&video).Error; err != nil {
		t.Fatalf("create video: %v", err)
	}

	post := Post{UserID: user.UUID, ChannelID: &channel.ID, Title: "Episode", Content: "Shownotes", Status: "published", Visibility: "public"}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}

	episode := PodcastEpisode{PostID: post.ID, ChannelID: channel.ID, AudioURL: "https://example.com/episode.mp3"}
	if err := db.Create(&episode).Error; err != nil {
		t.Fatalf("create episode: %v", err)
	}

	if err := db.Create(&VideoBookmark{UserID: user.UUID, VideoID: video.ID}).Error; err != nil {
		t.Fatalf("create video bookmark: %v", err)
	}
	if err := db.Create(&VideoBookmark{UserID: user.UUID, VideoID: video.ID}).Error; err == nil {
		t.Fatal("expected duplicate video bookmark to fail")
	}

	if err := db.Create(&PodcastEpisodeBookmark{UserID: user.UUID, EpisodeID: episode.ID}).Error; err != nil {
		t.Fatalf("create podcast episode bookmark: %v", err)
	}
	if err := db.Create(&PodcastEpisodeBookmark{UserID: user.UUID, EpisodeID: episode.ID}).Error; err == nil {
		t.Fatal("expected duplicate podcast episode bookmark to fail")
	}

	if err := db.Create(&ChannelBookmark{UserID: user.UUID, ChannelID: channel.ID, Kind: "video_channel"}).Error; err != nil {
		t.Fatalf("create channel bookmark: %v", err)
	}
	if err := db.Create(&ChannelBookmark{UserID: user.UUID, ChannelID: channel.ID, Kind: "video_channel"}).Error; err == nil {
		t.Fatal("expected duplicate channel bookmark to fail")
	}
	if err := db.Create(&ChannelBookmark{UserID: user.UUID, ChannelID: channel.ID, Kind: "podcast_show"}).Error; err != nil {
		t.Fatalf("create second-kind channel bookmark: %v", err)
	}
}
