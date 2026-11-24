package books

import (
	"atoman/internal/model"
	"atoman/internal/platform/indexnow"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Only actual publications (or withdrawal of an already-public list) may
// notify search engines. Saving a draft never calls this helper.
func bookListIndexPaths(id uuid.UUID, wasPublic, willBePublic bool) []string {
	if id == uuid.Nil || (!wasPublic && !willBePublic) {
		return nil
	}
	return []string{"/books/lists", "/books/lists/" + id.String()}
}

// Read the entity's actual persisted lifecycle, rather than trusting the
// caller's revision payload. A merged source is eligible only in a merge
// operation, whose existing preconditions require both entries to be active.
func enqueueBookWikiIndexNow(tx *gorm.DB, kind string, id uuid.UUID, editType string) error {
	if !indexnow.DurableEnabled() || id == uuid.Nil {
		return nil
	}
	switch kind {
	case "work":
		var work model.BookWork
		if err := tx.Select("id", "lifecycle_status", "edit_status").First(&work, "id = ?", id).Error; err != nil {
			return err
		}
		if (work.LifecycleStatus == model.BookLifecycleStatusActive && work.EditStatus != model.BookEditStatusClosed) ||
			(editType == "merge" && work.LifecycleStatus == model.BookLifecycleStatusMerged) {
			return indexnow.EnqueuePathsTx(tx, "/books", "/books/work/"+id.String())
		}
	case "edition":
		var edition model.BookEdition
		if err := tx.Select("id", "work_id", "lifecycle_status").First(&edition, "id = ?", id).Error; err != nil {
			return err
		}
		if edition.LifecycleStatus != model.BookLifecycleStatusActive &&
			!(editType == "merge" && edition.LifecycleStatus == model.BookLifecycleStatusMerged) {
			return nil
		}
		var count int64
		if err := tx.Model(&model.BookWork{}).Where("id = ? AND lifecycle_status = ? AND edit_status <> ?",
			edition.WorkID, model.BookLifecycleStatusActive, model.BookEditStatusClosed).Count(&count).Error; err != nil {
			return err
		}
		if count == 1 {
			return indexnow.EnqueuePathsTx(tx, "/books", "/books/work/"+edition.WorkID.String(), "/books/edition/"+id.String())
		}
	}
	return nil
}
