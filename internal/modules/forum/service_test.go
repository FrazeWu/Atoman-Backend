package forum

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"gorm.io/gorm"
)

func createForumTestTopic(t *testing.T, db *gorm.DB, user authctx.CurrentUser) model.ForumTopic {
	t.Helper()
	category := model.ForumCategory{Name: "General", Description: "general", Color: "#111111"}
	if err := db.Create(&category).Error; err != nil {
		t.Fatalf("create category: %v", err)
	}
	topic := model.ForumTopic{UserID: user.ID, CategoryID: category.ID, Title: "Solved topic", Content: "topic content"}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatalf("create topic: %v", err)
	}
	return topic
}
