package feed

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestFeedSearchSnippetStripsHTMLAndCentersTheMatch(t *testing.T) {
	snippet := feedSearchSnippet("<p>before <strong>reader body phrase</strong> after the match keeps going "+strings.Repeat("word ", 40)+"</p>", "reader body phrase")
	require.Contains(t, snippet, "reader body phrase")
	require.NotContains(t, snippet, "<p>")
	require.NotContains(t, snippet, "<strong>")
}

func TestFeedSearchFieldWeightPrefersTitleOverBody(t *testing.T) {
	titleWeight, titleField := feedSearchFieldWeight("citrus enzyme weekly", "other", "", "", "citrus enzyme", true)
	bodyWeight, bodyField := feedSearchFieldWeight("unrelated", "no", "", "", "citrus enzyme", true)
	require.Equal(t, 80, titleWeight)
	require.Equal(t, "title", titleField)
	require.Equal(t, 20, bodyWeight)
	require.Equal(t, "body", bodyField)
	require.Greater(t, titleWeight, bodyWeight)
}

func TestSearchSubscribedFeedRanksTitleAboveBodyAndSnippetsPlainText(t *testing.T) {
	service, db, user := newFeedTestService(t)
	require.NoError(t, migrations.RunFeedFullTextSearchIndexes(db))

	var source model.FeedSource
	require.NoError(t, db.Where("source_type = ?", "external_rss").First(&source).Error)
	now := time.Now().UTC()
	bodyItem := model.FeedItem{
		FeedSourceID: source.ID,
		GUID:         "body-only",
		Title:        "unrelated headline",
		Link:         "https://example.com/body",
		Summary:      "no match here",
		ReaderHTML:   "<p>The reader body phrase sits beside citrus enzyme details.</p>",
		PublishedAt:  now,
		FetchedAt:    now,
	}
	titleItem := model.FeedItem{
		FeedSourceID: source.ID,
		GUID:         "title-match",
		Title:        "citrus enzyme weekly",
		Link:         "https://example.com/title",
		Summary:      "a weekly note",
		PublishedAt:  now.Add(-30 * 24 * time.Hour),
		FetchedAt:    now,
	}
	require.NoError(t, db.Create(&bodyItem).Error)
	require.NoError(t, db.Create(&titleItem).Error)

	hidden := model.FeedSource{SourceType: "external_rss", RssURL: "https://other.example.com/feed.xml", Hash: "unsubscribed-search", Title: "Other"}
	require.NoError(t, db.Create(&hidden).Error)
	require.NoError(t, db.Create(&model.FeedItem{
		FeedSourceID: hidden.ID, GUID: "hidden-body", Title: "elsewhere", Link: "https://other.example.com/item",
		ReaderHTML: "<p>reader body phrase</p>", PublishedAt: now, FetchedAt: now,
	}).Error)

	hits, total, err := service.SearchSubscribedFeed(user, FeedQuery{Page: 1, PageSize: 20, Search: "reader body phrase"}, feedSearchRankRelevance)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, hits, 1)
	require.Equal(t, bodyItem.ID, hits[0].FeedItem.ID)
	require.Equal(t, "body", hits[0].SearchMatchField)
	require.Contains(t, hits[0].SearchSnippet, "reader body phrase")
	require.NotContains(t, hits[0].SearchSnippet, "<p>")

	ranked, _, err := service.SearchSubscribedFeed(user, FeedQuery{Page: 1, PageSize: 20, Search: "citrus enzyme"}, feedSearchRankRelevance)
	require.NoError(t, err)
	require.NotEmpty(t, ranked)
	require.Equal(t, titleItem.ID, ranked[0].FeedItem.ID)
	require.Equal(t, "title", ranked[0].SearchMatchField)

	recent, _, err := service.SearchSubscribedFeed(user, FeedQuery{Page: 1, PageSize: 20, Search: "citrus enzyme"}, feedSearchRankRecent)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(recent), 2)
	require.Equal(t, bodyItem.ID, recent[0].FeedItem.ID)
}

func TestSearchSubscribedFeedMatchesBlogContent(t *testing.T) {
	service, db, user := newFeedTestService(t)
	var post model.Post
	require.NoError(t, db.Where("title = ?", "Channel post").First(&post).Error)
	require.NoError(t, db.Model(&model.ContentBlogExtension{}).Where("content_id = ?", post.ID).Update("content", "channel content unique zeolite paragraph in the essay").Error)

	hits, total, err := service.SearchSubscribedFeed(user, FeedQuery{Page: 1, PageSize: 20, Search: "zeolite paragraph"}, feedSearchRankRelevance)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, hits, 1)
	require.Equal(t, "post", hits[0].Type)
	require.Equal(t, post.ID, hits[0].Post.ID)
	require.Equal(t, "content", hits[0].SearchMatchField)
	require.Contains(t, hits[0].SearchSnippet, "zeolite paragraph")
}

func TestSearchSubscribedFeedHTTPRequiresQueryAndAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, db, user := newFeedTestService(t)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)

	unauthorized := httptest.NewRequest(http.MethodGet, "/api/v1/feed/search?q=feed", nil)
	unauthorizedResponse := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedResponse, unauthorized)
	require.Equal(t, http.StatusUnauthorized, unauthorizedResponse.Code)

	missing := httptest.NewRequest(http.MethodGet, "/api/v1/feed/search", nil)
	missing.Header.Set("Authorization", "Bearer "+signedFeedHTTPTokenForTest(t, db, user))
	missingResponse := httptest.NewRecorder()
	router.ServeHTTP(missingResponse, missing)
	require.Equal(t, http.StatusBadRequest, missingResponse.Code)

	var feedItem model.FeedItem
	require.NoError(t, db.Where("title = ?", "Feed item").First(&feedItem).Error)
	require.NoError(t, db.Model(&feedItem).Update("full_text_html", "<div>full text enzyme note</div>").Error)
	found := httptest.NewRequest(http.MethodGet, "/api/v1/feed/search?q=enzyme+note&rank=relevance", nil)
	found.Header.Set("Authorization", "Bearer "+signedFeedHTTPTokenForTest(t, db, user))
	foundResponse := httptest.NewRecorder()
	router.ServeHTTP(foundResponse, found)
	require.Equal(t, http.StatusOK, foundResponse.Code)
	var payload struct {
		Data []SubscribedSearchHit `json:"data"`
		Meta struct {
			Rank  string `json:"rank"`
			Total int64  `json:"total"`
		} `json:"meta"`
	}
	require.NoError(t, json.Unmarshal(foundResponse.Body.Bytes(), &payload))
	require.Equal(t, feedSearchRankRelevance, payload.Meta.Rank)
	require.Equal(t, int64(1), payload.Meta.Total)
	require.Equal(t, feedItem.ID, payload.Data[0].FeedItem.ID)
	require.Equal(t, "body", payload.Data[0].SearchMatchField)
	require.NotContains(t, payload.Data[0].SearchSnippet, "<div>")
}

func TestSearchSubscribedFeedExcludesOtherUsers(t *testing.T) {
	service, db, _ := newFeedTestService(t)
	other := model.User{Username: "search-other", Email: "search-other@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	require.NoError(t, db.Create(&other).Error)
	hits, total, err := service.SearchSubscribedFeed(authctx.CurrentUser{ID: other.UUID, Role: authctx.RoleUser}, FeedQuery{Page: 1, PageSize: 20, Search: "Feed item"}, feedSearchRankRelevance)
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, hits)
}
