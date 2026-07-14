package comment

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/audit"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var hotScoreStatuses = []string{commentStatusActive, "auto_folded"}

var (
	ErrInvalidReport     = errors.New("invalid comment report")
	ErrInvalidModeration = errors.New("invalid comment moderation")
)

const (
	ReportReasonSpam           = "spam"
	ReportReasonHarassment     = "harassment"
	ReportReasonHate           = "hate"
	ReportReasonSexual         = "sexual"
	ReportReasonViolence       = "violence"
	ReportReasonMisinformation = "misinformation"
	ReportReasonOther          = "other"
	ReportStatusPending        = "pending"
	ReportStatusUpheld         = "upheld"
	ReportStatusRejected       = "rejected"

	ModerationRestore      = "restore"
	ModerationHide         = "hide"
	ModerationDelete       = "delete"
	ModerationUpholdReport = "uphold_report"
	ModerationRejectReport = "reject_report"
)

var validReportReasons = map[string]bool{
	ReportReasonSpam: true, ReportReasonHarassment: true, ReportReasonHate: true,
	ReportReasonSexual: true, ReportReasonViolence: true, ReportReasonMisinformation: true, ReportReasonOther: true,
}

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
			if err := s.notifyNewEditMentions(tx, entry, resolved, user.ID, input.Mentions); err != nil {
				return err
			}
			now := s.now()
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

			return s.deleteCommentLocked(tx, target, entry)
		})
	})
}

func (s *Service) deleteCommentLocked(tx *gorm.DB, target model.DiscussionTarget, entry model.CommentEntry) error {
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
	updates := map[string]any{"comment_count": gorm.Expr("comment_count - ?", visibleDeleteCount), "root_count": gorm.Expr("root_count - ?", rootDelta)}
	if target.PinnedCommentID != nil && *target.PinnedCommentID == entry.ID {
		updates["pinned_comment_id"] = gorm.Expr("NULL")
	}
	counter := tx.Model(&model.DiscussionTarget{}).Where("id = ? AND comment_count >= ? AND root_count >= ?", target.ID, visibleDeleteCount, rootDelta).Updates(updates)
	if counter.Error != nil || counter.RowsAffected != 1 {
		if counter.Error != nil {
			return counter.Error
		}
		return fmt.Errorf("delete comment counters: inconsistent target")
	}
	if entry.RootID != nil {
		var replyCount int64
		if err := tx.Model(&model.CommentEntry{}).Where("root_id = ? AND status IN ?", *entry.RootID, hotScoreStatuses).Count(&replyCount).Error; err != nil {
			return err
		}
		updated := tx.Model(&model.CommentEntry{}).Where("id = ? AND root_id IS NULL", *entry.RootID).Update("reply_count", replyCount)
		if updated.Error != nil || updated.RowsAffected != 1 {
			if updated.Error != nil {
				return updated.Error
			}
			return ErrCommentNotFound
		}
		return s.recomputeRootHotScore(tx, *entry.RootID, s.now())
	}
	return nil
}

func (s *Service) Report(user authctx.CurrentUser, commentID uuid.UUID, input ReportInput) error {
	if err := s.validateAuthor(user); err != nil {
		return err
	}
	input.Reason = strings.TrimSpace(input.Reason)
	input.Note = strings.TrimSpace(input.Note)
	if !validReportReasons[input.Reason] || input.Reason == ReportReasonOther && input.Note == "" {
		return ErrInvalidReport
	}
	located, _, resolved, err := s.resolveCommentMutation(Viewer{UserID: &user.ID}, commentID)
	if err != nil {
		return err
	}
	return withCreateTransactionMutex(s.createMu, func() error {
		return s.db.Transaction(func(tx *gorm.DB) error {
			h, err := s.repo.lockCommentHierarchy(tx, located)
			if err != nil {
				return ErrCommentNotFound
			}
			if !targetMatchesResolved(h.Target, resolved) {
				return ErrInvalidTargetResource
			}
			if !isVisibleCommentStatus(h.Entry.Status) {
				return ErrCommentNotFound
			}
			if h.Entry.AuthorID == user.ID {
				return ErrCommentForbidden
			}
			var existing model.CommentReport
			if err := tx.Unscoped().Where("comment_id = ? AND reporter_id = ?", commentID, user.ID).First(&existing).Error; err == nil {
				return nil
			} else if !isNotFound(err) {
				return err
			}
			report := model.CommentReport{CommentID: commentID, ReporterID: user.ID, Reason: input.Reason, Note: input.Note, Status: ReportStatusPending}
			if err := tx.Create(&report).Error; err != nil {
				return err
			}
			return s.recalibrateReports(tx, h.Entry)
		})
	})
}

