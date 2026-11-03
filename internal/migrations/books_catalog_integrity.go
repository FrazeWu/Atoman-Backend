package migrations

import (
	"fmt"

	"atoman/internal/model"

	"gorm.io/gorm"
)

// RunBooksCatalogIntegrityMigration protects bibliographic links without
// guessing how historical public metadata should be repaired. NOT VALID
// avoids rewriting or rejecting old catalog rows during rollout while still
// enforcing the constraints for future writes.
func RunBooksCatalogIntegrityMigration(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(&model.BookCatalogCreateReceipt{}); err != nil {
			return fmt.Errorf("create catalog idempotency receipts: %w", err)
		}
		// A work can have same-name authors, but must not have the same person
		// linked twice in the same role. Separate partial indexes avoid
		// PostgreSQL's default NULL != NULL uniqueness behavior.
		for _, sql := range []string{
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_book_contrib_work_live_unique
				ON book_contributions (work_id, person_id, role)
				WHERE deleted_at IS NULL AND work_id IS NOT NULL AND edition_id IS NULL`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_book_contrib_edition_live_unique
				ON book_contributions (edition_id, person_id, role)
				WHERE deleted_at IS NULL AND edition_id IS NOT NULL AND work_id IS NULL`,
		} {
			if err := tx.Exec(sql).Error; err != nil {
				return fmt.Errorf("catalog contribution uniqueness (check existing duplicate rows first): %w", err)
			}
		}
		constraints := []struct{ table, name, clause string }{
			{"book_editions", "fk_book_edition_owner_work", "FOREIGN KEY (work_id) REFERENCES book_works(id) DEFERRABLE INITIALLY DEFERRED NOT VALID"},
			{"book_contributions", "fk_book_contribution_work", "FOREIGN KEY (work_id) REFERENCES book_works(id) DEFERRABLE INITIALLY DEFERRED NOT VALID"},
			{"book_contributions", "fk_book_contribution_edition", "FOREIGN KEY (edition_id) REFERENCES book_editions(id) DEFERRABLE INITIALLY DEFERRED NOT VALID"},
			{"book_contributions", "fk_book_contribution_person", "FOREIGN KEY (person_id) REFERENCES book_people(id) DEFERRABLE INITIALLY DEFERRED NOT VALID"},
			{"book_contributions", "chk_book_contribution_owner", "CHECK ((work_id IS NULL) <> (edition_id IS NULL)) NOT VALID"},
			// Old, incomplete active editions are grandfathered pending manual
			// bibliographic repair; new active editions must have core identity.
			{"book_editions", "chk_book_active_edition_identity",
				"CHECK (lifecycle_status <> 'active' OR (NULLIF(BTRIM(language), '') IS NOT NULL AND (NULLIF(BTRIM(isbn10), '') IS NOT NULL OR NULLIF(BTRIM(isbn13), '') IS NOT NULL OR NULLIF(BTRIM(publisher), '') IS NOT NULL OR NULLIF(BTRIM(producer), '') IS NOT NULL OR (NULLIF(BTRIM(title), '') IS NOT NULL AND published_date IS NOT NULL)))) NOT VALID"},
		}
		for _, c := range constraints {
			var exists bool
			if err := tx.Raw("SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid = ?::regclass AND conname = ?)", c.table, c.name).Scan(&exists).Error; err != nil {
				return err
			}
			if !exists {
				if err := tx.Exec("ALTER TABLE " + c.table + " ADD CONSTRAINT " + c.name + " " + c.clause).Error; err != nil {
					return fmt.Errorf("catalog constraint %s: %w", c.name, err)
				}
			}
		}
		return nil
	})
}
