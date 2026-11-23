package migrations

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

// RunDebateStructuredMigration ensures partial unique and lookup indexes exist
// for structured debate entries and community challenges.
func RunDebateStructuredMigration(db *gorm.DB) error {
	if db.Migrator().HasTable(&model.DebateEntry{}) {
		if err := db.Exec(
			`CREATE INDEX IF NOT EXISTS idx_debate_entries_debate_parent
			 ON debate_entries (debate_id, parent_id)
			 WHERE deleted_at IS NULL`,
		).Error; err != nil {
			return err
		}
	}

	if db.Migrator().HasTable(&model.DebateChallenge{}) {
		if err := db.Exec(
			`CREATE UNIQUE INDEX IF NOT EXISTS uq_debate_challenge_open_target
			 ON debate_challenges (debate_id, target_type, target_id)
			 WHERE status = 'open' AND deleted_at IS NULL`,
		).Error; err != nil {
			return err
		}
		if err := db.Exec(
			`CREATE INDEX IF NOT EXISTS idx_debate_challenges_debate_status
			 ON debate_challenges (debate_id, status)
			 WHERE deleted_at IS NULL`,
		).Error; err != nil {
			return err
		}
	}

	return nil
}
