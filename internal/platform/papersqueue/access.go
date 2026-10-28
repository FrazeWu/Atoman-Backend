package papersqueue

import (
	"atoman/internal/model"
	"gorm.io/gorm"
)

// eligibleJob requires an active Papers invitation and a current, private
// project owned by the same user. Do not run queued jobs after revocation.
func eligibleJob(tx *gorm.DB, job model.PaperCompileJob) (bool, error) {
	var row struct{ Allowed bool }
	err := tx.Raw(`
SELECT EXISTS(
 SELECT 1 FROM paper_projects p
 JOIN paper_access_grants g ON g.user_id = p.owner_id
 WHERE p.id = ? AND p.owner_id = ? AND p.deleted_at IS NULL
  AND g.status = 'active'
  AND (g.expires_at IS NULL OR g.expires_at > CURRENT_TIMESTAMP)
) AS allowed
`, job.ProjectID, job.OwnerID).Scan(&row).Error
	return row.Allowed, err
}
