package comment

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrAuthenticationRequired = errors.New("comment authentication required")
	ErrTargetNotVisible       = errors.New("comment target not visible")
	ErrInvalidContent         = errors.New("invalid comment content")
	ErrInvalidReply           = errors.New("invalid comment reply")
	ErrInvalidAttachment      = errors.New("invalid comment attachment")
	ErrInvalidMention         = errors.New("invalid comment mention")
	ErrInvalidListOptions     = errors.New("invalid comment list options")
)

const commentStatusActive = "active"

type ExtensionWriter func(tx *gorm.DB, comment *model.CommentEntry) error

type Service struct {
	db       *gorm.DB
	registry *TargetRegistry
	repo     repository
	createMu sync.Mutex
}

func NewService(db *gorm.DB, registry *TargetRegistry) *Service {
	return &Service{db: db, registry: registry}
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
	assets, err := s.validateAttachments(user.ID, input.AttachmentIDs)
	if err != nil {
		return CommentDTO{}, err
	}
	if err := s.validateMentions(normalized, input.Mentions); err != nil {
		return CommentDTO{}, err
	}

	// SQLite ignores FOR UPDATE, so serialize creates within one service as well.
	// PostgreSQL correctness still comes from the target row lock and floor index.
	s.createMu.Lock()
	defer s.createMu.Unlock()

	var created model.CommentEntry
	err = s.db.Transaction(func(tx *gorm.DB) error {
		target, err := s.repo.lockTarget(tx, resolved)
		if err != nil {
			return fmt.Errorf("lock discussion target: %w", err)
		}

		created = model.CommentEntry{
			TargetID:    target.ID,
			AuthorID:    user.ID,
			Content:     normalized,
			ContentHash: ContentHash(normalized, input.AttachmentIDs),
			Status:      commentStatusActive,
		}
		isRoot := input.ReplyToID == nil
		if isRoot {
			floor := target.NextFloor
			created.FloorNumber = &floor
		} else {
			reply, err := s.repo.findReply(tx, *input.ReplyToID)
			if err != nil || reply.TargetID != target.ID || reply.Status != commentStatusActive {
				return ErrInvalidReply
			}
			created.ReplyToID = input.ReplyToID
			if reply.RootID == nil {
				created.RootID = &reply.ID
			} else {
				created.RootID = reply.RootID
			}
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
			if err := tx.Model(&model.CommentEntry{}).Where("id = ?", created.RootID).
				UpdateColumn("reply_count", gorm.Expr("reply_count + 1")).Error; err != nil {
				return fmt.Errorf("update root reply count: %w", err)
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
		return nil
	})
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

func (s *Service) List(user authctx.CurrentUser, targetRef TargetRef, input ListCommentsInput) (CommentListDTO, error) {
	if input.Page < 1 {
		return CommentListDTO{}, ErrInvalidListOptions
	}
	if input.Sort == "" {
		input.Sort = SortOldest
	}
	if input.Sort != SortOldest && input.Sort != SortLatest && input.Sort != SortHot {
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

	visible := []string{commentStatusActive, "auto_folded"}
	base := s.db.Model(&model.CommentEntry{}).Where("target_id = ? AND status IN ?", target.ID, visible)
	var totalComments, totalRoots int64
	if err := base.Count(&totalComments).Error; err != nil {
		return CommentListDTO{}, err
	}
	if err := s.db.Model(&model.CommentEntry{}).
		Where("target_id = ? AND root_id IS NULL AND status IN ?", target.ID, visible).
		Count(&totalRoots).Error; err != nil {
		return CommentListDTO{}, err
	}

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
	case SortLatest:
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

	items := make([]CommentDTO, 0, len(roots))
	for _, root := range roots {
		dto, err := s.entryDTO(s.db, root)
		if err != nil {
			return CommentListDTO{}, err
		}
		dto.Marked = marked != nil && root.ID == marked.ID
		var children []model.CommentEntry
		if err := s.db.Where("root_id = ? AND status IN ?", root.ID, visible).
			Order("created_at ASC").Order("id ASC").Limit(3).Find(&children).Error; err != nil {
			return CommentListDTO{}, err
		}
		for _, child := range children {
			childDTO, err := s.entryDTO(s.db, child)
			if err != nil {
				return CommentListDTO{}, err
			}
			dto.Replies = append(dto.Replies, childDTO)
		}
		items = append(items, dto)
	}
	return CommentListDTO{
		Items:         items,
		Page:          input.Page,
		PerPage:       pageSize,
		TotalRoots:    int(totalRoots),
		TotalComments: int(totalComments),
		TotalReplies:  int(totalComments - totalRoots),
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

func (s *Service) validateAttachments(userID uuid.UUID, ids []uuid.UUID) ([]model.MediaAsset, error) {
	if len(ids) > 4 {
		return nil, ErrInvalidAttachment
	}
	seen := make(map[uuid.UUID]struct{}, len(ids))
	assets := make([]model.MediaAsset, 0, len(ids))
	allowed := map[string]bool{"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true}
	for _, id := range ids {
		if id == uuid.Nil {
			return nil, ErrInvalidAttachment
		}
		if _, exists := seen[id]; exists {
			return nil, ErrInvalidAttachment
		}
		seen[id] = struct{}{}
		var asset model.MediaAsset
		if err := s.db.First(&asset, "id = ?", id).Error; err != nil || asset.UserID == nil || *asset.UserID != userID || !allowed[asset.ContentType] || strings.TrimSpace(asset.Key) == "" || strings.TrimSpace(asset.URL) == "" {
			return nil, ErrInvalidAttachment
		}
		assets = append(assets, asset)
	}
	return assets, nil
}

func (s *Service) validateMentions(content string, mentions []MentionInput) error {
	if err := ValidateMentions(content, mentions); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidMention, err)
	}
	seen := make(map[uuid.UUID]struct{}, len(mentions))
	for _, mention := range mentions {
		if _, exists := seen[mention.UserID]; exists {
			continue
		}
		seen[mention.UserID] = struct{}{}
		var user model.User
		if err := s.db.Select("uuid", "is_active").First(&user, "uuid = ?", mention.UserID).Error; err != nil || !user.IsActive {
			return ErrInvalidMention
		}
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

func (s *Service) loadCommentDTO(db *gorm.DB, id uuid.UUID) (CommentDTO, error) {
	entry, err := s.repo.findComment(db, id)
	if err != nil {
		return CommentDTO{}, err
	}
	return s.entryDTO(db, entry)
}

func (s *Service) entryDTO(db *gorm.DB, entry model.CommentEntry) (CommentDTO, error) {
	rendered, err := RenderCommentMarkdown(entry.Content)
	if err != nil {
		return CommentDTO{}, err
	}
	dto := CommentDTO{
		ID:           entry.ID,
		AuthorID:     entry.AuthorID,
		RootID:       entry.RootID,
		ReplyToID:    entry.ReplyToID,
		FloorNumber:  entry.FloorNumber,
		Content:      entry.Content,
		ContentHash:  entry.ContentHash,
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
	var mentions []model.CommentMention
	if err := db.Where("comment_id = ?", entry.ID).Order("start_offset ASC").Find(&mentions).Error; err != nil {
		return CommentDTO{}, err
	}
	for _, mention := range mentions {
		dto.Mentions = append(dto.Mentions, MentionDTO{UserID: mention.UserID, Start: mention.StartOffset, End: mention.EndOffset})
	}
	type attachmentRow struct {
		ID          uuid.UUID
		URL         string
		ContentType string
		Position    int
	}
	var attachments []attachmentRow
	if err := db.Table("comment_attachments AS ca").
		Select("ma.id, ma.url, ma.content_type, ca.position").
		Joins("JOIN media_assets AS ma ON ma.id = ca.media_asset_id").
		Where("ca.comment_id = ?", entry.ID).Order("ca.position ASC").Scan(&attachments).Error; err != nil {
		return CommentDTO{}, err
	}
	for _, attachment := range attachments {
		dto.Attachments = append(dto.Attachments, AttachmentDTO(attachment))
	}
	var anchors []model.CommentTimeAnchor
	if err := db.Where("comment_id = ?", entry.ID).Order("start_offset ASC").Find(&anchors).Error; err != nil {
		return CommentDTO{}, err
	}
	for _, anchor := range anchors {
		dto.TimeAnchors = append(dto.TimeAnchors, TimeAnchorDTO{Start: anchor.StartOffset, End: anchor.EndOffset, Seconds: anchor.Seconds})
	}
	return dto, nil
}
