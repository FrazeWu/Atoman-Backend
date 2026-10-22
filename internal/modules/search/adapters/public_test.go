package adapters

import (
	"context"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/search"
	"atoman/internal/testdb"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPublicSearchLinksTargetMatchedContent(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{}, &model.Post{}, &model.PodcastEpisode{}, &model.FeedSource{}, &model.FeedItem{}, &model.TimelineEvent{})
	user := model.User{Username: "search-links", Email: "search-links@example.test", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	channel := model.Channel{Name: "搜索节目", Slug: "search-show", UserID: &user.UUID}
	require.NoError(t, db.Create(&channel).Error)
	post := model.Post{UserID: user.UUID, ChannelID: &channel.ID, Title: "搜索单集", Content: "正文", Status: "published", Visibility: "public"}
	require.NoError(t, db.Create(&post).Error)
	episode := model.PodcastEpisode{PostID: post.ID, ChannelID: channel.ID, AudioURL: "/audio.mp3"}
	require.NoError(t, db.Create(&episode).Error)
	source := model.FeedSource{Title: "搜索来源", SourceType: "external_rss", Hash: "search-links"}
	require.NoError(t, db.Create(&source).Error)
	article := model.FeedItem{Title: "搜索文章", FeedSourceID: source.ID, GUID: "search-links", PublishedAt: time.Now()}
	require.NoError(t, db.Create(&article).Error)
	event := model.TimelineEvent{UserID: user.UUID, Title: "搜索事件", EventDate: time.Now(), IsPublic: true}
	require.NoError(t, db.Create(&event).Error)
	for _, tc := range []struct{ module, kind, href string }{
		{"feed", "source", "/feed?source_id=" + source.ID.String()},
		{"feed", "article", "/feed/item/" + article.ID.String()},
		{"podcast", "show", "/podcasts/show/" + channel.Slug},
		{"timeline", "event", "/timeline?event=" + event.ID.String()},
	} {
		t.Run(tc.module+"/"+tc.kind, func(t *testing.T) {
			provider := NewPublicProvider(db, tc.module, tc.module, PublicType(tc.kind, tc.kind))
			result, err := provider.Search(context.Background(), search.Viewer{}, search.ProviderSearchRequest{Scope: search.ScopePublic, Type: tc.kind, Query: "搜索", Sort: "relevance", Limit: 6})
			require.NoError(t, err)
			require.Len(t, result.Items, 1)
			require.Equal(t, tc.href, result.Items[0].Href)
			require.NoError(t, search.ValidateSearchHit(search.NormalizeSearchHit(result.Items[0])))
		})
	}
	// 深链接修复不能放宽公开可见性。
	require.NoError(t, db.Model(&source).Update("hidden", true).Error)
	for _, kind := range []string{"source", "article"} {
		result, err := NewPublicProvider(db, "feed", "feed").Search(context.Background(), search.Viewer{}, search.ProviderSearchRequest{Scope: search.ScopePublic, Type: kind, Query: "搜索", Limit: 6})
		require.NoError(t, err)
		require.Empty(t, result.Items)
	}
}

func TestPublicMediaSearchReadsCanonicalContent(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{}, &model.ContentEntry{}, &model.ContentEpisodeExtension{}, &model.ContentVideoExtension{})
	user := model.User{Username: "canonical-search", Email: "canonical-search@example.test", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	channel := model.Channel{Name: "新版节目", Slug: "canonical-show", UserID: &user.UUID}
	require.NoError(t, db.Create(&channel).Error)
	ids := map[string]string{}
	for _, kind := range []string{"podcast", "video"} {
		for _, state := range []string{"published", "draft", "private"} {
			entry := model.ContentEntry{AuthorID: &user.UUID, ChannelID: channel.ID, Kind: kind, Title: "新版内容", Summary: "新版内容摘要", Status: state, Visibility: "public"}
			if state == "private" {
				entry.Status, entry.Visibility = "published", "private"
			}
			require.NoError(t, db.Create(&entry).Error)
			id := uuid.New()
			if state == "published" {
				ids[kind] = id.String()
			}
			if kind == "podcast" {
				require.NoError(t, db.Create(&model.ContentEpisodeExtension{ContentID: entry.ID, EpisodeID: id, LegacyPostID: entry.ID, Shownotes: "新版正文命中"}).Error)
			} else {
				require.NoError(t, db.Create(&model.ContentVideoExtension{ContentID: entry.ID, VideoID: id}).Error)
			}
		}
	}
	for _, tc := range []struct{ module, kind, href string }{
		{"podcast", "show", "/podcasts/show/canonical-show"},
		{"podcast", "episode", "/podcasts/episode/" + ids["podcast"]},
		{"video", "video", "/videos/watch/" + ids["video"]},
	} {
		result, err := NewPublicProvider(db, tc.module, tc.module).Search(context.Background(), search.Viewer{}, search.ProviderSearchRequest{Scope: search.ScopePublic, Type: tc.kind, Query: "新版", Sort: "relevance", Limit: 20, ExactTotal: true})
		require.NoError(t, err)
		require.Len(t, result.Items, 1, tc.kind)
		require.EqualValues(t, 1, *result.Total)
		require.Equal(t, tc.href, result.Items[0].Href)
	}
}
