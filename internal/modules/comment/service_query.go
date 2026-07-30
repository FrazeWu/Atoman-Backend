package comment

import (
	"fmt"
	"math"

	"atoman/internal/model"
	"atoman/internal/modules/reference"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) List(user authctx.CurrentUser, targetRef TargetRef, input ListCommentsInput) (CommentListDTO, error) {
	if input.Page < 1 {
		return CommentListDTO{}, ErrInvalidListOptions
	}
	effectivePageSize := input.PageSize
	if effectivePageSize == 0 {
		effectivePageSize = pageSize
	}
	if effectivePageSize < 1 || effectivePageSize > pageSize {
		return CommentListDTO{}, ErrInvalidListOptions
	}
	offset, _, err := safePagination(input.Page, effectivePageSize)
	if err != nil {
		return CommentListDTO{}, err
	}
	if input.Sort == "" {
		input.Sort = SortOldest
	}
	if input.Sort != SortOldest && input.Sort != SortNewest && input.Sort != SortHot {
		return CommentListDTO{}, ErrInvalidListOptions
	}
	viewer := viewerFromUser(user)
	resolved, err := s.resolveVisible(viewer, targetRef)
	if err != nil {
		return CommentListDTO{}, err
	}
	target, err := s.repo.findTarget(s.db, resolved)
	if isNotFound(err) {
		return CommentListDTO{Items: []CommentDTO{}, Page: input.Page, PerPage: effectivePageSize, Target: targetSummary(resolved, model.DiscussionTarget{}, viewer.UserID)}, nil
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
	limit := effectivePageSize
	if marked != nil {
		query = query.Where("id <> ?", marked.ID)
		if input.Page == 1 {
			limit = effectivePageSize - 1
			offset = 0
		} else {
			offset--
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
		PerPage:       effectivePageSize,
		TotalRoots:    int(totalRoots),
		TotalComments: int(totalComments),
		TotalReplies:  int(totalReplies),
		Target:        targetSummary(resolved, target, viewer.UserID),
	}, nil
}

// Get returns one comment with the same visibility and relation hydration used by list responses.
func (s *Service) Get(viewer Viewer, commentID uuid.UUID) (CommentDTO, error) {
	entry, err := s.repo.findComment(s.db, commentID)
	if err != nil {
		return CommentDTO{}, ErrCommentNotFound
	}
	target, err := s.repo.findTargetByID(s.db, entry.TargetID)
	if err != nil {
		return CommentDTO{}, ErrCommentNotFound
	}
	if _, err := s.resolveStoredTarget(viewer, target); err != nil {
		return CommentDTO{}, err
	}
	visible := []string{commentStatusActive, CommentStatusAutoFolded}
	if entry.Status != commentStatusActive && entry.Status != CommentStatusAutoFolded {
		return CommentDTO{}, ErrCommentNotFound
	}
	entries := []model.CommentEntry{entry}
	var children []model.CommentEntry
	if entry.RootID == nil {
		children, err = s.previewChildren(s.db, []uuid.UUID{entry.ID}, visible)
		if err != nil {
			return CommentDTO{}, err
		}
		entries = append(entries, children...)
	}
	dtos, err := s.entryDTOs(s.db, entries, viewer.UserID)
	if err != nil {
		return CommentDTO{}, err
	}
	dto := dtos[entry.ID]
	dto.Marked = target.PinnedCommentID != nil && *target.PinnedCommentID == entry.ID
	for _, child := range children {
		dto.Replies = append(dto.Replies, dtos[child.ID])
	}
	return dto, nil
}

func targetSummary(resolved ResolvedTarget, target model.DiscussionTarget, viewerID *uuid.UUID) TargetSummaryDTO {
	canMark := viewerID != nil && resolved.OwnerID != nil && *viewerID == *resolved.OwnerID
	return TargetSummaryDTO{
		Kind: resolved.Kind, ResourceID: resolved.ResourceID, MarkLabel: resolved.MarkLabel,
		CanMark: canMark, MarkedCommentID: target.PinnedCommentID,
		CommentCount: target.CommentCount, RootCount: target.RootCount,
	}
}

func (s *Service) ListReplies(viewer Viewer, rootID uuid.UUID, page, pageSize int) (ReplyListDTO, error) {
	if rootID == uuid.Nil || page < 1 || pageSize < 1 || pageSize > 50 {
		return ReplyListDTO{}, ErrInvalidListOptions
	}
	offset, end, err := safePagination(page, pageSize)
	if err != nil {
		return ReplyListDTO{}, err
	}
	root, err := s.repo.findComment(s.db, rootID)
	if isNotFound(err) {
		return ReplyListDTO{}, ErrCommentNotFound
	}
	if err != nil {
		return ReplyListDTO{}, err
	}
	if root.RootID != nil {
		return ReplyListDTO{}, ErrInvalidReply
	}
	target, err := s.repo.findTargetByID(s.db, root.TargetID)
	if isNotFound(err) {
		return ReplyListDTO{}, ErrCommentNotFound
	}
	if err != nil {
		return ReplyListDTO{}, err
	}
	if _, err := s.resolveStoredTarget(viewer, target); err != nil {
		return ReplyListDTO{}, err
	}
	if !isVisibleCommentStatus(root.Status) {
		return ReplyListDTO{}, ErrCommentNotFound
	}
	visible := []string{CommentStatusActive, CommentStatusAutoFolded}
	var total int64
	if err := s.db.Model(&model.CommentEntry{}).Where("root_id = ? AND status IN ?", root.ID, visible).Count(&total).Error; err != nil {
		return ReplyListDTO{}, err
	}
	var entries []model.CommentEntry
	if err := s.db.Where("root_id = ? AND status IN ?", root.ID, visible).
		Order("created_at ASC").Order("id ASC").Offset(offset).Limit(pageSize).Find(&entries).Error; err != nil {
		return ReplyListDTO{}, err
	}
	dtos, err := s.entryDTOs(s.db, entries, viewer.UserID)
	if err != nil {
		return ReplyListDTO{}, err
	}
	items := make([]CommentDTO, 0, len(entries))
	for _, entry := range entries {
		items = append(items, dtos[entry.ID])
	}
	return ReplyListDTO{Items: items, Page: page, PerPage: pageSize, Total: total, HasMore: end < total}, nil
}

func safePagination(page, pageSize int) (int, int64, error) {
	if page < 1 || pageSize < 1 {
		return 0, 0, ErrInvalidListOptions
	}
	p, size := int64(page), int64(pageSize)
	if p > math.MaxInt64/size || p-1 > math.MaxInt64/size {
		return 0, 0, ErrInvalidListOptions
	}
	offset := (p - 1) * size
	if offset > int64(^uint(0)>>1) {
		return 0, 0, ErrInvalidListOptions
	}
	return int(offset), p * size, nil
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
	userIDSet := make(map[uuid.UUID]struct{}, len(entries)*2)
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
			References:   []reference.ResolvedReference{},
			Attachments:  []AttachmentDTO{},
			TimeAnchors:  []TimeAnchorDTO{},
			Replies:      []CommentDTO{},
		}
		ids = append(ids, entry.ID)
		userIDSet[entry.AuthorID] = struct{}{}
		if entry.ReplyToAuthorID != nil {
			userIDSet[*entry.ReplyToAuthorID] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return dtos, nil
	}
	userIDs := make([]uuid.UUID, 0, len(userIDSet))
	for id := range userIDSet {
		userIDs = append(userIDs, id)
	}
	userIDs = sortedUUIDs(userIDs)
	var users []model.User
	if err := db.Unscoped().Select("uuid", "username", "display_name", "avatar_url").Where("uuid IN ?", userIDs).Order("uuid ASC").Find(&users).Error; err != nil {
		return nil, err
	}
	userSummaries := make(map[uuid.UUID]UserSummaryDTO, len(userIDs))
	for _, id := range userIDs {
		userSummaries[id] = UserSummaryDTO{ID: id}
	}
	for _, user := range users {
		userSummaries[user.UUID] = UserSummaryDTO{ID: user.UUID, Username: user.Username, DisplayName: user.DisplayName, AvatarURL: user.AvatarURL}
	}
	for _, entry := range entries {
		dto := dtos[entry.ID]
		dto.Author = userSummaries[entry.AuthorID]
		if entry.ReplyToAuthorID != nil {
			summary := userSummaries[*entry.ReplyToAuthorID]
			dto.ReplyToAuthor = &summary
		}
		dtos[entry.ID] = dto
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
	var referenceRows []model.ContentReference
	if err := db.Where("source_type = ? AND source_id IN ?", "comment", ids).
		Order("source_id ASC").Order("source_field ASC").Order("start_offset ASC").Find(&referenceRows).Error; err != nil {
		return nil, err
	}
	viewer := reference.Viewer{}
	if len(viewerIDs) > 0 && viewerIDs[0] != nil {
		viewer.UserID = *viewerIDs[0]
	}
	resolvedReferences, err := s.references.ResolveStoredRows(db, viewer, referenceRows)
	if err != nil {
		return nil, err
	}
	for sourceID, items := range resolvedReferences {
		dto := dtos[sourceID]
		dto.References = items
		dtos[sourceID] = dto
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
