package comment

import (
	"errors"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type repository struct{}

func (repository) lockTarget(tx *gorm.DB, resolved ResolvedTarget) (model.DiscussionTarget, error) {
	candidate := model.DiscussionTarget{
		Kind:        resolved.Kind,
		ResourceKey: resolved.ResourceKey,
		OwnerID:     resolved.OwnerID,
		NextFloor:   1,
	}
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "kind"}, {Name: "resource_key"}},
		DoNothing: true,
	}).Create(&candidate).Error; err != nil {
		return model.DiscussionTarget{}, err
	}

	var target model.DiscussionTarget
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("kind = ? AND resource_key = ?", resolved.Kind, resolved.ResourceKey).
		First(&target).Error
	if err != nil {
		return model.DiscussionTarget{}, err
	}
	if !sameOptionalUUID(target.OwnerID, resolved.OwnerID) {
		if err := tx.Model(&target).Update("owner_id", resolved.OwnerID).Error; err != nil {
			return model.DiscussionTarget{}, err
		}
		target.OwnerID = resolved.OwnerID
	}
	return target, nil
}

func (repository) findTarget(db *gorm.DB, resolved ResolvedTarget) (model.DiscussionTarget, error) {
	var target model.DiscussionTarget
	err := db.Where("kind = ? AND resource_key = ?", resolved.Kind, resolved.ResourceKey).First(&target).Error
	return target, err
}

func (repository) findReply(tx *gorm.DB, id uuid.UUID) (model.CommentEntry, error) {
	var reply model.CommentEntry
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&reply, "id = ?", id).Error
	return reply, err
}

func (repository) findRoot(tx *gorm.DB, id uuid.UUID) (model.CommentEntry, error) {
	var root model.CommentEntry
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&root, "id = ?", id).Error
	return root, err
}

func (repository) createComment(tx *gorm.DB, entry *model.CommentEntry) error {
	return tx.Create(entry).Error
}

func (repository) findComment(db *gorm.DB, id uuid.UUID) (model.CommentEntry, error) {
	var entry model.CommentEntry
	err := db.First(&entry, "id = ?", id).Error
	return entry, err
}

func (repository) lockComment(tx *gorm.DB, id uuid.UUID) (model.CommentEntry, error) {
	var entry model.CommentEntry
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&entry, "id = ?", id).Error
	return entry, err
}

func (repository) lockTargetByID(tx *gorm.DB, id uuid.UUID) (model.DiscussionTarget, error) {
	var target model.DiscussionTarget
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&target, "id = ?", id).Error
	return target, err
}

func sameOptionalUUID(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func isNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}
