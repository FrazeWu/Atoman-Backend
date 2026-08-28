package migrations

import (
	"fmt"

	"atoman/internal/model"

	"gorm.io/gorm"
)

// RunBooksMigration creates the book catalog and private/public asset tables.
// Private content is intentionally kept in separate tables from public metadata.
func RunBooksMigration(db *gorm.DB) error {
	if err := db.AutoMigrate(
		&model.BookWork{},
		&model.BookEdition{},
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
	if err := db.Exec(`
ALTER TABLE user_book_assets DROP CONSTRAINT IF EXISTS chk_user_book_assets_processing_status;
ALTER TABLE user_book_assets ADD CONSTRAINT chk_user_book_assets_processing_status CHECK (processing_status IN ('pending_upload','uploading','uploaded','scanning','processing','metadata_ready','private_available','publication_requested','pending_review','failed','rejected','quarantined','removed'));`).Error; err != nil {
		return fmt.Errorf("ensure book asset processing status constraint: %w", err)
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
	return nil
}
