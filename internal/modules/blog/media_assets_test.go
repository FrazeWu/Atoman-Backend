package blog

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestSyncBlogContentMediaAssetsOnlyLinksCurrentAuthorsReferencedAssets(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.MediaAsset{}, &model.ContentMediaAsset{})
	service := NewService(db)
	ownerID, otherID, contentID := uuid.New(), uuid.New(), uuid.New()
	ownerAsset := model.MediaAsset{UserID: &ownerID, Purpose: "blog.image", URL: "https://assets.example.test/owner.png", Key: "blog/owner.png", ContentType: "image/png", Size: 10}
	otherAsset := model.MediaAsset{UserID: &otherID, Purpose: "blog.image", URL: "https://assets.example.test/other.png", Key: "blog/other.png", ContentType: "image/png", Size: 10}
	if err := db.Create(&ownerAsset).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&otherAsset).Error; err != nil {
		t.Fatal(err)
	}
	markdown := "![owner](https://assets.example.test/owner.png)\n![other](https://assets.example.test/other.png)"
	if err := service.syncBlogContentMediaAssets(db, contentID, ownerID, "", markdown); err != nil {
		t.Fatal(err)
	}
	var links []model.ContentMediaAsset
	if err := db.Where("content_id = ?", contentID).Find(&links).Error; err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].MediaAssetID != ownerAsset.ID {
		t.Fatalf("expected only owner asset to be linked, got %#v", links)
	}
}
