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
	ID              uuid.UUID
	Title           string
	Summary         string
	FeedContentHTML string
	ReaderHTML      string
	FullTextHTML    string
}

func backfillFeedItemLanguages(db *gorm.DB) error {
	rows := make([]feedItemLanguageRow, 0, 500)
	return db.Table("feed_items").
		Select("id, title, summary, feed_content_html, reader_html, full_text_html").
		Where("(language_code = '' OR language_code IS NULL) AND deleted_at IS NULL").
		FindInBatches(&rows, 500, func(_ *gorm.DB, _ int) error {
			updates := make(map[uuid.UUID]string, len(rows))
			for _, row := range rows {
				code := feedlanguage.Detect(strings.Join([]string{row.Title, row.Summary, row.FeedContentHTML, row.ReaderHTML, row.FullTextHTML}, " "))
				if code != "" {
					updates[row.ID] = code
				}
			}
			return updateLanguageCodes(db, &model.FeedItem{}, updates)
		}).Error
}

type postLanguageRow struct {
	ID      uuid.UUID
	Title   string
	Summary string
	Content string
}

func backfillPostLanguages(db *gorm.DB) error {
	rows := make([]postLanguageRow, 0, 500)
	return db.Table("posts").
		Select("id, title, summary, content").
		Where("(language_code = '' OR language_code IS NULL) AND deleted_at IS NULL").
		FindInBatches(&rows, 500, func(_ *gorm.DB, _ int) error {
			updates := make(map[uuid.UUID]string, len(rows))
			for _, row := range rows {
				code := feedlanguage.Detect(strings.Join([]string{row.Title, row.Summary, row.Content}, " "))
				if code != "" {
					updates[row.ID] = code
				}
			}
			return updateLanguageCodes(db, &model.Post{}, updates)
		}).Error
}

func updateLanguageCodes(db *gorm.DB, destination any, updates map[uuid.UUID]string) error {
	if len(updates) == 0 {
		return nil
	}

	ids := make([]uuid.UUID, 0, len(updates))
	args := make([]any, 0, len(updates)*2)
	caseSQL := "CASE id"
	for id, code := range updates {
		ids = append(ids, id)
		caseSQL += " WHEN ? THEN ?"
		args = append(args, id, code)
	}
	caseSQL += " END"

	return db.Model(destination).
		Where("id IN ?", ids).
		UpdateColumn("language_code", gorm.Expr(caseSQL, args...)).Error
}