func (s *Service) recalibrateReports(tx *gorm.DB, entry model.CommentEntry) error {
	var reports []model.CommentReport
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("comment_id = ?", entry.ID).Order("id ASC").Find(&reports).Error; err != nil {
		return err
	}
	count := 0
	for _, report := range reports {
		if report.Status == ReportStatusPending || report.Status == ReportStatusUpheld {
			count++
		}
	}
	status := entry.Status
	if count >= 4 && status == CommentStatusActive {
		status = CommentStatusAutoFolded
	}
	if count < 4 && status == CommentStatusAutoFolded {
		status = CommentStatusActive
	}
	return tx.Model(&model.CommentEntry{}).Where("id = ?", entry.ID).Updates(map[string]any{"report_count": count, "status": status}).Error
}

func (s *Service) Moderate(user authctx.CurrentUser, commentID uuid.UUID, input ModerateInput) error {
	if err := s.validateAuthor(user); err != nil {
		return err
	}
	if !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) {
		return ErrCommentForbidden
	}
	valid := input.Action == ModerationRestore || input.Action == ModerationHide || input.Action == ModerationDelete || input.Action == ModerationUpholdReport || input.Action == ModerationRejectReport
	if !valid {
		return ErrInvalidModeration
	}
	located, _, resolved, err := s.resolveCommentMutation(Viewer{UserID: &user.ID}, commentID)
	if err != nil {
		return err
	}
	return withCreateTransactionMutex(s.createMu, func() error {
		return s.db.Transaction(func(tx *gorm.DB) error {
			h, err := s.repo.lockCommentHierarchy(tx, located)
			if err != nil {
				return ErrCommentNotFound
			}
			if !targetMatchesResolved(h.Target, resolved) {
				return ErrInvalidTargetResource
			}
			switch input.Action {
			case ModerationRestore:
				if err := tx.Model(&model.CommentEntry{}).Where("id = ?", commentID).Update("status", CommentStatusActive).Error; err != nil {
					return err
				}
			case ModerationHide:
				if err := tx.Model(&model.CommentEntry{}).Where("id = ?", commentID).Update("status", CommentStatusModeratorHidden).Error; err != nil {
					return err
				}
			case ModerationDelete:
				if err := s.deleteCommentLocked(tx, h.Target, h.Entry); err != nil {
					return err
				}
			default:
				if input.ReportID == nil {
					return ErrInvalidModeration
				}
				var report model.CommentReport
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND comment_id = ? AND status = ?", *input.ReportID, commentID, ReportStatusPending).First(&report).Error; err != nil {
					return ErrInvalidModeration
				}
				now := s.now()
				status := ReportStatusUpheld
				if input.Action == ModerationRejectReport {
					status = ReportStatusRejected
				}
				if err := tx.Model(&report).Updates(map[string]any{"status": status, "reviewer_id": user.ID, "reviewed_at": now}).Error; err != nil {
					return err
				}
				if err := s.recalibrateReports(tx, h.Entry); err != nil {
					return err
				}
			}
			return audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "comment.moderate." + input.Action, EntityType: "comment", EntityID: &commentID, Reason: strings.TrimSpace(input.Reason)})
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
	if err := tx.Unscoped().Where("source_id IN ? AND source_type LIKE ?", ids, "comment_%").Delete(&model.Notification{}).Error; err != nil {
		return fmt.Errorf("delete comment notifications: %w", err)
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
			changed := false
			if liked && isNotFound(err) {
				if err := tx.Create(&model.CommentLike{CommentID: commentID, UserID: user.ID}).Error; err != nil {
					return err
				}
				changed = true
			} else if !liked && err == nil {
				if err := tx.Delete(&existing).Error; err != nil {
					return err
				}
				changed = true
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
			if changed {
				if err := s.syncLikeNotification(tx, hierarchy.Entry, user.ID, count, liked); err != nil {
					return err
				}
			}
			return s.recomputeRootHotScore(tx, hierarchy.Root.ID, s.now())
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
			return s.notifyMarkedComment(tx, entry, resolved, user.ID)
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
	return target.Kind == resolved.Kind && target.ResourceKey == resolved.ResourceKey
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
