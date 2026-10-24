package blog

import (
	"context"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"
	"github.com/stretchr/testify/require"
)

func TestPublicContainerSearchRanksExactBeforePrefixAndDescription(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Channel{}, &model.ContentCollection{}, &model.ContentCollectionMembership{}, &model.ContentEntry{}, &model.ContentBlogExtension{})
	s := &Service{db: db}
	// 更弱的匹配更新，确保不是碰巧按创建时间通过。
	for index, name := range []string{"needle", "needle prefix", "other"} {
		channel := model.Channel{Name: name, Slug: name, Description: "needle"}
		channel.CreatedAt = time.Now().Add(time.Duration(index) * time.Hour)
		require.NoError(t, db.Create(&channel).Error)
		collection := model.ContentCollection{ChannelID: channel.ID, Name: name, Description: "needle"}
		collection.CreatedAt = channel.CreatedAt
		require.NoError(t, db.Create(&collection).Error)
		entry := model.ContentEntry{ChannelID: channel.ID, Kind: "blog", Title: name, Status: "published", Visibility: "public"}
		require.NoError(t, db.Create(&entry).Error)
		require.NoError(t, db.Create(&model.ContentBlogExtension{ContentID: entry.ID}).Error)
		require.NoError(t, db.Create(&model.ContentCollectionMembership{ContentID: entry.ID, CollectionID: collection.ID}).Error)
	}
	opts := ContainerSearchOptions{Query: "NEEDLE", Sort: "relevance", Limit: 1, ExactTotal: true}
	channels, total, err := s.SearchPublicChannels(context.Background(), opts)
	require.NoError(t, err)
	require.EqualValues(t, 3, total)
	require.Len(t, channels, 1)
	require.Equal(t, "needle", channels[0].Name)
	collections, total, err := s.SearchPublicCollections(context.Background(), opts)
	require.NoError(t, err)
	require.EqualValues(t, 3, total)
	require.Len(t, collections, 1)
	require.Equal(t, "needle", collections[0].Name)
	opts.Offset = 1
	channels, _, err = s.SearchPublicChannels(context.Background(), opts)
	require.NoError(t, err)
	require.Len(t, channels, 1)
	require.Equal(t, "needle prefix", channels[0].Name)
	collections, _, err = s.SearchPublicCollections(context.Background(), opts)
	require.NoError(t, err)
	require.Len(t, collections, 1)
	require.Equal(t, "needle prefix", collections[0].Name)
}
