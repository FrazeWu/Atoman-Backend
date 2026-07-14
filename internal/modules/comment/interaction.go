package comment

import (
	"errors"
	"fmt"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var hotScoreStatuses = []string{commentStatusActive, "auto_folded"}

func (s *Service) Edit(user authctx.CurrentUser, commentID uuid.UUID, input EditCommentInput) (CommentDTO, error) {
	if err := s.validateAuthor(user); err != nil {
		return CommentDTO{}, err
	}
	normalized, _, err := validateCommentContent(input.Content, input.AttachmentIDs)
	if err != nil {
		return CommentDTO{}, err
	}
	_, err = s.validateAttachments(s.db, user.ID, input.AttachmentIDs)
	if err != nil {
		return CommentDTO{}, err
	}
	if err := s.validateMentions(s.db, normalized, input.Mentions); err != nil {
		return CommentDTO{}, err
	}
	located, _, resolved, err := s.resolveCommentMutation(Viewer{UserID: &user.ID}, commentID)
	if err != nil {
		return CommentDTO{}, err
	}

	err = withCreateTransactionMutex(s.createMu, func() error {
		return s.db.Transaction(func(tx *gorm.DB) error {
			hierarchy, err := s.repo.lockCommentHierarchy(tx, located)
			if isNotFound(err) || errors.Is(err, ErrCommentNotFound) {
				return ErrCommentNotFound
			}
			if err != nil {
				return err
			}
			if !targetMatchesResolved(hierarchy.Target, resolved) {
				return ErrInvalidTargetResource
			}
			entry := hierarchy.Entry
			if !isVisibleCommentStatus(entry.Status) {
				return ErrCommentNotFound
			}
			if entry.AuthorID != user.ID {
				return ErrCommentForbidden
			}
			assets, err := s.validateAttachments(tx, user.ID, input.AttachmentIDs)
			if err != nil {
				return err
			}
			if err := s.validateMentions(tx, normalized, input.Mentions); err != nil {
				return err
			}
			for _, relation := range []any{&model.CommentMention{}, &model.CommentAttachment{}, &model.CommentTimeAnchor{}} {
				if err := tx.Unscoped().Where("comment_id = ?", entry.ID).Delete(relation).Error; err != nil {
					return fmt.Errorf("replace comment relations: %w", err)
				}
			}
			if err := createCommentRelations(tx, entry.ID, input.Mentions, assets, resolved, normalized); err != nil {
				return err
			}
			now := time.Now()
			result := tx.Model(&model.CommentEntry{}).Where("id = ?", entry.ID).Updates(map[string]any{
				"content": normalized, "content_hash": ContentHash(normalized, input.AttachmentIDs), "edited_at": now,
			})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrCommentNotFound
			}
			return nil
		})
	})
	if err != nil {
		return CommentDTO{}, err
	}
	dto, err := s.loadCommentDTO(s.db, commentID, &user.ID)
	if err != nil {
		return CommentDTO{}, err
	}
	return dto, nil
}

func (s *Service) Delete(user authctx.CurrentUser, commentID uuid.UUID) error {
	if err := s.validateAuthor(user); err != nil {
		return err
	}
	located, _, resolved, err := s.resolveCommentMutation(Viewer{UserID: &user.ID}, commentID)
	if err != nil {
		return err
	}
	return withCreateTransactionMutex(s.createMu, func() error {
		return s.db.Transaction(func(tx *gorm.DB) error {
			hierarchy, err := s.repo.lockCommentHierarchy(tx, located)
			if isNotFound(err) || errors.Is(err, ErrCommentNotFound) {
				return ErrCommentNotFound
			}
			if err != nil {
				return err
			}
			if !targetMatchesResolved(hierarchy.Target, resolved) {
				return ErrInvalidTargetResource
			}
			entry := hierarchy.Entry
			if !isVisibleCommentStatus(entry.Status) {
				return ErrCommentNotFound
			}
			target := hierarchy.Target
			isOwner := resolved.OwnerID != nil && *resolved.OwnerID == user.ID
			if entry.AuthorID != user.ID && !isOwner {
				return ErrCommentForbidden
			}

			ids := []uuid.UUID{entry.ID}
			rootDelta := 0
			if entry.RootID == nil {
				var childIDs []uuid.UUID
				if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).Model(&model.CommentEntry{}).Where("root_id = ?", entry.ID).Order("id ASC").Pluck("id", &childIDs).Error; err != nil {
					return err
				}
				ids = append(ids, childIDs...)
				rootDelta = 1
			}
			var visibleDeleteCount int64
			if err := tx.Model(&model.CommentEntry{}).Where("id IN ? AND status IN ?", ids, hotScoreStatuses).Count(&visibleDeleteCount).Error; err != nil {
				return err
			}
			if err := deleteCommentRelations(tx, ids); err != nil {
				return err
			}
			result := tx.Unscoped().Where("id IN ?", ids).Delete(&model.CommentEntry{})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != int64(len(ids)) {
				return ErrCommentNotFound
			}
			updates := map[string]any{
				"comment_count": gorm.Expr("comment_count - ?", visibleDeleteCount),
				"root_count":    gorm.Expr("root_count - ?", rootDelta),
			}
			if target.PinnedCommentID != nil && *target.PinnedCommentID == entry.ID {
				updates["pinned_comment_id"] = gorm.Expr("NULL")
			}
			counter := tx.Model(&model.DiscussionTarget{}).
				Where("id = ? AND comment_count >= ? AND root_count >= ?", target.ID, visibleDeleteCount, rootDelta).Updates(updates)
			if counter.Error != nil {
				return counter.Error
			}
			if counter.RowsAffected != 1 {
				return fmt.Errorf("delete comment counters: inconsistent target")
			}
			if entry.RootID != nil {
				var replyCount int64
				if err := tx.Model(&model.CommentEntry{}).Where("root_id = ? AND status IN ?", *entry.RootID, hotScoreStatuses).Count(&replyCount).Error; err != nil {
					return err
				}
				updated := tx.Model(&model.CommentEntry{}).Where("id = ? AND root_id IS NULL", *entry.RootID).Update("reply_count", replyCount)
				if updated.Error != nil {
					return updated.Error
				}
				if updated.RowsAffected != 1 {
					return ErrCommentNotFound
				}
				return s.recomputeRootHotScore(tx, *entry.RootID, time.Now())
			}
			return nil
		})
	})
}

