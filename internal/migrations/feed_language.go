package migrations

import (
	"strings"

	"atoman/internal/feedlanguage"
	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// RunFeedLanguageMigration adds language indexes and backfills content created before
// language detection was introduced. Unknown or ambiguous content remains unset.
func RunFeedLanguageMigration(db *gorm.DB) error {
	if db.Migrator().HasTable(&model.FeedItem{}) {
		if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_feed_items_language_published
			ON feed_items (language_code, published_at DESC, id DESC) WHERE deleted_at IS NULL`).Error; err != nil {
			return err
		}
		if err := backfillFeedItemLanguages(db); err != nil {
			return err
		}
	}
	if db.Migrator().HasTable(&model.Post{}) {
		if err := backfillPostLanguages(db); err != nil {
			return err
		}
	}
	return nil
}

type feedItemLanguageRow struct {
	ID            uuid.UUID
	Title         string
	Summary       string
	FeedContentHTML string
	ReaderHTML    string
	FullTextHTML  string
}

func backfillFeedItemLanguages(db *gorm.DB) error {
	return db.Model(&model.FeedItem{}).
		Select("id, title, summary, feed_content_html, reader_html, full_text_html").
		Where("language_code = '' OR language_code IS NULL").
		FindInBatches(&[]feedItemLanguageRow{}, 500, func(tx *gorm.DB, _ int) error {
			rows := tx.Statement.Dest.(*[]feedItemLanguageRow)
			for _, row := range *rows {
				code := feedlanguage.Detect(strings.Join([]string{row.Title, row.Summary, row.FeedContentHTML, row.ReaderHTML, row.FullTextHTML}, " "))
				if code == "" {
					continue
				}
				if err := db.Model(&model.FeedItem{}).Where("id = ?", row.ID).Update("language_code", code).Error; err != nil {
					return err
				}
			}
			return nil
		}).Error
}

type postLanguageRow struct {
	ID      uuid.UUID
	Title   string
	Summary string
	Content string
}

func backfillPostLanguages(db *gorm.DB) error {
	return db.Model(&model.Post{}).
		Select("id, title, summary, content").
		Where("language_code = '' OR language_code IS NULL").
		FindInBatches(&[]postLanguageRow{}, 500, func(tx *gorm.DB, _ int) error {
			rows := tx.Statement.Dest.(*[]postLanguageRow)
			for _, row := range *rows {
				code := feedlanguage.Detect(strings.Join([]string{row.Title, row.Summary, row.Content}, " "))
				if code == "" {
					continue
				}
				if err := db.Model(&model.Post{}).Where("id = ?", row.ID).Update("language_code", code).Error; err != nil {
					return err
				}
			}
			return nil
		}).Error
}
