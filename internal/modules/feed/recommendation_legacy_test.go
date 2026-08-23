package feed

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/recommendation"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

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
		t.Fatalf("recommendation should use legacy posts without canonical extensions: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected one legacy recommendation, got total=%d items=%d", total, len(items))
	}
	if items[0].ID != post.ID.String() || items[0].Title != post.Title {
		t.Fatalf("unexpected recommendation: %+v", items[0])
	}
}