func isVisibleCommentStatus(status string) bool {
	return status == commentStatusActive || status == "auto_folded"
}

func deleteCommentRelations(tx *gorm.DB, ids []uuid.UUID) error {
	for _, relation := range []any{&model.CommentMention{}, &model.CommentAttachment{}, &model.CommentLike{}, &model.CommentReport{}, &model.CommentTimeAnchor{}} {
		if err := tx.Unscoped().Where("comment_id IN ?", ids).Delete(relation).Error; err != nil {
			return fmt.Errorf("delete comment relations: %w", err)
		}
	}
	if err := deleteCommentExtensionRelations(tx, ids); err != nil {
		return err
	}
	return nil
}

func deleteCommentExtensionRelations(tx *gorm.DB, ids []uuid.UUID) error {
	for _, relation := range []any{&model.TimelineRevisionProposal{}, &model.DebateArgumentDetail{}, &model.DebateArgumentDebateRef{}} {
		if err := tx.Unscoped().Where("comment_id IN ?", ids).Delete(relation).Error; err != nil {
			return fmt.Errorf("delete comment extension relations: %w", err)
		}
	}
	if err := tx.Unscoped().Where("comment_id IN ? OR referenced_comment_id IN ?", ids, ids).Delete(&model.DebateArgumentReference{}).Error; err != nil {
		return fmt.Errorf("delete comment extension references: %w", err)
	}
	return nil
}

func (s *Service) Like(user authctx.CurrentUser, commentID uuid.UUID) error {
	return s.setLiked(user, commentID, true)
}

func (s *Service) Unlike(user authctx.CurrentUser, commentID uuid.UUID) error {
	return s.setLiked(user, commentID, false)
}

func (s *Service) setLiked(user authctx.CurrentUser, commentID uuid.UUID, liked bool) error {
	if err := s.validateAuthor(user); err != nil {
		return err
	}
	located, _, resolved, err := s.resolveCommentMutation(Viewer{UserID: &user.ID}, commentID)
	if err != nil {
		return err
	}
	return withCreateTransactionMutex(s.createMu, func() error {
		return s.db.Transaction(func(tx *gorm.DB) error {
			hierarchy, err := s.repo.lockCommentHierarchy(tx, located)
			if isNotFound(err) || errors.Is(err, ErrCommentNotFound) {
				return ErrCommentNotFound
			}
			if err != nil {
				return err
			}
			if !targetMatchesResolved(hierarchy.Target, resolved) {
				return ErrInvalidTargetResource
			}
			entry := hierarchy.Entry
			if !isVisibleCommentStatus(entry.Status) {
				return ErrCommentNotFound
			}
			var existing model.CommentLike
			err = tx.Where("comment_id = ? AND user_id = ?", commentID, user.ID).First(&existing).Error
			if liked && isNotFound(err) {
				if err := tx.Create(&model.CommentLike{CommentID: commentID, UserID: user.ID}).Error; err != nil {
					return err
				}
			} else if !liked && err == nil {
				if err := tx.Delete(&existing).Error; err != nil {
					return err
				}
			} else if err != nil && !isNotFound(err) {
				return err
			}
			var count int64
			if err := tx.Model(&model.CommentLike{}).Where("comment_id = ?", commentID).Count(&count).Error; err != nil {
				return err
			}
			updated := tx.Model(&model.CommentEntry{}).Where("id = ?", commentID).Update("like_count", count)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrCommentNotFound
			}
			return s.recomputeRootHotScore(tx, hierarchy.Root.ID, time.Now())
		})
	})
}

