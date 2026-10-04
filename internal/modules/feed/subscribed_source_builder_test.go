package feed

import (
	"testing"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
)

func TestBuildSubscribedFeedSourcesSkipsPausedAndBlogExternalSources(t *testing.T) {
	userID := uuid.New()
	channelID := uuid.New()
	externalID := uuid.New()
	resumed := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	sources := buildSubscribedFeedSources([]model.Subscription{
		{IsPaused: true, FeedSource: &model.FeedSource{SourceType: "internal_user", SourceID: &userID}},
		{FeedSource: &model.FeedSource{SourceType: "internal_channel", SourceID: &channelID}},
		{ResumedAfter: &resumed, FeedSource: &model.FeedSource{Base: model.Base{ID: externalID}, SourceType: "external_rss"}},
	}, nil, FeedQuery{ContentType: "blog"})

	if len(sources.userIDs) != 0 || len(sources.channelIDs) != 1 || sources.channelIDs[0] != channelID {
		t.Fatalf("unexpected internal source set: %#v", sources)
	}
	if len(sources.feedSourceIDs) != 0 || len(sources.visibleAfter) != 0 {
		t.Fatalf("blog query included external source: %#v", sources)
	}
}
