package comment

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrAuthenticationRequired = errors.New("comment authentication required")
	ErrTargetNotVisible       = errors.New("comment target not visible")
	ErrInvalidContent         = errors.New("invalid comment content")
	ErrInvalidReply           = errors.New("invalid comment reply")
	ErrInvalidAttachment      = errors.New("invalid comment attachment")
	ErrInvalidMention         = errors.New("invalid comment mention")
	ErrInvalidListOptions     = errors.New("invalid comment list options")
	ErrCommentForbidden       = errors.New("comment operation forbidden")
	ErrCommentNotFound        = errors.New("comment not found")
	ErrInvalidMark            = errors.New("invalid comment mark")
	ErrCommentRateLimited     = errors.New("comment rate limited")
	ErrDuplicateComment       = errors.New("duplicate comment")
)

const (
	CommentStatusActive          = "active"
	CommentStatusAutoFolded      = "auto_folded"
	CommentStatusModeratorHidden = "moderator_hidden"
	commentStatusActive          = CommentStatusActive
)

type ExtensionWriter func(tx *gorm.DB, comment *model.CommentEntry) error

type Service struct {
	db         *gorm.DB
	registry   *TargetRegistry
	repo       repository
	createMu   *sync.Mutex
	now        func() time.Time
	checkAbuse bool
}

func NewService(db *gorm.DB, registry *TargetRegistry) *Service {
	return &Service{db: db, registry: registry, createMu: createTransactionMutex(db.Dialector.Name()), now: time.Now, checkAbuse: true}
}

func createTransactionMutex(dialect string) *sync.Mutex {
	if dialect == "sqlite" {
		return &sync.Mutex{}
	}
	return nil
}

func withCreateTransactionMutex(mutex *sync.Mutex, transaction func() error) error {
	if mutex == nil {
		return transaction()
	}
	mutex.Lock()
	defer mutex.Unlock()
	return transaction()
}

func (s *Service) Create(user authctx.CurrentUser, target TargetRef, input CreateCommentInput) (CommentDTO, error) {
	return s.CreateWithExtension(user, target, input, nil)
}