func (s *Service) Mark(user authctx.CurrentUser, targetRef TargetRef, commentID uuid.UUID) error {
	if err := s.validateAuthor(user); err != nil {
		return err
	}
	resolved, err := s.resolveVisible(Viewer{UserID: &user.ID}, targetRef)
	if err != nil {
		return err
	}
	if resolved.OwnerID == nil || *resolved.OwnerID != user.ID {
		return ErrCommentForbidden
	}
	return withCreateTransactionMutex(s.createMu, func() error {
		return s.db.Transaction(func(tx *gorm.DB) error {
			target, err := s.repo.lockTarget(tx, resolved)
			if err != nil {
				return err
			}
			entry, err := s.repo.findRoot(tx, commentID)
			if isNotFound(err) {
				return ErrInvalidMark
			}
			if err != nil {
				return err
			}
			if entry.TargetID != target.ID || entry.RootID != nil || entry.Status != commentStatusActive {
				return ErrInvalidMark
			}
			if target.PinnedCommentID != nil && *target.PinnedCommentID == commentID {
				return nil
			}
			updated := tx.Model(&model.DiscussionTarget{}).Where("id = ?", target.ID).Update("pinned_comment_id", commentID)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrInvalidMark
			}
			return nil
		})
	})
}

func (s *Service) Unmark(user authctx.CurrentUser, targetRef TargetRef) error {
	if err := s.validateAuthor(user); err != nil {
		return err
	}
	resolved, err := s.resolveVisible(Viewer{UserID: &user.ID}, targetRef)
	if err != nil {
		return err
	}
	target, err := s.repo.findTarget(s.db, resolved)
	if isNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	storedResolved, err := s.resolveStoredTarget(Viewer{UserID: &user.ID}, target)
	if err != nil {
		return err
	}
	if storedResolved.OwnerID == nil || *storedResolved.OwnerID != user.ID {
		return ErrCommentForbidden
	}
	return withCreateTransactionMutex(s.createMu, func() error {
		return s.db.Transaction(func(tx *gorm.DB) error {
			lockedTarget, err := s.repo.lockTargetByID(tx, target.ID)
			if err != nil {
				return err
			}
			if !targetMatchesResolved(lockedTarget, storedResolved) {
				return ErrInvalidTargetResource
			}
			updated := tx.Model(&model.DiscussionTarget{}).Where("id = ?", lockedTarget.ID).Update("pinned_comment_id", gorm.Expr("NULL"))
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrInvalidMark
			}
			return nil
		})
	})
}

func (s *Service) resolveCommentMutation(viewer Viewer, commentID uuid.UUID) (model.CommentEntry, model.DiscussionTarget, ResolvedTarget, error) {
	entry, err := s.repo.findComment(s.db, commentID)
	if isNotFound(err) {
		return model.CommentEntry{}, model.DiscussionTarget{}, ResolvedTarget{}, ErrCommentNotFound
	}
	if err != nil {
		return model.CommentEntry{}, model.DiscussionTarget{}, ResolvedTarget{}, err
	}
	target, err := s.repo.findTargetByID(s.db, entry.TargetID)
	if isNotFound(err) {
		return model.CommentEntry{}, model.DiscussionTarget{}, ResolvedTarget{}, ErrCommentNotFound
	}
	if err != nil {
		return model.CommentEntry{}, model.DiscussionTarget{}, ResolvedTarget{}, err
	}
	resolved, err := s.resolveStoredTarget(viewer, target)
	if err != nil {
		return model.CommentEntry{}, model.DiscussionTarget{}, ResolvedTarget{}, err
	}
	return entry, target, resolved, nil
}

func targetMatchesResolved(target model.DiscussionTarget, resolved ResolvedTarget) bool {
	return target.Kind == resolved.Kind && target.ResourceID == resolved.ResourceID && target.ResourceKey == resolved.ResourceKey
}

func (s *Service) recomputeRootHotScore(tx *gorm.DB, rootID uuid.UUID, now time.Time) error {
	var root model.CommentEntry
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND root_id IS NULL", rootID).First(&root).Error; err != nil {
		return err
	}
	type aggregate struct {
		LikeCount  int
		ChildCount int
	}
	var totals aggregate
	if err := tx.Model(&model.CommentEntry{}).
		Select("COALESCE(SUM(like_count), 0) AS like_count, COUNT(*) AS child_count").
		Where("root_id = ? AND status IN ?", rootID, hotScoreStatuses).Scan(&totals).Error; err != nil {
		return err
	}
	score := HotScore(root.LikeCount, totals.LikeCount, totals.ChildCount, now.Sub(root.CreatedAt))
	updated := tx.Model(&model.CommentEntry{}).Where("id = ?", rootID).Update("hot_score", score)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrCommentNotFound
	}
	return nil
}
