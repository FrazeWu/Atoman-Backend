package comment

import (
	"errors"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type repository struct{}

type lockedCommentHierarchy struct {
	Target model.DiscussionTarget
	Root   model.CommentEntry
	Entry  model.CommentEntry
}

func (repository) lockTarget(tx *gorm.DB, resolved ResolvedTarget) (model.DiscussionTarget, error) {
	candidate := model.DiscussionTarget{
		Kind:        resolved.Kind,
		ResourceID:  resolved.ResourceID,
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
	if target.ResourceID != resolved.ResourceID || !sameOptionalUUID(target.OwnerID, resolved.OwnerID) {
		if err := tx.Model(&target).Updates(map[string]any{"resource_id": resolved.ResourceID, "owner_id": resolved.OwnerID}).Error; err != nil {
			return model.DiscussionTarget{}, err
		}
		target.ResourceID = resolved.ResourceID
		target.OwnerID = resolved.OwnerID
	}
	return target, nil
}

func (repository) findTarget(db *gorm.DB, resolved ResolvedTarget) (model.DiscussionTarget, error) {
	var target model.DiscussionTarget
	err := db.Where("kind = ? AND resource_key = ?", resolved.Kind, resolved.ResourceKey).First(&target).Error
	return target, err
}

func (repository) findTargetByID(db *gorm.DB, id uuid.UUID) (model.DiscussionTarget, error) {
	var target model.DiscussionTarget
	err := db.First(&target, "id = ?", id).Error
	return target, err
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

func (r repository) lockCommentHierarchy(tx *gorm.DB, located model.CommentEntry) (lockedCommentHierarchy, error) {
	target, err := r.lockTargetByID(tx, located.TargetID)
	if err != nil {
		return lockedCommentHierarchy{}, err
	}
	rootID := located.ID
	if located.RootID != nil {
		rootID = *located.RootID
	}
	root, err := r.findRoot(tx, rootID)
	if err != nil {
		return lockedCommentHierarchy{}, err
	}
	entry := root
	if located.RootID != nil {
		entry, err = r.lockComment(tx, located.ID)
		if err != nil {
			return lockedCommentHierarchy{}, err
		}
	}
	if root.TargetID != target.ID || root.RootID != nil || entry.TargetID != target.ID || entry.ID != located.ID || !sameOptionalUUID(entry.RootID, located.RootID) {
		return lockedCommentHierarchy{}, ErrCommentNotFound
	}
	return lockedCommentHierarchy{Target: target, Root: root, Entry: entry}, nil
}

func (r repository) lockReplyHierarchy(tx *gorm.DB, targetID, replyID uuid.UUID) (model.CommentEntry, model.CommentEntry, error) {
	hint, err := r.findComment(tx, replyID)
	if err != nil {
		return model.CommentEntry{}, model.CommentEntry{}, err
	}
	rootID := hint.ID
	if hint.RootID != nil {
		rootID = *hint.RootID
	}
	root, err := r.findRoot(tx, rootID)
	if err != nil {
		return model.CommentEntry{}, model.CommentEntry{}, err
	}
	reply := root
	if hint.RootID != nil {
		reply, err = r.lockComment(tx, replyID)
		if err != nil {
			return model.CommentEntry{}, model.CommentEntry{}, err
		}
	}
	if root.TargetID != targetID || root.RootID != nil || reply.TargetID != targetID || reply.ID != replyID || !sameOptionalUUID(reply.RootID, hint.RootID) {
		return model.CommentEntry{}, model.CommentEntry{}, ErrInvalidReply
	}
	return reply, root, nil
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
