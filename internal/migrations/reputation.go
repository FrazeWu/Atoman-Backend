package migrations

import "gorm.io/gorm"

func RunReputationIndexes(db *gorm.DB) error {
	if !db.Migrator().HasTable("reputation_runs") {
		return nil
	}
	return db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_reputation_runs_single_running
		ON reputation_runs (status) WHERE status = 'running'`).Error
}
