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
		&model.BookProducer{},
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
		&model.UserBookAnnotation{},
	); err != nil {
		return fmt.Errorf("migrate books schema: %w", err)
	}
	if err := MigrateUserBookAnnotations(db); err != nil {
		return fmt.Errorf("migrate user book annotations: %w", err)
	}
	if err := backfillBookPublishers(db); err != nil {
		return fmt.Errorf("backfill book publishers: %w", err)
	}
	if err := seedBookOrganizations(db); err != nil {
		return fmt.Errorf("seed book organizations: %w", err)
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

func seedBookOrganizations(db *gorm.DB) error {
	for _, name := range []string{
		"人民文学出版社", "商务印书馆", "中华书局", "生活·读书·新知三联书店", "人民出版社",
		"上海译文出版社", "译林出版社", "作家出版社", "北京十月文艺出版社", "江苏凤凰文艺出版社",
		"长江文艺出版社", "湖南文艺出版社", "浙江文艺出版社", "上海文艺出版社", "南海出版公司",
		"广西师范大学出版社", "北京大学出版社", "清华大学出版社", "上海交通大学出版社", "复旦大学出版社",
		"机械工业出版社", "电子工业出版社", "人民邮电出版社", "中信出版集团", "法律出版社",
		"社会科学文献出版社", "人民教育出版社", "高等教育出版社", "教育科学出版社", "接力出版社",
		"二十一世纪出版社集团", "天地出版社", "化学工业出版社", "北京联合出版公司", "新星出版社",
	} {
		if err := ensureBookPublisher(db, name); err != nil {
			return err
		}
	}
	for _, name := range []string{
		"果麦文化", "磨铁图书", "读客文化", "新经典文化", "后浪出版公司",
		"博集天卷", "时代华语", "青豆书坊", "浦睿文化", "湛庐文化",
	} {
		if err := ensureBookProducer(db, name); err != nil {
			return err
		}
	}
	return nil
}

func ensureBookPublisher(db *gorm.DB, name string) error {
	var count int64
	if err := db.Model(&model.BookPublisher{}).Where("name = ? AND deleted_at IS NULL", name).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return db.Create(&model.BookPublisher{Name: name, SortName: name, LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}).Error
}

func ensureBookProducer(db *gorm.DB, name string) error {
	var count int64
	if err := db.Model(&model.BookProducer{}).Where("name = ? AND deleted_at IS NULL", name).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return db.Create(&model.BookProducer{Name: name, SortName: name, LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}).Error
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
