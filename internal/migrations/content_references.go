package migrations

import (
	"atoman/internal/model"
	"atoman/internal/modules/reference"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func RunContentReferencesMigration(db *gorm.DB) error {
	if err := db.AutoMigrate(&model.ContentReference{}); err != nil {
		return err
	}
	for _, statement := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_content_reference_source_range ON content_references (source_type, source_id, source_field, start_offset, end_offset) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_content_reference_target ON content_references (target_type, target_id) WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_content_reference_source ON content_references (source_type, source_id) WHERE deleted_at IS NULL`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	if db.Migrator().HasTable(&model.CommentMention{}) {
		var mentions []model.CommentMention
		if err := db.Find(&mentions).Error; err != nil {
			return err
		}
		for _, mention := range mentions {
			row := model.ContentReference{
				SourceType: "comment", SourceID: mention.CommentID, SourceField: "content",
				TargetType: "user", TargetID: mention.UserID,
				StartOffset: mention.StartOffset, EndOffset: mention.EndOffset,
			}
			if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
		}
	}
	return backfillPublishedContentReferences(db)
}

func backfillPublishedContentReferences(db *gorm.DB) error {
	service := reference.NewService(db)
	return db.Transaction(func(tx *gorm.DB) error {
		if tx.Migrator().HasTable(&model.Post{}) {
			var posts []model.Post
			if err := tx.Where("status = ?", "published").Find(&posts).Error; err != nil {
				return err
			}
			for _, post := range posts {
				audience := ""
				if post.Visibility == "" || post.Visibility == "public" {
					audience = reference.AudiencePublic
				}
				if err := service.BackfillAvailable(tx, reference.Source{Type: "post", ID: post.ID, ActorID: post.UserID, Audience: audience}, []reference.Field{{Name: "content", Content: post.Content}}); err != nil {
					return err
				}
			}
		}
		if tx.Migrator().HasTable(&model.ForumTopic{}) {
			var topics []model.ForumTopic
			if err := tx.Find(&topics).Error; err != nil {
				return err
			}
			for _, topic := range topics {
				audience := reference.AudiencePublic
				if tx.Migrator().HasTable(&model.ForumCategoryPermission{}) {
					var permissionCount int64
					if err := tx.Model(&model.ForumCategoryPermission{}).Where("category_id = ?", topic.CategoryID).Count(&permissionCount).Error; err != nil {
						return err
					}
					if permissionCount > 0 {
						audience = ""
					}
				}
				if err := service.BackfillAvailable(tx, reference.Source{Type: "thread", ID: topic.ID, ActorID: topic.UserID, Audience: audience}, []reference.Field{{Name: "content", Content: topic.Content}}); err != nil {
					return err
				}
			}
		}
		if tx.Migrator().HasTable(&model.Debate{}) {
			var debates []model.Debate
			if err := tx.Find(&debates).Error; err != nil {
				return err
			}
			for _, debate := range debates {
				if err := service.BackfillAvailable(tx, reference.Source{Type: "debate", ID: debate.ID, ActorID: debate.UserID, Audience: reference.AudiencePublic}, []reference.Field{
					{Name: "description", Content: debate.Description},
					{Name: "content", Content: debate.Content},
				}); err != nil {
					return err
				}
			}
		}
		if tx.Migrator().HasTable(&model.CommentEntry{}) {
			var comments []model.CommentEntry
			if err := tx.Where("status IN ?", []string{"active", "auto_folded"}).Find(&comments).Error; err != nil {
				return err
			}
			for _, comment := range comments {
				if err := service.BackfillAvailable(tx, reference.Source{Type: "comment", ID: comment.ID, ActorID: comment.AuthorID}, []reference.Field{{Name: "content", Content: comment.Content}}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
