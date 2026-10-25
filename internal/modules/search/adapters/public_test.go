package adapters

import (
	"bytes"
	"context"
	"log"
	"testing"
	"time"

	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/modules/search"
	"atoman/internal/testdb"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestPublicSearchLinksTargetMatchedContent(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{}, &model.Post{}, &model.PodcastEpisode{}, &model.FeedSource{}, &model.FeedItem{}, &model.TimelineEvent{})
	require.NoError(t, migrations.RunFeedShortSearchIndexes(db))
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

func TestEmptyRSSSourcesRemainDiscoverable(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSource{}, &model.FeedItem{})
	for _, tc := range []struct {
		title, kind     string
		hidden, deleted bool
	}{
		{"empty_source", "external_rss", false, false},
		{"emptyXsource", "external_rss", false, false},
		{"empty_source_hidden", "external_rss", true, false},
		{"empty_source_deleted", "external_rss", false, true},
		{"empty_source_internal", "internal", false, false},
	} {
		source := model.FeedSource{Title: tc.title, SourceType: tc.kind, Hidden: tc.hidden, Hash: tc.title}
		require.NoError(t, db.Create(&source).Error)
		if tc.deleted {
			require.NoError(t, db.Delete(&source).Error)
		}
	}
	provider := NewPublicProvider(db, "feed", "RSS", PublicType("source", "来源"))
	result, err := provider.Search(context.Background(), search.Viewer{}, search.ProviderSearchRequest{
		Scope: search.ScopePublic, Type: "source", Query: "empty_", Sort: "relevance", Limit: 20, ExactTotal: true,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, *result.Total)
	require.Len(t, result.Items, 1)
	require.Equal(t, "empty_source", result.Items[0].Title)
	require.NoError(t, search.ValidateSearchHit(search.NormalizeSearchHit(result.Items[0])))
}

func TestFeedArticleSearchRetainsAllIndexedFields(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSource{}, &model.FeedItem{})
	require.NoError(t, migrations.RunFeedShortSearchIndexes(db))
	source := model.FeedSource{Title: "RSS", SourceType: "external_rss", Hash: "indexed-fields"}
	require.NoError(t, db.Create(&source).Error)
	provider := NewPublicProvider(db, "feed", "RSS")
	var queries bytes.Buffer
	provider.db = db.Session(&gorm.Session{Logger: logger.New(log.New(&queries, "", 0), logger.Config{LogLevel: logger.Info})})
	for _, field := range []string{"title", "summary", "author"} {
		t.Run(field, func(t *testing.T) {
			article := model.FeedItem{FeedSourceID: source.ID, GUID: field, Title: "Unrelated", PublishedAt: time.Now()}
			require.NoError(t, db.Create(&article).Error)
			require.NoError(t, db.Model(&article).Update(field, `MiXeD_百分%路径\中文`).Error)
			for _, query := range []string{`mixed_百分%路径\中文`, "中文"} {
				queries.Reset()
				result, err := provider.Search(context.Background(), search.Viewer{}, search.ProviderSearchRequest{
					Scope: search.ScopePublic, Type: "article", Query: query, Sort: "relevance", Limit: 10, ExactTotal: true,
				})
				require.NoError(t, err)
				require.EqualValues(t, 1, *result.Total)
				require.Len(t, result.Items, 1)
				require.Equal(t, article.ID.String(), result.Items[0].ID)
				// 防止退回 ILIKE 原列，导致现有 LOWER 表达式索引失效。
				for _, indexedField := range []string{"title", "summary", "author"} {
					require.Contains(t, queries.String(), "LOWER(i."+indexedField+") LIKE LOWER(")
				}
			}
			require.NoError(t, db.Delete(&article).Error)
		})
	}
}

func TestPodcastShowRequiresPublicEpisode(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{}, &model.ContentEntry{}, &model.ContentEpisodeExtension{})
	user := model.User{Username: "show-qualification", Email: "show@example.test", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	for _, state := range []string{"empty", "draft", "private", "deleted", "published"} {
		channel := model.Channel{Name: "节目 " + state, Slug: "show-" + state, UserID: &user.UUID}
		require.NoError(t, db.Create(&channel).Error)
		if state == "empty" {
			continue
		}
		entry := model.ContentEntry{AuthorID: &user.UUID, ChannelID: channel.ID, Kind: "podcast", Title: "单集", Status: "published", Visibility: "public"}
		if state == "draft" {
			entry.Status = "draft"
		}
		if state == "private" {
			entry.Visibility = "private"
		}
		require.NoError(t, db.Create(&entry).Error)
		require.NoError(t, db.Create(&model.ContentEpisodeExtension{ContentID: entry.ID, EpisodeID: uuid.New(), LegacyPostID: entry.ID}).Error)
		if state == "deleted" {
			require.NoError(t, db.Delete(&entry).Error)
		}
	}
	result, err := NewPublicProvider(db, "podcast", "播客").Search(context.Background(), search.Viewer{}, search.ProviderSearchRequest{
		Scope: search.ScopePublic, Type: "show", Query: "节目", Sort: "relevance", Limit: 20, ExactTotal: true,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, *result.Total)
	require.Len(t, result.Items, 1)
	require.Equal(t, "/podcasts/show/show-published", result.Items[0].Href)
}
