package feed

import (
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/recommendation"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestRecommendArticlesUsesCanonicalBlogExtensions(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.ContentEntry{},
		&model.ContentBlogExtension{},
		&model.ContentBlogTag{},
		&model.ContentCollectionMembership{},
		&model.FeedSource{},
		&model.FeedItem{},
		&model.FeedItemRead{},
		&model.FeedItemStar{},
	)

	user := model.User{
		Username: "canonical-recommendation-user-" + uuid.NewString()[:8],
		Email:    "canonical-recommendation-" + uuid.NewString()[:8] + "@example.com",
		Password: "hash",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{UserID: &user.UUID, Name: "Canonical Channel", Slug: "canonical-channel-" + uuid.NewString()[:8]}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	createdAt := time.Now().Add(-time.Hour)
	entry := model.ContentEntry{
		Base:       model.Base{CreatedAt: createdAt},
		AuthorID:   &user.UUID,
		ChannelID:  channel.ID,
		Kind:       "blog",
		Title:      "Canonical recommendation article",
		Summary:    "Article summary",
		Status:     "published",
		Visibility: "public",
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ContentBlogExtension{ContentID: entry.ID, Content: strings.Repeat("Article content ", 30)}).Error; err != nil {
		t.Fatal(err)
	}

	items, total, err := NewService(db).RecommendArticlesByMode(recommendation.ModeHot, "blog", "", "", "", 1, 20)
	if err != nil {
		t.Fatalf("canonical recommendation failed: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected one canonical recommendation, got total=%d items=%d", total, len(items))
	}
	if items[0].ID != entry.ID.String() || items[0].Title != entry.Title {
		t.Fatalf("unexpected recommendation: %+v", items[0])
	}
}

func TestRecommendArticlesRanksReadableFeedItemsAheadOfLowQualityCandidates(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.ContentEntry{},
		&model.ContentBlogExtension{},
		&model.ContentBlogTag{},
		&model.FeedSource{},
		&model.FeedItem{},
		&model.FeedItemRead{},
		&model.FeedItemStar{},
	)

	now := time.Now().UTC()
	highQualitySource := model.FeedSource{SourceType: "external_rss", Hash: "high-quality-source-" + uuid.NewString(), Title: "High Quality Source", Category: "blog"}
	lowQualitySource := model.FeedSource{SourceType: "external_rss", Hash: "low-quality-source-" + uuid.NewString(), Title: "Low Quality Source", Category: "blog"}
	for _, source := range []*model.FeedSource{&highQualitySource, &lowQualitySource} {
		if err := db.Create(source).Error; err != nil {
			t.Fatalf("create feed source: %v", err)
		}
	}
	highQualityItem := model.FeedItem{
		FeedSourceID: highQualitySource.ID, GUID: "high-quality-item", Title: "A detailed technical analysis", Summary: strings.Repeat("A substantial article summary. ", 20),
		ReaderQualityScore: 95, FullTextWordCount: 1200, PublishedAt: now.Add(-time.Hour), FetchedAt: now,
	}
	lowQualityItem := model.FeedItem{
		FeedSourceID: lowQualitySource.ID, GUID: "low-quality-item", Title: "A short update", Summary: "brief",
		ReaderQualityScore: 10, PublishedAt: now, FetchedAt: now,
	}
	for _, item := range []*model.FeedItem{&highQualityItem, &lowQualityItem} {
		if err := db.Create(item).Error; err != nil {
			t.Fatalf("create feed item: %v", err)
		}
	}

	items, _, err := NewService(db).RecommendArticlesByMode(recommendation.ModeHot, "blog", "", "", "", 1, 20)
	if err != nil {
		t.Fatalf("recommend articles: %v", err)
	}

	highQualityIndex := -1
	for index, item := range items {
		switch item.ID {
		case highQualityItem.ID.String():
			highQualityIndex = index
		case lowQualityItem.ID.String():
			t.Fatalf("low-quality feed item must not be recommended: %+v", item)
		}
	}
	if highQualityIndex < 0 {
		t.Fatalf("expected readable item to be recommended, got items=%+v", items)
	}
}

