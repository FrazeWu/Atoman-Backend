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
