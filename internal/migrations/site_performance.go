package migrations

import (
	"atoman/internal/model"
	"fmt"
	"gorm.io/gorm"
)

// 并发建索引，不在迁移期间阻塞线上内容写入。
func RunSitePerformanceIndexes(db *gorm.DB) error {
	if err := db.AutoMigrate(&model.MusicMetadataMatchJob{}); err != nil {
		return err
	}
	if db.Dialector.Name() != "postgres" {
		return nil
	}
	if err := db.Exec("CREATE EXTENSION IF NOT EXISTS pg_trgm").Error; err != nil {
		return err
	}
	var schema string
	if err := db.Raw(`SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = 'pg_trgm'`).Scan(&schema).Error; err != nil {
		return err
	}
	op := quotePostgresIdentifier(schema) + ".gin_trgm_ops"
	statements := []string{
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_book_works_title_trgm ON book_works USING GIN (title %s) WHERE deleted_at IS NULL`, op),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_book_works_original_title_trgm ON book_works USING GIN (original_title %s) WHERE deleted_at IS NULL`, op),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_book_works_subtitle_trgm ON book_works USING GIN (subtitle %s) WHERE deleted_at IS NULL`, op),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_book_people_name_trgm ON book_people USING GIN (name %s) WHERE deleted_at IS NULL`, op),
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feed_diagnostics_created ON feed_source_diagnostics (created_at)`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feed_diagnostics_source_time ON feed_source_diagnostics (feed_source_id, created_at DESC)`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_subscriptions_source_live ON subscriptions (feed_source_id) WHERE deleted_at IS NULL`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feed_items_fulltext_source_ready ON feed_items (feed_source_id, created_at, published_at, id) WHERE deleted_at IS NULL AND full_text_status IN ('pending', 'retry') AND COALESCE(enclosure_url, '') = '' AND COALESCE(enclosure_type, '') NOT LIKE 'audio/%' AND COALESCE(enclosure_type, '') NOT LIKE 'video/%' AND COALESCE(duration, '') = ''`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("site performance index: %w", err)
		}
	}
	return nil
}
