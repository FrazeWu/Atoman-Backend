package comment

import (
	"crypto/sha256"
	"fmt"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/reference"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) Create(user authctx.CurrentUser, target TargetRef, input CreateCommentInput) (CommentDTO, error) {
	return s.CreateWithExtension(user, target, input, nil)
}

func (s *Service) CreateWithExtension(user authctx.CurrentUser, targetRef TargetRef, input CreateCommentInput, write ExtensionWriter) (CommentDTO, error) {
	if err := s.validateAuthor(user); err != nil {
		return CommentDTO{}, err
	}
	resolved, err := s.resolveVisible(viewerFromUser(user), targetRef)
	if err != nil {
		return CommentDTO{}, err
	}
	if resolved.Locked {
		return CommentDTO{}, ErrTargetLocked
	}
	if write == nil && input.ReplyToID == nil && (resolved.Kind == TargetKindTimelineEvent || resolved.Kind == TargetKindTimelinePerson) {
		return CommentDTO{}, ErrInvalidContent
	}
	normalized, rendered, err := validateCommentContent(input.Content, input.AttachmentIDs)
	if err != nil {
		return CommentDTO{}, err
	}
	assets, err := s.validateAttachments(s.db, user.ID, input.AttachmentIDs)
	if err != nil {
		return CommentDTO{}, err
	}
	var created model.CommentEntry
	var mentions []MentionInput
	contentHash := ContentHash(normalized, input.AttachmentIDs)
	runTransaction := func() error {
		return s.db.Transaction(func(tx *gorm.DB) error {
			if err := s.repo.lockUser(tx, user.ID); err != nil {
				return ErrAuthenticationRequired
			}
			target, err := s.repo.lockTarget(tx, resolved)
			if err != nil {
				return fmt.Errorf("lock discussion target: %w", err)
			}
			if err := s.validateCreateTargetTx(tx, resolved); err != nil {
				return err
			}
			if resolved.Kind == TargetKindForumTopic && s.forumPolicy != nil {
				if err := s.forumPolicy.CheckCreateComment(tx, user, resolved.ResourceID, normalized); err != nil {
					return err
				}
			}
			createNow := s.now()
			if s.checkAbuse {
				if err := s.checkCreateAbuse(tx, user.ID, target.ID, contentHash, createNow); err != nil {
					return err
				}
			}

			created = model.CommentEntry{
				TargetID:    target.ID,
				AuthorID:    user.ID,
				Content:     normalized,
				ContentHash: contentHash,
				Status:      commentStatusActive,
			}
			created.CreatedAt = createNow
			isRoot := input.ReplyToID == nil
			var replyAuthorID *uuid.UUID
			if isRoot {
				floor := target.NextFloor
				created.FloorNumber = &floor
			} else {
				reply, root, err := s.repo.lockReplyHierarchy(tx, target.ID, *input.ReplyToID)
				if err != nil || reply.Status != commentStatusActive {
					return ErrInvalidReply
				}
				if root.TargetID != target.ID || root.RootID != nil || root.Status != commentStatusActive {
					return ErrInvalidReply
				}
				created.ReplyToID = input.ReplyToID
				created.ReplyToAuthorID = &reply.AuthorID
				created.RootID = &root.ID
				replyAuthorID = &reply.AuthorID
			}
			assets, err = s.validateAttachments(tx, user.ID, input.AttachmentIDs)
			if err != nil {
				return err
			}
			if err := s.repo.createComment(tx, &created); err != nil {
				return fmt.Errorf("create comment: %w", err)
			}
			mentions, err = s.replaceContentReferences(tx, created, resolved, normalized)
			if err != nil {
				return err
			}
			if err := createCommentRelations(tx, created.ID, mentions, assets, resolved, normalized); err != nil {
				return err
			}
			updates := map[string]any{"comment_count": gorm.Expr("comment_count + 1")}
			if isRoot {
				updates["root_count"] = gorm.Expr("root_count + 1")
				updates["next_floor"] = gorm.Expr("next_floor + 1")
			} else {
				result := tx.Model(&model.CommentEntry{}).
					Where("id = ? AND target_id = ? AND root_id IS NULL AND status = ?", created.RootID, target.ID, commentStatusActive).
					UpdateColumn("reply_count", gorm.Expr("reply_count + 1"))
				if result.Error != nil {
					return fmt.Errorf("update root reply count: %w", result.Error)
				}
				if result.RowsAffected != 1 {
					return ErrInvalidReply
				}
			}
			if err := tx.Model(&model.DiscussionTarget{}).Where("id = ?", target.ID).Updates(updates).Error; err != nil {
				return fmt.Errorf("update discussion target counters: %w", err)
			}
			if write != nil {
				if err := write(tx, &created); err != nil {
					return err
				}
			}
			if err := s.notifyCreatedComment(tx, created, resolved, replyAuthorID, mentions); err != nil {
				return err
			}
			if !isRoot {
				if err := s.recomputeRootHotScore(tx, *created.RootID, createNow); err != nil {
					return err
				}
			}
			record := model.CommentPublishRecord{AuthorID: user.ID, TargetID: target.ID, ContentHash: contentHash}
			record.CreatedAt = createNow
			if err := tx.Create(&record).Error; err != nil {
				return fmt.Errorf("create comment publish record: %w", err)
			}
			return nil
		})
	}
	err = withCreateTransactionMutex(s.createMu, runTransaction)
	if err != nil {
		return CommentDTO{}, err
	}
	if resolved.Kind == TargetKindForumTopic && s.forumPolicy != nil {
		s.forumPolicy.EvaluateTrust(user.ID)
	}
	dto, err := s.loadCommentDTO(s.db, created.ID)
	if err != nil {
		return CommentDTO{}, err
	}
	dto.RenderedHTML = rendered
	return dto, nil
}

