package migrations

import (
	"fmt"
	"strings"

	"atoman/internal/model"

	"gorm.io/gorm"
)

const bookPublisherBackfillBatchSize = 500

// RunBooksMigration creates the book catalog and private/public asset tables.
// Private content is intentionally kept in separate tables from public metadata.
func RunBooksMigration(db *gorm.DB) error {
	if err := db.AutoMigrate(
		&model.BookWork{},
		&model.BookEdition{},
		&model.BookPublisher{},
		&model.BookPerson{},
		&model.BookContribution{},
		&model.BookSource{},
		&model.BookEdit{},
		&model.UserBookImport{},
		&model.UserBookAsset{},
		&model.UserBookReadingState{},
		&model.UserBookShelf{},
		&model.BookPublicationRequest{},
		&model.BookRightsDeclaration{},
		&model.PublishedBookAsset{},
		&model.BookRating{},
		&model.BookReview{},
		&model.BookPostLink{},
		&model.BookEditVote{},
		&model.BookPublicationReport{},
		&model.BookPublicationAppeal{},
	); err != nil {
		return fmt.Errorf("migrate books schema: %w", err)
	}
	if err := backfillBookPublishers(db); err != nil {
		return fmt.Errorf("backfill book publishers: %w", err)
	}
	if err := db.Exec(`
ALTER TABLE user_book_assets DROP CONSTRAINT IF EXISTS chk_user_book_assets_format;
ALTER TABLE user_book_assets ADD CONSTRAINT chk_user_book_assets_format CHECK (format IN ('epub','pdf','txt','cbz','cbr'));
ALTER TABLE published_book_assets DROP CONSTRAINT IF EXISTS chk_published_book_assets_format;
ALTER TABLE published_book_assets ADD CONSTRAINT chk_published_book_assets_format CHECK (format IN ('epub','pdf','txt','cbz','cbr'));
ALTER TABLE user_book_assets DROP CONSTRAINT IF EXISTS chk_user_book_assets_processing_status;
ALTER TABLE user_book_assets ADD CONSTRAINT chk_user_book_assets_processing_status CHECK (processing_status IN ('pending_upload','uploading','uploaded','scanning','processing','metadata_ready','private_available','publication_requested','pending_review','failed','rejected','quarantined','removed'));`).Error; err != nil {
		return fmt.Errorf("ensure book asset processing status constraint: %w", err)
	}
	if err := db.Exec(`
CREATE UNIQUE INDEX IF NOT EXISTS idx_published_book_assets_source_asset
ON published_book_assets (source_asset_id) WHERE deleted_at IS NULL;`).Error; err != nil {
		return fmt.Errorf("ensure published book asset source uniqueness: %w", err)
	}
	if err := db.Exec(`
CREATE UNIQUE INDEX IF NOT EXISTS idx_book_publication_requests_pending_asset
ON book_publication_requests (asset_id) WHERE status = 'pending_review';`).Error; err != nil {
		return fmt.Errorf("ensure pending book publication request uniqueness: %w", err)
	}
	if err := db.Exec(`
CREATE UNIQUE INDEX IF NOT EXISTS idx_book_publication_appeals_pending_request
ON book_publication_appeals (publication_request_id) WHERE status = 'pending';`).Error; err != nil {
		return fmt.Errorf("ensure pending book publication appeal uniqueness: %w", err)
	}
	if err := db.Exec(`
CREATE INDEX IF NOT EXISTS idx_book_works_active_title
ON book_works (title) WHERE lifecycle_status = 'active' AND edit_status <> 'closed' AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_book_editions_active_work
ON book_editions (work_id, published_date, title) WHERE lifecycle_status = 'active' AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_book_publishers_active_name
ON book_publishers (name) WHERE lifecycle_status = 'active' AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_book_people_active_name
ON book_people (name) WHERE lifecycle_status = 'active' AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_book_contributions_work_role
ON book_contributions (work_id, role, position) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_published_book_assets_status_created
ON published_book_assets (status, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_book_sources_live_type_url
ON book_sources (target_type, url) WHERE deleted_at IS NULL;`).Error; err != nil {
		return fmt.Errorf("ensure book catalog indexes: %w", err)
	}
	return nil
}

func backfillBookPublishers(db *gorm.DB) error {
	var names []string
	if err := db.Table("book_editions").Where("publisher_id IS NULL AND NULLIF(BTRIM(publisher), '') IS NOT NULL").Distinct().Pluck("publisher", &names).Error; err != nil {
		return err
	}

	uniqueNames := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		uniqueNames = append(uniqueNames, name)
	}

	for start := 0; start < len(uniqueNames); start += bookPublisherBackfillBatchSize {
		end := min(start+bookPublisherBackfillBatchSize, len(uniqueNames))
		batch := uniqueNames[start:end]
		var existing []string
		if err := db.Model(&model.BookPublisher{}).Where("name IN ? AND deleted_at IS NULL", batch).Pluck("name", &existing).Error; err != nil {
			return err
		}
		existingNames := make(map[string]struct{}, len(existing))
		for _, name := range existing {
			existingNames[name] = struct{}{}
		}

		publishers := make([]model.BookPublisher, 0, len(batch))
		for _, name := range batch {
			if _, exists := existingNames[name]; exists {
				continue
			}
			publishers = append(publishers, model.BookPublisher{
				Name: name, LifecycleStatus: model.BookLifecycleStatusActive,
				EditStatus: model.BookEditStatusDevelopment,
			})
		}
		if len(publishers) > 0 {
			if err := db.CreateInBatches(&publishers, bookPublisherBackfillBatchSize).Error; err != nil {
				return err
			}
		}
	}
	return db.Exec(`
		UPDATE book_editions
		SET publisher_id = (
			SELECT id FROM book_publishers
			WHERE name = TRIM(book_editions.publisher) AND deleted_at IS NULL
			ORDER BY created_at, id
			LIMIT 1
		)
		WHERE publisher_id IS NULL
			AND NULLIF(TRIM(publisher), '') IS NOT NULL
			AND EXISTS (
				SELECT 1 FROM book_publishers
				WHERE name = TRIM(book_editions.publisher) AND deleted_at IS NULL
			)`).Error
}
