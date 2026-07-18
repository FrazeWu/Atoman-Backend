package studio

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
)

func (s *Service) ListInteractions(user authctx.CurrentUser, module Module, query InteractionQuery) ([]StudioInteractionItem, int64, error) {
	if err := requireUser(user); err != nil {
		return nil, 0, err
	}
	if _, err := ParseModule(string(module)); err != nil {
		return nil, 0, err
	}
	channel, err := s.resolveContentChannel(user.ID, query.ChannelID)
	if err != nil {
		return nil, 0, err
	}
	targetKind, titles, err := s.interactionContentTitles(user.ID, channel.ID, module)
	if err != nil {
		return nil, 0, err
	}
	if len(titles) == 0 {
		return []StudioInteractionItem{}, 0, nil
	}
	contentIDs := make([]uuid.UUID, 0, len(titles))
	for id := range titles {
		contentIDs = append(contentIDs, id)
	}
	var targets []model.DiscussionTarget
	if err := s.db.Where("kind = ? AND resource_id IN ?", targetKind, contentIDs).Find(&targets).Error; err != nil {
		return nil, 0, err
	}
	if len(targets) == 0 {
		return []StudioInteractionItem{}, 0, nil
	}
	targetByID := make(map[uuid.UUID]model.DiscussionTarget, len(targets))
	targetIDs := make([]uuid.UUID, 0, len(targets))
	for _, target := range targets {
		targetByID[target.ID] = target
		targetIDs = append(targetIDs, target.ID)
	}
	db := s.db.Model(&model.CommentEntry{}).
		Where("comment_entries.target_id IN ? AND comment_entries.root_id IS NULL AND comment_entries.status = ?", targetIDs, "active")
	if query.Unreplied {
		db = db.Where(`NOT EXISTS (
			SELECT 1 FROM comment_entries replies
			WHERE replies.root_id = comment_entries.id
			  AND replies.author_id = ?
			  AND replies.status = 'active'
			  AND replies.deleted_at IS NULL
		)`, user.ID)
	}
	if query.Anchored {
		db = db.Where("EXISTS (SELECT 1 FROM comment_time_anchors WHERE comment_time_anchors.comment_id = comment_entries.id AND comment_time_anchors.deleted_at IS NULL)")
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, pageSize := normalizeContentPage(query.Page, query.PageSize)
	var comments []model.CommentEntry
	if err := db.Order("comment_entries.created_at DESC").Order("comment_entries.id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&comments).Error; err != nil {
		return nil, 0, err
	}
	items, err := s.buildInteractionItems(user.ID, comments, targetByID, titles)
	return items, total, err
}

func (s *Service) interactionContentTitles(userID, channelID uuid.UUID, module Module) (string, map[uuid.UUID]string, error) {
	titles := make(map[uuid.UUID]string)
	switch module {
	case ModuleBlog:
		var rows []struct {
			ID    uuid.UUID
			Title string
		}
		err := s.db.Model(&model.Post{}).Select("posts.id, posts.title").
			Where("posts.user_id = ? AND posts.channel_id = ?", userID, channelID).
			Where("NOT EXISTS (SELECT 1 FROM podcast_episodes WHERE podcast_episodes.post_id = posts.id AND podcast_episodes.deleted_at IS NULL)").
			Scan(&rows).Error
		for _, row := range rows {
			titles[row.ID] = row.Title
		}
		return "blog_post", titles, err
	case ModulePodcast:
		var rows []struct {
			ID    uuid.UUID
			Title string
		}
		err := s.db.Model(&model.PodcastEpisode{}).
			Select("podcast_episodes.id, posts.title").
			Joins("JOIN posts ON posts.id = podcast_episodes.post_id AND posts.deleted_at IS NULL").
			Where("posts.user_id = ? AND podcast_episodes.channel_id = ?", userID, channelID).
			Scan(&rows).Error
		for _, row := range rows {
			titles[row.ID] = row.Title
		}
		return "podcast_episode", titles, err
	case ModuleVideo:
		var videos []model.Video
		err := s.db.Select("id", "title").Where("user_id = ? AND channel_id = ?", userID, channelID).Find(&videos).Error
		for _, video := range videos {
			titles[video.ID] = video.Title
		}
		return "video", titles, err
	default:
		return "", nil, apperr.BadRequest("studio.invalid_module", "module must be blog, podcast, or video")
	}
}

func (s *Service) buildInteractionItems(
	userID uuid.UUID,
	comments []model.CommentEntry,
	targetByID map[uuid.UUID]model.DiscussionTarget,
	titles map[uuid.UUID]string,
) ([]StudioInteractionItem, error) {
	if len(comments) == 0 {
		return []StudioInteractionItem{}, nil
	}
	commentIDs := make([]uuid.UUID, 0, len(comments))
	authorIDs := make([]uuid.UUID, 0, len(comments))
	for _, comment := range comments {
		commentIDs = append(commentIDs, comment.ID)
		authorIDs = append(authorIDs, comment.AuthorID)
	}
	var authors []model.User
	if err := s.db.Where("uuid IN ?", authorIDs).Find(&authors).Error; err != nil {
		return nil, err
	}
	authorByID := make(map[uuid.UUID]model.User, len(authors))
	for _, author := range authors {
		authorByID[author.UUID] = author
	}
	var anchors []model.CommentTimeAnchor
	if err := s.db.Where("comment_id IN ?", commentIDs).Order("start_offset ASC").Find(&anchors).Error; err != nil {
		return nil, err
	}
	anchorsByComment := make(map[uuid.UUID][]StudioTimeAnchor)
	for _, anchor := range anchors {
		anchorsByComment[anchor.CommentID] = append(anchorsByComment[anchor.CommentID], StudioTimeAnchor{
			Start: anchor.StartOffset, End: anchor.EndOffset, Seconds: anchor.Seconds,
		})
	}
	var repliedRootIDs []uuid.UUID
	if err := s.db.Model(&model.CommentEntry{}).Distinct("root_id").
		Where("root_id IN ? AND author_id = ? AND status = ?", commentIDs, userID, "active").
		Pluck("root_id", &repliedRootIDs).Error; err != nil {
		return nil, err
	}
	replied := make(map[uuid.UUID]struct{}, len(repliedRootIDs))
	for _, id := range repliedRootIDs {
		replied[id] = struct{}{}
	}
	items := make([]StudioInteractionItem, 0, len(comments))
	for _, comment := range comments {
		target := targetByID[comment.TargetID]
		author := authorByID[comment.AuthorID]
		_, hasReply := replied[comment.ID]
		items = append(items, StudioInteractionItem{
			ID: comment.ID, ContentID: target.ResourceID, ContentTitle: titles[target.ResourceID], TargetKind: target.Kind,
			Author:  StudioInteractionAuthor{ID: author.UUID, Username: author.Username, DisplayName: author.DisplayName, AvatarURL: author.AvatarURL},
			Content: comment.Content, ReplyCount: comment.ReplyCount, Replied: hasReply,
			Pinned:      target.PinnedCommentID != nil && *target.PinnedCommentID == comment.ID,
			TimeAnchors: anchorsByComment[comment.ID], CreatedAt: comment.CreatedAt,
		})
	}
	return items, nil
}