func (s *Service) CreateWithExtension(user authctx.CurrentUser, targetRef TargetRef, input CreateCommentInput, write ExtensionWriter) (CommentDTO, error) {
	if err := s.validateAuthor(user); err != nil {
		return CommentDTO{}, err
	}
	resolved, err := s.resolveVisible(Viewer{UserID: &user.ID}, targetRef)
	if err != nil {
		return CommentDTO{}, err
	}
	normalized, rendered, err := validateCommentContent(input.Content, input.AttachmentIDs)
	if err != nil {
		return CommentDTO{}, err
	}
	assets, err := s.validateAttachments(s.db, user.ID, input.AttachmentIDs)
	if err != nil {
		return CommentDTO{}, err
	}
	if err := s.validateMentions(s.db, normalized, input.Mentions); err != nil {
		return CommentDTO{}, err
	}

	var created model.CommentEntry
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
				created.RootID = &root.ID
				replyAuthorID = &reply.AuthorID
			}
			assets, err = s.validateAttachments(tx, user.ID, input.AttachmentIDs)
			if err != nil {
				return err
			}
			if err := s.validateMentions(tx, normalized, input.Mentions); err != nil {
				return err
			}

			if err := s.repo.createComment(tx, &created); err != nil {
				return fmt.Errorf("create comment: %w", err)
			}
			if err := createCommentRelations(tx, created.ID, input.Mentions, assets, resolved, normalized); err != nil {
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
			if err := s.notifyCreatedComment(tx, created, resolved, replyAuthorID, input.Mentions); err != nil {
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
	dto, err := s.loadCommentDTO(s.db, created.ID)
	if err != nil {
		return CommentDTO{}, err
	}
	dto.RenderedHTML = rendered
	return dto, nil
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

func (s *Service) List(user authctx.CurrentUser, targetRef TargetRef, input ListCommentsInput) (CommentListDTO, error) {
	if input.Page < 1 {
		return CommentListDTO{}, ErrInvalidListOptions
	}
	if input.Sort == "" {
		input.Sort = SortOldest
	}
	if input.Sort != SortOldest && input.Sort != SortNewest && input.Sort != SortHot {
		return CommentListDTO{}, ErrInvalidListOptions
	}
	viewer := Viewer{}
	if user.ID != uuid.Nil {
		viewer.UserID = &user.ID
	}
	resolved, err := s.resolveVisible(viewer, targetRef)
	if err != nil {
		return CommentListDTO{}, err
	}
	target, err := s.repo.findTarget(s.db, resolved)
	if isNotFound(err) {
		return CommentListDTO{Items: []CommentDTO{}, Page: input.Page, PerPage: pageSize}, nil
	}
	if err != nil {
		return CommentListDTO{}, fmt.Errorf("find discussion target: %w", err)
	}
	if target.ResourceID != resolved.ResourceID {
		if err := withCreateTransactionMutex(s.createMu, func() error {
			return s.db.Model(&model.DiscussionTarget{}).
				Where("id = ?", target.ID).
				Update("resource_id", resolved.ResourceID).Error
		}); err != nil {
			return CommentListDTO{}, err
		}
		target.ResourceID = resolved.ResourceID
	}

	visible := []string{commentStatusActive, "auto_folded"}
	var totalRoots, totalReplies int64
	if err := s.db.Model(&model.CommentEntry{}).
		Where("target_id = ? AND root_id IS NULL AND status IN ?", target.ID, visible).
		Count(&totalRoots).Error; err != nil {
		return CommentListDTO{}, err
	}
	if err := s.db.Model(&model.CommentEntry{}).
		Where("target_id = ? AND root_id IS NOT NULL AND status IN ?", target.ID, visible).
		Where("EXISTS (SELECT 1 FROM comment_entries AS roots WHERE roots.id = comment_entries.root_id AND roots.status IN ? AND roots.deleted_at IS NULL)", visible).
		Count(&totalReplies).Error; err != nil {
		return CommentListDTO{}, err
	}
	totalComments := totalRoots + totalReplies

	var marked *model.CommentEntry
	if target.PinnedCommentID != nil {
		var entry model.CommentEntry
		if err := s.db.Where("id = ? AND target_id = ? AND root_id IS NULL AND status IN ?", *target.PinnedCommentID, target.ID, visible).First(&entry).Error; err == nil {
			marked = &entry
		} else if !isNotFound(err) {
			return CommentListDTO{}, err
		}
	}

	query := s.db.Where("target_id = ? AND root_id IS NULL AND status IN ?", target.ID, visible)
	limit := pageSize
	offset := (input.Page - 1) * pageSize
	if marked != nil {
		query = query.Where("id <> ?", marked.ID)
		if input.Page == 1 {
			limit = pageSize - 1
			offset = 0
		} else {
			offset = pageSize - 1 + (input.Page-2)*pageSize
		}
	}
	switch input.Sort {
	case SortNewest:
		query = query.Order("floor_number DESC")
	case SortHot:
		query = query.Order("hot_score DESC").Order("floor_number ASC")
	default:
		query = query.Order("floor_number ASC")
	}
	var roots []model.CommentEntry
	if err := query.Offset(offset).Limit(limit).Find(&roots).Error; err != nil {
		return CommentListDTO{}, err
	}
	if marked != nil && input.Page == 1 {
		roots = append([]model.CommentEntry{*marked}, roots...)
	}

	rootIDs := make([]uuid.UUID, 0, len(roots))
	for _, root := range roots {
		rootIDs = append(rootIDs, root.ID)
	}
	children, err := s.previewChildren(s.db, rootIDs, visible)
	if err != nil {
		return CommentListDTO{}, err
	}
	allEntries := make([]model.CommentEntry, 0, len(roots)+len(children))
	allEntries = append(allEntries, roots...)
	allEntries = append(allEntries, children...)
	dtos, err := s.entryDTOs(s.db, allEntries, viewer.UserID)
	if err != nil {
		return CommentListDTO{}, err
	}
	childrenByRoot := make(map[uuid.UUID][]model.CommentEntry, len(roots))
	for _, child := range children {
		childrenByRoot[*child.RootID] = append(childrenByRoot[*child.RootID], child)
	}
	items := make([]CommentDTO, 0, len(roots))
	for _, root := range roots {
		dto := dtos[root.ID]
		dto.Marked = marked != nil && root.ID == marked.ID
		for _, child := range childrenByRoot[root.ID] {
			dto.Replies = append(dto.Replies, dtos[child.ID])
		}
		items = append(items, dto)
	}
	return CommentListDTO{
		Items:         items,
		Page:          input.Page,
		PerPage:       pageSize,
		TotalRoots:    int(totalRoots),
		TotalComments: int(totalComments),
		TotalReplies:  int(totalReplies),
	}, nil
}

func (s *Service) validateAuthor(user authctx.CurrentUser) error {
	if user.ID == uuid.Nil {
		return ErrAuthenticationRequired
	}
	var stored model.User
	if err := s.db.Select("uuid", "is_active").First(&stored, "uuid = ?", user.ID).Error; err != nil || !stored.IsActive {
		return ErrAuthenticationRequired
	}
	return nil
}

func (s *Service) resolveVisible(viewer Viewer, target TargetRef) (ResolvedTarget, error) {
	resolved, err := s.registry.Resolve(viewer, target)
	if err != nil {
		return ResolvedTarget{}, err
	}
	if !resolved.Visible {
		return ResolvedTarget{}, ErrTargetNotVisible
	}
	return resolved, nil
}

func (s *Service) resolveStoredTarget(viewer Viewer, target model.DiscussionTarget) (ResolvedTarget, error) {
	resolved, err := s.resolveVisible(viewer, TargetRef{Kind: target.Kind, ResourceID: target.ResourceID})
	if err != nil {
		return ResolvedTarget{}, err
	}
	if resolved.Kind != target.Kind || resolved.ResourceID != target.ResourceID || resolved.ResourceKey != target.ResourceKey {
		return ResolvedTarget{}, ErrInvalidTargetResource
	}
	return resolved, nil
}

func validateCommentContent(raw string, attachments []uuid.UUID) (string, string, error) {
	normalized := NormalizeContent(raw)
	if normalized == "" && len(attachments) == 0 {
		return "", "", ErrInvalidContent
	}
	if len([]rune(normalized)) > 2000 {
		return "", "", ErrInvalidContent
	}
	rendered, err := RenderCommentMarkdown(normalized)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrInvalidContent, err)
	}
	return normalized, rendered, nil
}

