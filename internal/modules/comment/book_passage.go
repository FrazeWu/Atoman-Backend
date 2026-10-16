package comment

import (
	"atoman/internal/model"

	"github.com/google/uuid"
)

// The target remains the passage UUID, not the mutable current file or work.
// Removing any public lifecycle layer also blocks comments/replies/likes.
func (r *databaseTargetResolvers) resolveBookPassage(_ Viewer, resourceID uuid.UUID) (ResolvedTarget, error) {
	var passage model.BookDiscussionPassage
	err := r.db.Joins("JOIN book_asset_revisions r ON r.id = book_discussion_passages.revision_id AND r.deleted_at IS NULL").
		Joins("JOIN book_assets a ON a.id = r.asset_id AND a.deleted_at IS NULL").
		Joins("JOIN book_works w ON w.id = a.work_id AND w.deleted_at IS NULL").
		Where("book_discussion_passages.id = ? AND r.status = ? AND a.status = ? AND w.lifecycle_status = ? AND w.edit_status <> ?",
			resourceID, model.BookRevisionStatusReady, model.BookResourceStatusActive, model.BookLifecycleStatusActive, model.BookEditStatusClosed).
		First(&passage).Error
	if err != nil {
		return ResolvedTarget{}, targetLookupError(TargetKindBookPassage, resourceID, err)
	}
	return communityTarget(TargetKindBookPassage, passage.ID, true), nil
}
