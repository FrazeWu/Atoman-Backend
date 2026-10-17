package books

import (
	"atoman/internal/model"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Keep comment UUIDs and reply/like/report relationships; only renumber source root floors.
func mergeBookWorkComments(tx *gorm.DB, sourceID, targetID uuid.UUID) error {
	if !tx.Migrator().HasTable(&model.DiscussionTarget{}) {
		return nil
	}
	var source, target model.DiscussionTarget
	err := tx.Where("kind = 'book_work' AND resource_id = ?", sourceID).First(&source).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	err = tx.Where("kind = 'book_work' AND resource_id = ?", targetID).First(&target).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Model(&source).Updates(map[string]any{"resource_id": targetID, "resource_key": targetID.String()}).Error
	}
	if err != nil {
		return err
	}
	if err := tx.Unscoped().Model(&model.CommentEntry{}).Where("target_id = ?", source.ID).Updates(map[string]any{"target_id": target.ID, "floor_number": gorm.Expr("CASE WHEN floor_number IS NULL THEN NULL ELSE floor_number + ? END", target.NextFloor)}).Error; err != nil {
		return err
	}
	if tx.Migrator().HasTable(&model.CommentPublishRecord{}) {
		if err := tx.Model(&model.CommentPublishRecord{}).Where("target_id = ?", source.ID).Update("target_id", target.ID).Error; err != nil {
			return err
		}
	}
	if err := tx.Model(&target).Updates(map[string]any{"comment_count": target.CommentCount + source.CommentCount, "root_count": target.RootCount + source.RootCount, "next_floor": target.NextFloor + source.NextFloor}).Error; err != nil {
		return err
	}
	return tx.Unscoped().Delete(&source).Error
}
