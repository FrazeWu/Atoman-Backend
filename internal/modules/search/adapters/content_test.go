package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/blog"
	"atoman/internal/modules/forum"
	"atoman/internal/modules/search"
	"atoman/internal/modules/shortnote"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func contentSearchFixture(t *testing.T) (*gorm.DB, model.User, *search.Service, *BlogProvider, *ForumProvider) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{}, &model.ContentEntry{}, &model.ContentBlogExtension{},
		&model.ContentBlogTag{}, &model.ContentBlogUserTag{}, &model.ContentCollection{}, &model.ContentCollectionMembership{}, &model.ShortNote{},
		&model.ForumCategory{}, &model.ForumTopic{}, &model.ForumUserTrust{}, &model.ForumGroup{}, &model.ForumGroupMember{}, &model.ForumCategoryPermission{}, &model.DiscussionTarget{}, &model.CommentEntry{})
	user := model.User{UUID: uuid.New(), Username: "content-reader", Email: "content@example.test", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	bp := NewBlogProvider(blog.NewService(db), shortnote.NewService(db))
	fp := NewForumProvider(forum.NewRepo(db))
	registry := search.NewRegistry()
	require.NoError(t, registry.Register(bp))
	require.NoError(t, registry.Register(fp))
	return db, user, search.NewService(registry), bp, fp
}

func createSearchBlog(t *testing.T, db *gorm.DB, user model.User, channel model.Channel, title, body, status, visibility string) model.ContentEntry {
	t.Helper()
	entry := model.ContentEntry{AuthorID: &user.UUID, ChannelID: channel.ID, Kind: "blog", Title: title, Status: status, Visibility: visibility}
	require.NoError(t, db.Create(&entry).Error)
	require.NoError(t, db.Create(&model.ContentBlogExtension{ContentID: entry.ID, Content: body}).Error)
	return entry
}

func TestContentProvidersPublicAPIContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, user, service, bp, fp := contentSearchFixture(t)
	category := model.ForumCategory{Name: "General"}
	require.NoError(t, db.Create(&category).Error)
	for i := range 9 {
		channel := model.Channel{Name: fmt.Sprintf("公开频道%02d", i), Slug: fmt.Sprintf("public-%d", i), UserID: &user.UUID}
		require.NoError(t, db.Create(&channel).Error)
		post := createSearchBlog(t, db, user, channel, fmt.Sprintf("公开文章%02d", i), "全文内容", "published", "public")
		collection := model.ContentCollection{ChannelID: channel.ID, Name: fmt.Sprintf("公开合集%02d", i)}
		require.NoError(t, db.Create(&collection).Error)
		require.NoError(t, db.Create(&model.ContentCollectionMembership{ContentID: post.ID, CollectionID: collection.ID}).Error)
		require.NoError(t, db.Create(&model.ShortNote{UserID: user.UUID, Content: fmt.Sprintf("公开短笺%02d", i)}).Error)
		require.NoError(t, db.Create(&model.ForumTopic{UserID: user.UUID, CategoryID: category.ID, Title: fmt.Sprintf("公开主题%02d", i), Content: "正文"}).Error)
	}
	var counts atomic.Int64
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:count-search-totals", func(tx *gorm.DB) {
		if strings.HasPrefix(strings.ToLower(tx.Statement.SQL.String()), "select count(*)") {
			counts.Add(1)
		}
	}))
	router := gin.New()
	search.RegisterRoutes(router.Group("/api/v1"), service)
	for _, kind := range []struct{ module, key, href string }{
		{"blog", "post", "/posts/post/"}, {"blog", "short_note", "/posts/notes/"}, {"blog", "channel", "/channels/"}, {"blog", "collection", "/posts/collection/"}, {"forum", "topic", "/forum/topic/"},
	} {
		for _, view := range []string{"preview", "grouped", "list"} {
			t.Run(kind.key+"/"+view, func(t *testing.T) {
				before := counts.Load()
				path := "/api/v1/search?q=公开&module=" + kind.module + "&type=" + kind.key + "&view=" + view
				if view == "list" {
					path += "&page=2&page_size=2"
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
				require.Equal(t, 200, response.Code, response.Body.String())
				require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
				var envelope search.SearchEnvelope
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
				require.False(t, envelope.Data.Partial, response.Body.String())
				items := envelope.Data.Items
				if view == "list" {
					require.EqualValues(t, 9, envelope.Data.Pagination.Total)
					require.True(t, envelope.Data.Pagination.HasMore)
					require.Len(t, items, 2)
					require.Greater(t, counts.Load(), before)
				} else {
					require.Len(t, envelope.Data.Groups, 1)
					group := envelope.Data.Groups[0]
					require.Nil(t, group.Total)
					require.True(t, group.HasMore)
					limit := 6
					if view == "preview" {
						limit = 2
					}
					require.Len(t, group.Items, limit)
					items = group.Items
					require.Equal(t, before, counts.Load(), "预览不能计算精确总数")
				}
				for _, hit := range items {
					require.NoError(t, search.ValidateSearchHit(hit))
					require.True(t, strings.HasPrefix(hit.Href, kind.href), hit.Href)
				}
			})
		}
		// 总数、偏移和唯一排序必须一致，翻页不能重复或遗漏。
		all, err := service.Search(context.Background(), search.Viewer{}, search.SearchRequest{Query: "公开", Module: kind.module, Types: []string{kind.key}, View: search.ViewList, Sort: "recent"})
		require.NoError(t, err)
		var paged []string
		for page := 1; page <= 5; page++ {
			part, err := service.Search(context.Background(), search.Viewer{}, search.SearchRequest{Query: "公开", Module: kind.module, Types: []string{kind.key}, View: search.ViewList, Sort: "recent", Page: page, PageSize: 2})
			require.NoError(t, err)
			for _, item := range part.Items {
				paged = append(paged, item.ID)
			}
		}
		var expected []string
		for _, item := range all.Items {
			expected = append(expected, item.ID)
		}
		require.Equal(t, expected, paged)
		require.Len(t, paged, 9)
		seen := map[string]bool{}
		for _, id := range paged {
			require.False(t, seen[id])
			seen[id] = true
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, provider := range []search.SearchProvider{bp, fp} {
		for _, kind := range provider.Capabilities(context.Background(), search.Viewer{}, search.ScopePublic).Types {
			_, err := provider.Search(ctx, search.Viewer{}, search.ProviderSearchRequest{Scope: search.ScopePublic, Type: kind.Key, Query: "公开", Limit: 3})
			require.ErrorIs(t, err, context.Canceled)
			_, err = provider.Search(context.Background(), search.Viewer{}, search.ProviderSearchRequest{Scope: search.ScopeMine, Type: kind.Key})
			require.Error(t, err)
		}
		require.Empty(t, provider.Capabilities(context.Background(), search.Viewer{}, search.ScopeMine).Types)
	}
}

func TestBlogProviderPublicEligibilityAndFilters(t *testing.T) {
	db, user, service, _, _ := contentSearchFixture(t)
	channel := model.Channel{Name: "Orchid channel", Slug: "orchid", UserID: &user.UUID}
	empty := model.Channel{Name: "Secret empty", Slug: "empty", UserID: &user.UUID}
	private := model.Channel{Name: "Secret private", Slug: "private", UserID: &user.UUID}
	for _, c := range []*model.Channel{&channel, &empty, &private} {
		require.NoError(t, db.Create(c).Error)
	}
	title := createSearchBlog(t, db, user, channel, "Orchid", "plain", "published", "public")
	body := createSearchBlog(t, db, user, channel, "Body match", "连续中文全文 orchid 独特正文", "published", "")
	privatePost := createSearchBlog(t, db, user, private, "Secret private", "secret", "published", "private")
	createSearchBlog(t, db, user, private, "Secret followers", "secret", "published", "followers")
	createSearchBlog(t, db, user, private, "Secret draft", "secret", "draft", "public")
	deleted := createSearchBlog(t, db, user, private, "Secret deleted", "secret", "published", "public")
	require.NoError(t, db.Delete(&deleted).Error)
	collection := model.ContentCollection{ChannelID: channel.ID, Name: "Orchid collection"}
	second := model.ContentCollection{ChannelID: channel.ID, Name: "Other collection"}
	secret := model.ContentCollection{ChannelID: empty.ID, Name: "Secret collection"}
	privateCollection := model.ContentCollection{ChannelID: private.ID, Name: "Secret private collection"}
	for _, c := range []*model.ContentCollection{&collection, &second, &secret, &privateCollection} {
		require.NoError(t, db.Create(c).Error)
	}
	require.NoError(t, db.Create(&model.ContentCollectionMembership{ContentID: privatePost.ID, CollectionID: privateCollection.ID}).Error)
	// 同一文章属于多个合集，也不能产生重复结果；筛选必须命中任一成员关系。
	for _, id := range []uuid.UUID{collection.ID, second.ID} {
		require.NoError(t, db.Create(&model.ContentCollectionMembership{ContentID: title.ID, CollectionID: id}).Error)
	}
	require.NoError(t, db.Create(&model.ContentBlogTag{ContentID: title.ID, Name: "orchid-tag"}).Error)
	note := model.ShortNote{UserID: user.UUID, Content: "Secret deleted note"}
	require.NoError(t, db.Create(&note).Error)
	require.NoError(t, db.Delete(&note).Error)
	for _, viewer := range []search.Viewer{{}, {ID: user.UUID}, {ID: user.UUID, Role: authctx.RoleAdmin}, {ID: user.UUID, Role: authctx.RoleOwner}} {
		for _, kind := range []string{"post", "short_note", "channel", "collection"} {
			result, err := service.Search(context.Background(), viewer, search.SearchRequest{Query: "Secret", Module: "blog", Types: []string{kind}, View: search.ViewList})
			require.NoError(t, err)
			require.Zero(t, result.Pagination.Total)
		}
	}
	result, err := service.Search(context.Background(), search.Viewer{}, search.SearchRequest{Query: "orchid", Module: "blog", Types: []string{"post"}, View: search.ViewList})
	require.NoError(t, err)
	require.EqualValues(t, 2, result.Pagination.Total)
	require.Equal(t, title.ID.String(), result.Items[0].ID)
	for _, filters := range []map[string][]string{
		{"author_id": {uuid.NewString(), user.UUID.String()}, "channel_id": {channel.ID.String()}, "collection_id": {second.ID.String()}, "tag": {"missing", "Orchid-Tag"}},
		{"collection_id": {collection.ID.String(), second.ID.String()}},
	} {
		result, err = service.Search(context.Background(), search.Viewer{}, search.SearchRequest{Query: "orchid", Module: "blog", Types: []string{"post"}, View: search.ViewList, Filters: filters})
		require.NoError(t, err)
		require.EqualValues(t, 1, result.Pagination.Total)
		require.Equal(t, title.ID.String(), result.Items[0].ID)
	}
	result, err = service.Search(context.Background(), search.Viewer{}, search.SearchRequest{Query: "独特正文", Module: "blog", Types: []string{"post"}, View: search.ViewList})
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	require.Equal(t, body.ID.String(), result.Items[0].ID)
	require.Contains(t, result.Items[0].MatchedFields, "content")
	for _, kind := range []string{"channel", "collection"} {
		result, err = service.Search(context.Background(), search.Viewer{}, search.SearchRequest{Query: "orchid", Module: "blog", Types: []string{kind}, View: search.ViewList})
		require.NoError(t, err)
		require.EqualValues(t, 1, result.Pagination.Total)
	}
	// recent 与 relevance 独立，正文命中的新文章可排在标题命中的旧文章之前。
	require.NoError(t, db.Model(&body).Update("published_at", time.Now().Add(time.Hour)).Error)
	result, err = service.Search(context.Background(), search.Viewer{}, search.SearchRequest{Query: "orchid", Module: "blog", Types: []string{"post"}, View: search.ViewList, Sort: "recent"})
	require.NoError(t, err)
	require.Equal(t, body.ID.String(), result.Items[0].ID)
}

func TestForumProviderRepliesPermissionsAndLegacyVisibility(t *testing.T) {
	db, user, service, _, _ := contentSearchFixture(t)
	public := model.ForumCategory{Name: "Public"}
	private := model.ForumCategory{Name: "Private"}
	deleted := model.ForumCategory{Name: "Deleted"}
	for _, c := range []*model.ForumCategory{&public, &private, &deleted} {
		require.NoError(t, db.Create(c).Error)
	}
	group := model.ForumGroup{Name: "Members"}
	require.NoError(t, db.Create(&group).Error)
	require.NoError(t, db.Create(&model.ForumCategoryPermission{CategoryID: private.ID, GroupID: group.ID, CanView: true}).Error)
	require.NoError(t, db.Create(&model.ForumGroupMember{GroupID: group.ID, UserID: user.UUID}).Error)
	var topics []model.ForumTopic
	for i, cat := range []uuid.UUID{public.ID, private.ID, deleted.ID, public.ID} {
		topic := model.ForumTopic{UserID: user.UUID, CategoryID: cat, Title: fmt.Sprintf("Parent %d", i), Content: "plain", Tags: model.StringSlice{"topic-tag"}}
		require.NoError(t, db.Create(&topic).Error)
		topics = append(topics, topic)
		target := model.DiscussionTarget{Kind: "forum_topic", ResourceID: topic.ID, ResourceKey: topic.ID.String()}
		require.NoError(t, db.Create(&target).Error)
		for j := range 2 {
			status := "active"
			if j == 1 {
				status = "auto_folded"
			}
			if i == 3 {
				status = "hidden"
			}
			reply := model.CommentEntry{TargetID: target.ID, AuthorID: user.UUID, Content: "orchid 连续中文回复", ContentHash: uuid.NewString(), Status: status}
			require.NoError(t, db.Create(&reply).Error)
		}
	}
	require.NoError(t, db.Delete(&deleted).Error)
	for _, viewer := range []search.Viewer{{}, {ID: user.UUID}, {ID: user.UUID, Role: authctx.RoleAdmin}, {ID: user.UUID, Role: authctx.RoleOwner}} {
		result, err := service.Search(context.Background(), viewer, search.SearchRequest{Query: "连续中文回复", Module: "forum", Types: []string{"topic"}, View: search.ViewList})
		require.NoError(t, err)
		require.EqualValues(t, 1, result.Pagination.Total)
		require.Equal(t, topics[0].ID.String(), result.Items[0].ID)
	}
	result, err := service.Search(context.Background(), search.Viewer{}, search.SearchRequest{Query: "orchid", Module: "forum", Types: []string{"topic"}, View: search.ViewList,
		Filters: map[string][]string{"category_id": {public.ID.String(), private.ID.String()}, "tag": {"missing", "topic-tag"}}})
	require.NoError(t, err)
	require.EqualValues(t, 1, result.Pagination.Total)
	result, err = service.Search(context.Background(), search.Viewer{}, search.SearchRequest{Query: "orchid", Module: "forum", Types: []string{"topic"}, View: search.ViewList, Filters: map[string][]string{"tag": {"topic"}}})
	require.NoError(t, err)
	require.Zero(t, result.Pagination.Total)
	legacy, _, err := forum.NewRepo(db).SearchTopics(authctx.CurrentUser{ID: user.UUID}, forum.ListTopicsQuery{Search: "orchid"})
	require.NoError(t, err)
	ids := make([]uuid.UUID, 0, len(legacy))
	for _, item := range legacy {
		ids = append(ids, item.ID)
	}
	require.Contains(t, ids, topics[1].ID, "旧接口继续允许成员检索受限板块")
	// 标签特殊字符作为字面值匹配，不变成通配符。
	tag := `100%_quote'`
	require.NoError(t, db.Model(&topics[0]).Update("tags", model.StringSlice{tag}).Error)
	router := gin.New()
	search.RegisterRoutes(router.Group("/api/v1"), service)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/search?module=forum&type=topic&view=list&q=orchid&filter[tag]="+url.QueryEscape(tag), nil))
	require.Equal(t, 200, response.Code, response.Body.String())
	var envelope search.SearchEnvelope
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
	require.EqualValues(t, 1, envelope.Data.Pagination.Total)
	// 相关度不能被 recent 覆盖；recent 不能继续沿用相关度排序。
	titleMatch := model.ForumTopic{UserID: user.UUID, CategoryID: public.ID, Title: "orchid", Content: "plain"}
	require.NoError(t, db.Create(&titleMatch).Error)
	require.NoError(t, db.Model(&titleMatch).Update("created_at", time.Now().Add(-time.Hour)).Error)
	for _, sort := range []string{"relevance", "recent"} {
		result, err = service.Search(context.Background(), search.Viewer{}, search.SearchRequest{Query: "orchid", Module: "forum", Types: []string{"topic"}, View: search.ViewList, Sort: sort})
		require.NoError(t, err)
		require.EqualValues(t, 2, result.Pagination.Total)
		expected := titleMatch.ID
		if sort == "recent" {
			expected = topics[0].ID
		}
		require.Equal(t, expected.String(), result.Items[0].ID)
	}
}
