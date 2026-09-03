package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

// RunBlogArchiveRemovalMigration converts legacy archived blog content to drafts.
// Podcast content keeps its existing status because the legacy posts table is shared.
func RunBlogArchiveRemovalMigration(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if tx.Migrator().HasTable(&model.Post{}) && tx.Migrator().HasTable(&model.PodcastEpisode{}) {
			if err := tx.Model(&model.Post{}).
				Where("status = ? AND NOT EXISTS (SELECT 1 FROM podcast_episodes WHERE podcast_episodes.post_id = posts.id AND podcast_episodes.deleted_at IS NULL)", "archived").
				Update("status", "draft").Error; err != nil {
				return err
			}
		} else if tx.Migrator().HasTable(&model.Post{}) {
			if err := tx.Model(&model.Post{}).Where("status = ?", "archived").Update("status", "draft").Error; err != nil {
				return err
			}
		}
		if tx.Migrator().HasTable(&model.ContentEntry{}) {
			if err := tx.Model(&model.ContentEntry{}).
				Where("kind = ? AND status = ?", "blog", "archived").
				Update("status", "draft").Error; err != nil {
				return err
			}
		}
		return nil
	})
}
