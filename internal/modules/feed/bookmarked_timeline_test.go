package feed

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"
)

func TestGetBookmarkedFeedCombinesContentTypesByBookmarkTime(t *testing.T) {
	service, db, viewer, creator, channel := newUnifiedSubscriptionFixture(t)
	testdb.Migrate(t, db, &model.Bookmark{}, &model.VideoBookmark{}, &model.PodcastEpisodeBookmark{})

	post, episode, video := seedUnifiedChannelUpdates(t, db, creator, channel)
	base := time.Now().UTC().Add(-time.Hour)
	blogBookmark := model.Bookmark{UserID: viewer.ID, ContentID: post.ID}
	if err := db.Create(&blogBookmark).Error; err != nil {
		t.Fatalf("create blog bookmark: %v", err)
	}
	if err := db.Model(&blogBookmark).Update("created_at", base).Error; err != nil {
		t.Fatalf("set blog bookmark time: %v", err)
	}
	videoBookmark := model.VideoBookmark{UserID: viewer.ID, VideoID: video.ID}
	if err := db.Create(&videoBookmark).Error; err != nil {
		t.Fatalf("create video bookmark: %v", err)
	}
	if err := db.Model(&videoBookmark).Update("created_at", base.Add(time.Minute)).Error; err != nil {
		t.Fatalf("set video bookmark time: %v", err)
	}
	favorite := model.PodcastEpisodeBookmark{UserID: viewer.ID, EpisodeID: episode.ID, Kind: "favorite"}
	if err := db.Create(&favorite).Error; err != nil {
		t.Fatalf("create podcast favorite: %v", err)
	}
	if err := db.Model(&favorite).Update("created_at", base.Add(2*time.Minute)).Error; err != nil {
		t.Fatalf("set podcast favorite time: %v", err)
	}
	if err := db.Create(&model.PodcastEpisodeBookmark{UserID: viewer.ID, EpisodeID: episode.ID, Kind: "listen_later"}).Error; err != nil {
		t.Fatalf("create listen later bookmark: %v", err)
	}

	items, total, err := service.GetBookmarkedFeed(authctx.CurrentUser{ID: viewer.ID}, FeedQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("get bookmarked feed: %v", err)
	}
	if total != 3 || len(items) != 3 {
		t.Fatalf("items=%d total=%d, want 3", len(items), total)
	}
	if items[0].PodcastEpisode == nil || items[0].PodcastEpisode.ID != episode.ID {
		t.Fatalf("newest favorite must be the podcast episode: %#v", items[0])
	}
	if items[1].Video == nil || items[1].Video.ID != video.ID {
		t.Fatalf("second favorite must be the video: %#v", items[1])
	}
	if items[2].Post == nil || items[2].Post.ID != post.ID {
		t.Fatalf("oldest favorite must be the post: %#v", items[2])
	}
}
