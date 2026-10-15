package migrations

import (
	"fmt"

	"atoman/internal/model"

	"gorm.io/gorm"
)

// RunBooksV2Migration adds the new resource graph without copying legacy books.
// Composite foreign keys enforce ownership even when writes bypass the service.
func RunBooksV2Migration(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(&model.MediaAsset{}, &model.BookAsset{}, &model.BookAssetRevision{},
			&model.BookDefaultAsset{}, &model.BookReadingSelection{}, &model.BookReadingState{}, &model.BookWikiMerge{}, &model.BookUploadSession{},
			&model.BookReadingSession{}, &model.BookReadingWrite{}); err != nil {
			return fmt.Errorf("migrate book resources: %w", err)
		}
		if err := tx.Exec(`
CREATE UNIQUE INDEX IF NOT EXISTS idx_book_assets_work_language ON book_assets (id, work_id, language);
CREATE UNIQUE INDEX IF NOT EXISTS idx_book_assets_work ON book_assets (id, work_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_book_revisions_asset ON book_asset_revisions (id, asset_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_book_reading_session_identity ON book_reading_sessions (id, user_id, revision_id);
`).Error; err != nil {
			return err
		}
		constraints := []struct{ table, name, definition string }{
			{"book_assets", "fk_book_assets_work", "FOREIGN KEY (work_id) REFERENCES book_works(id)"},
			{"book_assets", "fk_book_assets_current_revision", "FOREIGN KEY (current_revision_id, id) REFERENCES book_asset_revisions(id, asset_id) DEFERRABLE INITIALLY DEFERRED"},
			{"book_asset_revisions", "fk_book_revisions_asset", "FOREIGN KEY (asset_id) REFERENCES book_assets(id)"},
			{"book_asset_revisions", "fk_book_revisions_media", "FOREIGN KEY (media_asset_id) REFERENCES media_assets(id)"},
			{"book_default_assets", "fk_book_default_asset", "FOREIGN KEY (asset_id, work_id, language) REFERENCES book_assets(id, work_id, language)"},
			{"book_reading_selections", "fk_book_selection_asset", "FOREIGN KEY (asset_id, work_id) REFERENCES book_assets(id, work_id)"},
			{"book_reading_selections", "fk_book_selection_revision", "FOREIGN KEY (revision_id, asset_id) REFERENCES book_asset_revisions(id, asset_id)"},
			{"book_reading_selections", "fk_book_selection_user", "FOREIGN KEY (user_id) REFERENCES \"Users\"(uuid)"},
			{"book_reading_states", "fk_book_state_user", "FOREIGN KEY (user_id) REFERENCES \"Users\"(uuid)"},
			{"book_reading_states", "fk_book_state_revision", "FOREIGN KEY (revision_id) REFERENCES book_asset_revisions(id)"},
			{"book_upload_sessions", "fk_book_upload_asset", "FOREIGN KEY (asset_id) REFERENCES book_assets(id) DEFERRABLE INITIALLY IMMEDIATE"},
			{"book_upload_sessions", "fk_book_upload_user", "FOREIGN KEY (user_id) REFERENCES \"Users\"(uuid)"},
			{"book_reading_sessions", "fk_book_reading_session_user", "FOREIGN KEY (user_id) REFERENCES \"Users\"(uuid)"},
			{"book_reading_sessions", "fk_book_reading_session_revision", "FOREIGN KEY (revision_id) REFERENCES book_asset_revisions(id)"},
			{"book_reading_writes", "fk_book_reading_write_session", "FOREIGN KEY (session_id, user_id, revision_id) REFERENCES book_reading_sessions(id, user_id, revision_id)"},
			{"book_reading_writes", "fk_book_reading_write_state", "FOREIGN KEY (user_id, revision_id) REFERENCES book_reading_states(user_id, revision_id)"},
		}
		for _, constraint := range constraints {
			var exists bool
			if err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = ?::regclass AND conname = ?)`, constraint.table, constraint.name).Scan(&exists).Error; err != nil {
				return err
			}
			if !exists {
				if err := tx.Exec("ALTER TABLE " + constraint.table + " ADD CONSTRAINT " + constraint.name + " " + constraint.definition).Error; err != nil {
					return fmt.Errorf("add %s: %w", constraint.name, err)
				}
			}
		}
		// Resource moves during a merge must be checked at commit, after both
		// asset ownership and dependent defaults/selections have been updated.
		if err := tx.Exec(`
ALTER TABLE book_assets ALTER CONSTRAINT fk_book_assets_work DEFERRABLE INITIALLY IMMEDIATE;
ALTER TABLE book_asset_revisions ALTER CONSTRAINT fk_book_revisions_asset DEFERRABLE INITIALLY IMMEDIATE;
ALTER TABLE book_default_assets ALTER CONSTRAINT fk_book_default_asset DEFERRABLE INITIALLY IMMEDIATE;
ALTER TABLE book_reading_selections ALTER CONSTRAINT fk_book_selection_asset DEFERRABLE INITIALLY IMMEDIATE;`).Error; err != nil {
			return err
		}
		// Scan/down-take status can change, but the identity of a revision cannot.
		return tx.Exec(`
CREATE OR REPLACE FUNCTION guard_book_revision_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.id, NEW.asset_id, NEW.number, NEW.media_asset_id, NEW.sha256, NEW.original_filename, NEW.created_by, NEW.created_at)
 IS DISTINCT FROM ROW(OLD.id, OLD.asset_id, OLD.number, OLD.media_asset_id, OLD.sha256, OLD.original_filename, OLD.created_by, OLD.created_at) THEN
  RAISE EXCEPTION 'book file revisions are immutable';
 END IF;
 RETURN NEW;
END; $$;
DROP TRIGGER IF EXISTS book_revision_identity ON book_asset_revisions;
CREATE TRIGGER book_revision_identity BEFORE UPDATE ON book_asset_revisions
FOR EACH ROW EXECUTE FUNCTION guard_book_revision_identity();
CREATE OR REPLACE FUNCTION guard_book_file_media() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS (SELECT 1 FROM book_asset_revisions WHERE media_asset_id = OLD.id)
 AND ROW(NEW.id, NEW.key, NEW.url, NEW.content_type, NEW.size, NEW.purpose)
 IS DISTINCT FROM ROW(OLD.id, OLD.key, OLD.url, OLD.content_type, OLD.size, OLD.purpose) THEN
  RAISE EXCEPTION 'media referenced by a book revision is immutable';
 END IF;
 RETURN NEW;
END; $$;
DROP TRIGGER IF EXISTS book_file_media_identity ON media_assets;
CREATE TRIGGER book_file_media_identity BEFORE UPDATE ON media_assets
FOR EACH ROW EXECUTE FUNCTION guard_book_file_media();`).Error
	})
}