func (s *Service) validateAttachments(db *gorm.DB, userID uuid.UUID, ids []uuid.UUID) ([]model.MediaAsset, error) {
	if len(ids) > 4 {
		return nil, ErrInvalidAttachment
	}
	seen := make(map[uuid.UUID]struct{}, len(ids))
	orderedIDs := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id == uuid.Nil {
			return nil, ErrInvalidAttachment
		}
		if _, exists := seen[id]; exists {
			return nil, ErrInvalidAttachment
		}
		seen[id] = struct{}{}
		orderedIDs = append(orderedIDs, id)
	}
	orderedIDs = sortedUUIDs(orderedIDs)
	assetsByID := make(map[uuid.UUID]model.MediaAsset, len(ids))
	allowed := map[string]bool{"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true}
	const maxAttachmentSize = 10 * 1024 * 1024
	for _, id := range orderedIDs {
		var asset model.MediaAsset
		if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&asset, "id = ?", id).Error; err != nil || asset.UserID == nil || *asset.UserID != userID || !allowed[asset.ContentType] || asset.Size <= 0 || asset.Size > maxAttachmentSize || strings.TrimSpace(asset.Key) == "" || strings.TrimSpace(asset.URL) == "" {
			return nil, ErrInvalidAttachment
		}
		assetsByID[id] = asset
	}
	assets := make([]model.MediaAsset, 0, len(ids))
	for _, id := range ids {
		assets = append(assets, assetsByID[id])
	}
	return assets, nil
}

func (s *Service) validateMentions(db *gorm.DB, content string, mentions []MentionInput) error {
	if err := ValidateMentions(content, mentions); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidMention, err)
	}
	seen := make(map[uuid.UUID]struct{}, len(mentions))
	userIDs := make([]uuid.UUID, 0, len(mentions))
	for _, mention := range mentions {
		if _, exists := seen[mention.UserID]; exists {
			continue
		}
		seen[mention.UserID] = struct{}{}
		userIDs = append(userIDs, mention.UserID)
	}
	userIDs = sortedUUIDs(userIDs)
	var users []model.User
	if len(userIDs) > 0 {
		if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Select("uuid", "username").Where("uuid IN ? AND is_active = ?", userIDs, true).Order("uuid ASC").Find(&users).Error; err != nil {
			return ErrInvalidMention
		}
	}
	usernames := make(map[uuid.UUID]string, len(users))
	for _, user := range users {
		usernames[user.UUID] = user.Username
	}
	runes := []rune(content)
	for _, mention := range mentions {
		username, exists := usernames[mention.UserID]
		if !exists || string(runes[mention.Start:mention.End]) != "@"+username {
			return ErrInvalidMention
		}
	}
	return nil
}

