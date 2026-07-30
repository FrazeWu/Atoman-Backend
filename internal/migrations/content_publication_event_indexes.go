package migrations

import "gorm.io/gorm"

func RunContentPublicationEventIndexes(db *gorm.DB) error {
	if !db.Migrator().HasTable("content_publication_events") {
		return nil
	}
	return db.Exec(`CREATE INDEX IF NOT EXISTS idx_content_publication_events_dispatch_candidates
		ON content_publication_events (created_at, id)
		WHERE deleted_at IS NULL AND status IN ('pending', 'processing')`).Error
}
