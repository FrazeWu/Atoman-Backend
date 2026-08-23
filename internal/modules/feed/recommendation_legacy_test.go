package feed

import (
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
	if err := db.Create(&model.ContentBlogExtension{ContentID: entry.ID, Content: "Article content"}).Error; err != nil {
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

func TestRecommendArticlesFallsBackWithoutCanonicalBlogExtensions(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.Post{},
		&model.FeedSource{},
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
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected one legacy recommendation, got total=%d items=%d", total, len(items))
	}
	if items[0].ID != post.ID.String() || items[0].Title != post.Title {
		t.Fatalf("unexpected recommendation: %+v", items[0])
	}
}
