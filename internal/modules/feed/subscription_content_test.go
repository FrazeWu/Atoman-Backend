package feed

import (
	"testing"

	"atoman/internal/model"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPlatformSubscriptionTargetRecognizesYouTubeChannel(t *testing.T) {
	target, ok := platformSubscriptionTargetURL("https://www.youtube.com/channel/UC1234567890")
	require.True(t, ok)
	require.Equal(t, "rsshub", target.Provider)
	require.Equal(t, "video", target.ContentType)
	require.Equal(t, "video", target.Category)
	require.Equal(t, "https://rsshub.app/youtube/channel/UC1234567890", target.RssURL)
}

func TestTimelineFeedItemTypeMarksGitHubSourcesAsProjectUpdates(t *testing.T) {
	source := &model.FeedSource{Provider: "rsshub", RssURL: "https://rsshub.app/github/repo/acme/project"}
	item := &model.FeedItem{FeedSource: source}
	require.Equal(t, "project_update", timelineFeedItemType(item))
}

func TestTimelineFeedItemTypeRecognizesLegacyGitHubRSSSources(t *testing.T) {
	source := &model.FeedSource{Provider: "rss", RssURL: "https://rsshub.app/github/repo/acme/project"}
	item := &model.FeedItem{FeedSource: source}
	require.Equal(t, "project_update", timelineFeedItemType(item))
}

func TestTimelineHelpersSortUnifiedTypesWithStableIDs(t *testing.T) {
	noteID := uuid.New()
	item := TimelineItemDTO{Type: "short_note", ShortNote: &model.ShortNote{Base: model.Base{ID: noteID}}}
	require.Equal(t, noteID.String(), timelineItemID(item))
	require.Equal(t, 1, timelineTypeRank("short_note"))
	require.Equal(t, 4, timelineTypeRank("project_update"))
}

func TestPlatformSubscriptionTargetRecognizesYouTubePlaylist(t *testing.T) {
	target, ok := platformSubscriptionTargetURL("https://www.youtube.com/playlist?list=PL123456")
	require.True(t, ok)
	require.Equal(t, "https://rsshub.app/youtube/playlist/PL123456", target.RssURL)
}

func TestPlatformSubscriptionTargetRecognizesBilibiliUser(t *testing.T) {
	target, ok := platformSubscriptionTargetURL("https://space.bilibili.com/2267573")
	require.True(t, ok)
	require.Equal(t, "video", target.ContentType)
	require.Equal(t, "https://rsshub.app/bilibili/user/video/2267573", target.RssURL)
}