func sortedUUIDs(ids []uuid.UUID) []uuid.UUID {
	result := append([]uuid.UUID(nil), ids...)
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result
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

func isMediaTarget(kind string) bool {
	return kind == TargetKindVideo || kind == TargetKindPodcastEpisode || kind == TargetKindMusicSong
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

func (s *Service) loadCommentDTO(db *gorm.DB, id uuid.UUID, viewerIDs ...*uuid.UUID) (CommentDTO, error) {
	entry, err := s.repo.findComment(db, id)
	if err != nil {
		return CommentDTO{}, err
	}
	dtos, err := s.entryDTOs(db, []model.CommentEntry{entry}, viewerIDs...)
	if err != nil {
		return CommentDTO{}, err
	}
	return dtos[id], nil
}

func (s *Service) previewChildren(db *gorm.DB, rootIDs []uuid.UUID, visible []string) ([]model.CommentEntry, error) {
	if len(rootIDs) == 0 {
		return []model.CommentEntry{}, nil
	}
	ranked := db.Model(&model.CommentEntry{}).
		Select("comment_entries.*, ROW_NUMBER() OVER (PARTITION BY root_id ORDER BY created_at ASC, id ASC) AS row_number").
		Where("root_id IN ? AND status IN ?", rootIDs, visible)
	var children []model.CommentEntry
	err := db.Table("(?) AS ranked", ranked).
		Where("row_number <= 3").
		Order("created_at ASC").Order("id ASC").
		Find(&children).Error
	return children, err
}

type commentAttachmentRow struct {
	CommentID   uuid.UUID
	ID          uuid.UUID
	URL         string
	ContentType string
	Position    int
}

func (s *Service) entryDTOs(db *gorm.DB, entries []model.CommentEntry, viewerIDs ...*uuid.UUID) (map[uuid.UUID]CommentDTO, error) {
	dtos := make(map[uuid.UUID]CommentDTO, len(entries))
	ids := make([]uuid.UUID, 0, len(entries))
	for _, entry := range entries {
		rendered, err := RenderCommentMarkdown(entry.Content)
		if err != nil {
			return nil, err
		}
		dtos[entry.ID] = CommentDTO{
			ID:           entry.ID,
			AuthorID:     entry.AuthorID,
			RootID:       entry.RootID,
			ReplyToID:    entry.ReplyToID,
			FloorNumber:  entry.FloorNumber,
			Content:      entry.Content,
			RenderedHTML: rendered,
			Status:       entry.Status,
			EditedAt:     entry.EditedAt,
			LikeCount:    entry.LikeCount,
			ReplyCount:   entry.ReplyCount,
			HotScore:     entry.HotScore,
			CreatedAt:    entry.CreatedAt,
			Mentions:     []MentionDTO{},
			Attachments:  []AttachmentDTO{},
			TimeAnchors:  []TimeAnchorDTO{},
			Replies:      []CommentDTO{},
		}
		ids = append(ids, entry.ID)
	}
	if len(ids) == 0 {
		return dtos, nil
	}
	if len(viewerIDs) > 0 && viewerIDs[0] != nil {
		var likedIDs []uuid.UUID
		if err := db.Model(&model.CommentLike{}).
			Where("comment_id IN ? AND user_id = ?", ids, *viewerIDs[0]).
			Pluck("comment_id", &likedIDs).Error; err != nil {
			return nil, err
		}
		for _, id := range likedIDs {
			dto := dtos[id]
			dto.Liked = true
			dtos[id] = dto
		}
	}
	var mentions []model.CommentMention
	if err := db.Where("comment_id IN ?", ids).Order("comment_id ASC").Order("start_offset ASC").Find(&mentions).Error; err != nil {
		return nil, err
	}
	for _, mention := range mentions {
		dto := dtos[mention.CommentID]
		dto.Mentions = append(dto.Mentions, MentionDTO{UserID: mention.UserID, Start: mention.StartOffset, End: mention.EndOffset})
		dtos[mention.CommentID] = dto
	}
	var attachments []commentAttachmentRow
	if err := db.Table("comment_attachments AS ca").
		Select("ca.comment_id, ma.id, ma.url, ma.content_type, ca.position").
		Joins("JOIN media_assets AS ma ON ma.id = ca.media_asset_id").
		Where("ca.comment_id IN ? AND ca.deleted_at IS NULL AND ma.deleted_at IS NULL", ids).
		Order("ca.comment_id ASC").Order("ca.position ASC").Scan(&attachments).Error; err != nil {
		return nil, err
	}
	for _, attachment := range attachments {
		dto := dtos[attachment.CommentID]
		dto.Attachments = append(dto.Attachments, AttachmentDTO{
			ID: attachment.ID, URL: attachment.URL, ContentType: attachment.ContentType, Position: attachment.Position,
		})
		dtos[attachment.CommentID] = dto
	}
	var anchors []model.CommentTimeAnchor
	if err := db.Where("comment_id IN ?", ids).Order("comment_id ASC").Order("start_offset ASC").Find(&anchors).Error; err != nil {
		return nil, err
	}
	for _, anchor := range anchors {
		dto := dtos[anchor.CommentID]
		dto.TimeAnchors = append(dto.TimeAnchors, TimeAnchorDTO{Start: anchor.StartOffset, End: anchor.EndOffset, Seconds: anchor.Seconds})
		dtos[anchor.CommentID] = dto
	}
	return dtos, nil
}