func (s *Service) replaceContentReferences(tx *gorm.DB, entry model.CommentEntry, resolved ResolvedTarget, content string) ([]MentionInput, error) {
	meta := notificationMeta(entry, resolved)
	items, err := s.references.ReplacePublished(tx, reference.Source{
		Type: "comment", ID: entry.ID, ActorID: entry.AuthorID, Audience: reference.AudiencePublic,
		MentionNotificationType: NotificationTypeMention, NotificationSourceType: "comment_event",
		SuppressMentionNotifications: true, Meta: map[string]interface{}(meta),
	}, []reference.Field{{Name: "content", Content: content}})
	if err != nil {
		return nil, err
	}
	mentions := make([]MentionInput, 0)
	for _, item := range items {
		if item.TargetType == reference.TargetTypeUser {
			mentions = append(mentions, MentionInput{UserID: item.TargetID, Start: item.Start, End: item.End})
		}
	}
	return mentions, nil
}

func (s *Service) validateCreateTargetTx(tx *gorm.DB, resolved ResolvedTarget) error {
	if resolved.Kind != TargetKindForumTopic {
		return nil
	}
	var topic model.ForumTopic
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "closed").First(&topic, "id = ?", resolved.ResourceID).Error; err != nil {
		return ErrTargetNotFound
	}
	if topic.Closed {
		return ErrTargetLocked
	}
	return nil
}

func (s *Service) checkCreateAbuse(tx *gorm.DB, userID, targetID uuid.UUID, contentHash string, now time.Time) error {
	if err := tx.Unscoped().Where("author_id = ? AND created_at <= ?", userID, now.Add(-5*time.Minute)).Delete(&model.CommentPublishRecord{}).Error; err != nil {
		return err
	}
	var recentCount int64
	if err := tx.Model(&model.CommentPublishRecord{}).
		Where("author_id = ? AND created_at > ?", userID, now.Add(-time.Minute)).Count(&recentCount).Error; err != nil {
		return err
	}
	if recentCount >= 5 {
		return ErrCommentRateLimited
	}
	var duplicateCount int64
	if err := tx.Model(&model.CommentPublishRecord{}).
		Where("author_id = ? AND target_id = ? AND content_hash = ? AND created_at > ?", userID, targetID, contentHash, now.Add(-5*time.Minute)).
		Count(&duplicateCount).Error; err != nil {
		return err
	}
	if duplicateCount > 0 {
		return ErrDuplicateComment
	}
	return nil
}

func createCommentRelations(tx *gorm.DB, commentID uuid.UUID, mentions []MentionInput, assets []model.MediaAsset, resolved ResolvedTarget, content string) error {
	for _, mention := range mentions {
		relation := model.CommentMention{CommentID: commentID, UserID: mention.UserID, StartOffset: mention.Start, EndOffset: mention.End}
		if err := tx.Create(&relation).Error; err != nil {
			return fmt.Errorf("create comment mention: %w", err)
		}
	}
	for position, asset := range assets {
		relation := model.CommentAttachment{CommentID: commentID, MediaAssetID: asset.ID, Position: position}
		if err := tx.Create(&relation).Error; err != nil {
			return fmt.Errorf("create comment attachment: %w", err)
		}
	}
	if isMediaTarget(resolved.Kind) {
		for _, anchor := range ParseTimeAnchors(content, resolved.DurationSec) {
			relation := model.CommentTimeAnchor{CommentID: commentID, StartOffset: anchor.Start, EndOffset: anchor.End, Seconds: anchor.Seconds}
			if err := tx.Create(&relation).Error; err != nil {
				return fmt.Errorf("create comment time anchor: %w", err)
			}
		}
	}
	return nil
}

func ContentHash(content string, attachments []uuid.UUID) string {
	hash := sha256.New()
	hash.Write([]byte(NormalizeContent(content)))
	for _, id := range attachments {
		hash.Write([]byte{0})
		hash.Write([]byte(id.String()))
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}
