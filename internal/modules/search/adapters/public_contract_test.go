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

// 公共投影类型共享发现、相关度、字面关键词、分页和软删除契约。
// 用户的标识符排序及隐私规则由 public_users_test.go 单独覆盖。
func TestPublicTypesDiscoverabilityContract(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{}, &model.ContentEntry{}, &model.ContentEpisodeExtension{}, &model.ContentVideoExtension{}, &model.FeedSource{}, &model.FeedItem{}, &model.Debate{}, &model.TimelinePerson{}, &model.TimelineEvent{})
	user := model.User{Username: "contract", Email: "contract@example.test", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	for index, title := range []string{"needle", "needle prefix", "contains needle", "unrelated", "literal_name", "literalXname", "percent%name", "percentXname", `slash\name`, "slashXname"} {
		created := time.Now().Add(time.Duration(index) * time.Hour)
		source := model.FeedSource{Title: title, SourceType: "external_rss", Hash: title}
		require.NoError(t, db.Create(&source).Error)
		// 来源没有简介搜索字段，用 URL 验证非标题匹配。
		require.NoError(t, db.Model(&source).Updates(map[string]any{"site_url": "https://needle.example.test", "updated_at": created}).Error)
		require.NoError(t, db.Create(&model.FeedItem{FeedSourceID: source.ID, GUID: title, Title: title, Summary: "needle synopsis", PublishedAt: created}).Error)
		channel := model.Channel{Name: title, Slug: uuid.NewString(), Description: "needle synopsis"}
		channel.CreatedAt = created
		require.NoError(t, db.Create(&channel).Error)
		for _, kind := range []string{"podcast", "video"} {
			entry := model.ContentEntry{AuthorID: &user.UUID, ChannelID: channel.ID, Kind: kind, Title: title, Summary: "needle synopsis", Status: "published", Visibility: "public"}
			entry.CreatedAt = created
			require.NoError(t, db.Create(&entry).Error)
			if kind == "podcast" {
				require.NoError(t, db.Create(&model.ContentEpisodeExtension{ContentID: entry.ID, EpisodeID: uuid.New(), LegacyPostID: entry.ID, Shownotes: "needle synopsis"}).Error)
			} else {
				require.NoError(t, db.Create(&model.ContentVideoExtension{ContentID: entry.ID, VideoID: uuid.New()}).Error)
			}
		}
		debate := model.Debate{UserID: user.UUID, Title: title, Description: "needle synopsis", Status: "active"}
		debate.CreatedAt = created
		require.NoError(t, db.Create(&debate).Error)
		person := model.TimelinePerson{UserID: user.UUID, Name: title, Bio: "needle synopsis", IsPublic: true}
		person.CreatedAt = created
		require.NoError(t, db.Create(&person).Error)
		require.NoError(t, db.Create(&model.TimelineEvent{UserID: user.UUID, Title: title, Description: "needle synopsis", EventDate: created, IsPublic: true}).Error)
	}
	types := []struct{ module, kind string }{
		{"feed", "source"}, {"feed", "article"}, {"podcast", "show"}, {"podcast", "episode"},
		{"video", "video"}, {"debate", "debate"}, {"timeline", "person"}, {"timeline", "event"},
	}
	for _, tc := range types {
		t.Run(tc.module+"/"+tc.kind, func(t *testing.T) {
			provider := NewPublicProvider(db, tc.module, tc.module)
			req := search.ProviderSearchRequest{Scope: search.ScopePublic, Type: tc.kind, Query: "NEEDLE", Sort: "relevance", Limit: 2, ExactTotal: true}
			result, err := provider.Search(context.Background(), search.Viewer{}, req)
			require.NoError(t, err)
			// 所有行都在简介/URL 命中，但精确、前缀名称仍必须排在最前。
			require.EqualValues(t, 10, *result.Total)
			require.Len(t, result.Items, 2)
			require.Equal(t, "needle", result.Items[0].Title)
			require.Equal(t, "needle prefix", result.Items[1].Title)
			req.Offset = 2
			page, err := provider.Search(context.Background(), search.Viewer{}, req)
			require.NoError(t, err)
			require.Equal(t, "contains needle", page.Items[0].Title)
			for _, item := range append(result.Items, page.Items...) {
				require.NoError(t, search.ValidateSearchHit(search.NormalizeSearchHit(item)))
			}
			for _, query := range []string{"literal_", "percent%", `slash\`} {
				req.Query, req.Offset = query, 0
				literal, err := provider.Search(context.Background(), search.Viewer{}, req)
				require.NoError(t, err)
				require.EqualValues(t, 1, *literal.Total, query)
				require.Len(t, literal.Items, 1, query)
			}
		})
	}
	for _, entity := range []any{&model.Channel{}, &model.ContentEntry{}, &model.FeedSource{}, &model.FeedItem{}, &model.Debate{}, &model.TimelinePerson{}, &model.TimelineEvent{}} {
		require.NoError(t, db.Where("1=1").Delete(entity).Error)
	}
	for _, tc := range types {
		result, err := NewPublicProvider(db, tc.module, tc.module).Search(context.Background(), search.Viewer{}, search.ProviderSearchRequest{Scope: search.ScopePublic, Type: tc.kind, Query: "needle", Limit: 20, ExactTotal: true})
		require.NoError(t, err)
		require.Zero(t, *result.Total, tc.module+"/"+tc.kind)
		require.Empty(t, result.Items)
	}
}