func TestRecommendArticlesDeduplicatesFeedItemsByCanonicalLink(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.ContentEntry{},
		&model.ContentBlogExtension{},
		&model.ContentBlogTag{},
		&model.FeedSource{},
		&model.FeedItem{},
		&model.FeedItemRead{},
		&model.FeedItemStar{},
	)

	now := time.Now().UTC()
	preferredSource := model.FeedSource{SourceType: "external_rss", Hash: "preferred-source-" + uuid.NewString(), Title: "Primary feed", Category: "blog"}
	duplicateSource := model.FeedSource{SourceType: "external_rss", Hash: "duplicate-source-" + uuid.NewString(), Title: "Mirror feed", Category: "blog"}
	if err := db.Create(&preferredSource).Error; err != nil {
		t.Fatalf("create preferred source: %v", err)
	}
	if err := db.Create(&duplicateSource).Error; err != nil {
		t.Fatalf("create duplicate source: %v", err)
	}

	preferredItem := model.FeedItem{
		FeedSourceID:       preferredSource.ID,
		GUID:               "primary-guid",
		Title:              "Shared article",
		Summary:            strings.Repeat("A substantial article summary. ", 20),
		Link:               "https://Example.COM/articles/shared?b=2&a=1#comments",
		ReaderQualityScore: 95,
		FullTextWordCount:  1200,
		PublishedAt:        now,
		FetchedAt:          now,
	}
	duplicateItem := model.FeedItem{
		FeedSourceID:       duplicateSource.ID,
		GUID:               "mirror-guid",
		Title:              "Shared article",
		Summary:            strings.Repeat("A substantial article summary. ", 20),
		Link:               "https://example.com/articles/shared?a=1&b=2",
		ReaderQualityScore: 80,
		FullTextWordCount:  1000,
		PublishedAt:        now.Add(-time.Minute),
		FetchedAt:          now,
	}
	if err := db.Create(&preferredItem).Error; err != nil {
		t.Fatalf("create preferred item: %v", err)
	}
	if err := db.Create(&duplicateItem).Error; err != nil {
		t.Fatalf("create duplicate item: %v", err)
	}

	items, total, err := NewService(db).RecommendArticlesByMode(recommendation.ModeHot, "blog", "", "", "", 1, 20)
	if err != nil {
		t.Fatalf("recommend articles: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected one recommendation after cross-source deduplication, got total=%d items=%+v", total, items)
	}
	if items[0].ID != preferredItem.ID.String() {
		t.Fatalf("expected the highest-ranked duplicate to be retained, got %+v", items[0])
	}
}

func TestRecommendArticlesReturnsEmptyWithoutCanonicalBlogExtensions(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.Post{},
		&model.ContentEntry{},
		&model.ContentBlogExtension{},
		&model.ContentBlogTag{},
		&model.FeedItem{},
		&model.FeedItemRead{},
		&model.FeedItemStar{},
	)

	user := model.User{
		Username: "legacy-recommendation-user-" + uuid.NewString()[:8],
		Email:    "legacy-recommendation-" + uuid.NewString()[:8] + "@example.com",
		Password: "hash",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{UserID: &user.UUID, Name: "Legacy Channel", Slug: "legacy-channel-" + uuid.NewString()[:8]}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	post := model.Post{
		Base:      model.Base{CreatedAt: time.Now().Add(-time.Hour)},
		UserID:    user.UUID,
		ChannelID: &channel.ID,
		Title:     "Legacy recommendation article",
		Summary:   "Article summary",
		Content:   "Article content",
		Status:    "published",
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatal(err)
	}

	items, total, err := NewService(db).RecommendArticlesByMode(recommendation.ModeHot, "blog", "", "", "", 1, 20)
	if err != nil {
		t.Fatalf("legacy recommendation should work without canonical extensions: %v", err)
	}
	if total != 0 || len(items) != 0 {
		t.Fatalf("expected canonical-only recommendation query to exclude legacy post, got total=%d items=%d", total, len(items))
	}
}

func TestRecommendChannelsReturnsEmptyWithoutCanonicalBlogExtensions(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.Post{},
		&model.ContentEntry{},
		&model.ContentBlogExtension{},
		&model.ContentBlogTag{},
	)

	user := model.User{
		Username: "legacy-channel-recommendation-user-" + uuid.NewString()[:8],
		Email:    "legacy-channel-recommendation-" + uuid.NewString()[:8] + "@example.com",
		Password: "hash",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{
		UserID:      &user.UUID,
		Name:        "Legacy Recommendation Channel",
		Slug:        "legacy-recommendation-channel-" + uuid.NewString()[:8],
		Description: "Channel description",
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	post := model.Post{
		Base:       model.Base{CreatedAt: time.Now().Add(-time.Hour)},
		UserID:     user.UUID,
		ChannelID:  &channel.ID,
		Title:      "Recent legacy article",
		Content:    "Article content",
		Status:     "published",
		Visibility: "public",
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatal(err)
	}

	rows, err := NewRepo(db).ListRecommendationChannels("")
	if err != nil {
		t.Fatalf("legacy channel recommendation should work without canonical extensions: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected canonical-only channel recommendation query to exclude legacy channel, got %+v", rows)
	}
	recentPosts, err := NewRepo(db).ListRecentPublishedPostsByChannelID(channel.ID, 3)
	if err != nil {
		t.Fatalf("legacy channel recent posts should load without canonical extensions: %v", err)
	}
	if len(recentPosts) != 0 {
		t.Fatalf("expected canonical-only recent posts query to exclude legacy post, got %+v", recentPosts)
	}
}
