package studio

import (
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
	contentmodule "atoman/internal/modules/content"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) ListContents(user authctx.CurrentUser, module Module, query ContentQuery) ([]StudioContentItem, int64, error) {
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
	query.ChannelID = channel.ID
	if err := s.validateContentQuery(user.ID, module, query); err != nil {
		return nil, 0, err
	}
	return s.listContentsForChannel(user.ID, module, query)
}

func (s *Service) resolveContentChannel(userID, channelID uuid.UUID) (model.Channel, error) {
	if channelID != uuid.Nil {
		return s.ownedChannel(userID, channelID)
	}
	state, err := s.repo.GetState(userID)
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && state.ChannelID == nil) {
		return model.Channel{}, apperr.NotFound("studio.current_channel_not_found", "Current Studio channel not found")
	}
	if err != nil {
		return model.Channel{}, err
	}
	return s.ownedChannel(userID, *state.ChannelID)
}

func (s *Service) validateContentQuery(userID uuid.UUID, module Module, query ContentQuery) error {
	if status := strings.TrimSpace(query.Status); status != "" && status != "draft" && status != "published" && status != "scheduled" {
		return apperr.BadRequest("studio.invalid_status", "status must be draft, scheduled, or published")
	}
	if _, err := studioVisibilityToDB(query.Visibility); err != nil {
		return err
	}
	if issue := strings.TrimSpace(query.Issue); issue != "" {
		allowed := map[Module]map[string]bool{
			ModuleBlog:    {"draft": true, "stale_draft": true, "missing_cover": true, "missing_collection": true},
			ModulePodcast: {"draft": true, "stale_draft": true, "missing_cover": true, "missing_collection": true, "missing_audio": true},
			ModuleVideo:   {"draft": true, "stale_draft": true, "missing_cover": true, "missing_collection": true, "processing_failed": true, "external_unplayable": true},
		}
		if !allowed[module][issue] {
			return apperr.BadRequest("studio.invalid_issue", "issue is not supported for this module")
		}
	}
	if query.CollectionID == uuid.Nil {
		return nil
	}
	var collection model.ContentCollection
	if err := s.db.First(&collection, "id = ?", query.CollectionID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.NotFound("studio.collection_not_found", "Collection not found")
	} else if err != nil {
		return err
	}
	if _, err := s.ownedChannel(userID, collection.ChannelID); err != nil {
		return err
	}
	if collection.ChannelID != query.ChannelID {
		return apperr.BadRequest("studio.invalid_collection_scope", "Collection does not belong to the selected channel")
	}
	return nil
}

func (s *Service) listContentsForChannel(userID uuid.UUID, module Module, query ContentQuery) ([]StudioContentItem, int64, error) {
	query.Page, query.PageSize = normalizeContentPage(query.Page, query.PageSize)
	switch module {
	case ModuleBlog:
		return s.listBlogContents(userID, query)
	case ModulePodcast:
		return s.listPodcastContents(userID, query)
	case ModuleVideo:
		return s.listVideoContents(userID, query)
	default:
		return nil, 0, apperr.BadRequest("studio.invalid_module", "module must be blog, podcast, or video")
	}
}

type canonicalStudioBlogRow struct {
	ID                 uuid.UUID  `gorm:"column:id"`
	CreatedAt          time.Time  `gorm:"column:created_at"`
	UpdatedAt          time.Time  `gorm:"column:updated_at"`
	AuthorID           *uuid.UUID `gorm:"column:author_id"`
	ChannelID          uuid.UUID  `gorm:"column:channel_id"`
	Title              string     `gorm:"column:title"`
	Summary            string     `gorm:"column:summary"`
	CoverURL           string     `gorm:"column:cover_url"`
	Status             string     `gorm:"column:status"`
	Visibility         string     `gorm:"column:visibility"`
	PublishedAt        *time.Time `gorm:"column:published_at"`
	ScheduledAt        *time.Time `gorm:"column:scheduled_at"`
	Content            string     `gorm:"column:content"`
	Pinned             bool       `gorm:"column:pinned"`
	ViewCount          int64      `gorm:"column:view_count"`
	CollectionConflict bool       `gorm:"column:collection_conflict"`
	CollectionID       *uuid.UUID `gorm:"column:collection_id"`
	CollectionPosition int        `gorm:"column:collection_position"`
}

func (s *Service) listBlogContents(userID uuid.UUID, query ContentQuery) ([]StudioContentItem, int64, error) {
	db := s.db.Table("content_entries AS posts").
		Select(`posts.id, posts.created_at, posts.updated_at, posts.author_id, posts.channel_id,
			posts.title, posts.summary, posts.cover_url, posts.status, posts.visibility,
			posts.published_at, posts.scheduled_at, blog_extensions.content,
			blog_extensions.pinned, blog_extensions.view_count, blog_extensions.collection_conflict,
			memberships.collection_id, memberships.position AS collection_position`).
		Joins("JOIN content_blog_extensions AS blog_extensions ON blog_extensions.content_id = posts.id").
		Joins(`LEFT JOIN LATERAL (
			SELECT collection_id, position
			FROM content_collection_memberships
			WHERE content_id = posts.id
			ORDER BY position ASC, collection_id ASC
			LIMIT 1
		) AS memberships ON TRUE`).
		Where("posts.kind = ? AND posts.author_id = ? AND posts.channel_id = ? AND posts.deleted_at IS NULL", "blog", userID, query.ChannelID)
	if query.Status != "" {
		db = db.Where("posts.status = ?", strings.TrimSpace(query.Status))
	}
	if visibility, _ := studioVisibilityToDB(query.Visibility); visibility != "" {
		db = db.Where("posts.visibility = ?", visibility)
	}
	if search := strings.ToLower(strings.TrimSpace(query.Search)); search != "" {
		like := "%" + search + "%"
		db = db.Where("LOWER(posts.title) LIKE ? OR LOWER(posts.summary) LIKE ? OR LOWER(blog_extensions.content) LIKE ?", like, like, like)
	}
	switch strings.TrimSpace(query.Issue) {
	case "draft":
		db = db.Where("posts.status = ?", "draft")
	case "stale_draft":
		db = db.Where("posts.status = ? AND posts.updated_at < ?", "draft", staleDraftBefore())
	case "missing_cover":
		db = db.Where("TRIM(COALESCE(posts.cover_url, '')) = ''")
	case "missing_collection":
		db = db.Where("memberships.collection_id IS NULL")
	}
	if query.CollectionID != uuid.Nil {
		db = db.Where("memberships.collection_id = ?", query.CollectionID)
	}
	var total int64
	if err := db.Session(&gorm.Session{}).Distinct("posts.id").Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []canonicalStudioBlogRow
	if err := db.Order("posts.updated_at DESC").Order("posts.id DESC").
		Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	collectionIDs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		if row.CollectionID != nil {
			collectionIDs = append(collectionIDs, *row.CollectionID)
		}
	}
	collectionsByID := make(map[uuid.UUID]model.Collection, len(collectionIDs))
	if len(collectionIDs) > 0 {
		var collections []model.ContentCollection
		if err := s.db.Where("id IN ?", collectionIDs).Find(&collections).Error; err != nil {
			return nil, 0, err
		}
		for _, collection := range collections {
			collectionsByID[collection.ID] = model.Collection{
				Base: collection.Base, ChannelID: collection.ChannelID, ContentType: string(ModuleBlog),
				CreatedBy: collection.CreatedBy, Name: collection.Name, Description: collection.Description,
				CoverURL: collection.CoverURL, IsDefault: collection.IsDefault,
			}
		}
	}
	items := make([]StudioContentItem, 0, len(rows))
	for _, row := range rows {
		authorID := uuid.Nil
		if row.AuthorID != nil {
			authorID = *row.AuthorID
		}
		post := model.Post{
			Base: model.Base{ID: row.ID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, UserID: authorID,
			ChannelID: &row.ChannelID, Title: row.Title, Summary: row.Summary, CoverURL: row.CoverURL,
			Status: row.Status, Visibility: row.Visibility, PublishedAt: row.PublishedAt, ScheduledAt: row.ScheduledAt,
			Content: row.Content, Pinned: row.Pinned, ViewCount: row.ViewCount,
			CollectionConflict: row.CollectionConflict, CollectionPosition: row.CollectionPosition,
		}
		if row.CollectionID != nil {
			post.CollectionID = row.CollectionID
			if collection, ok := collectionsByID[*row.CollectionID]; ok {
				post.Collection = &collection
			}
		}
		collections := make([]model.Collection, 0, 1)
		if post.Collection != nil {
			collections = append(collections, *post.Collection)
		}
		items = append(items, studioPostItem(ModuleBlog, post.ID, post, collections))
	}
	if err := s.enrichContentMetrics(ModuleBlog, items); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *Service) listPodcastContents(userID uuid.UUID, query ContentQuery) ([]StudioContentItem, int64, error) {
	db := contentmodule.PodcastQuery(s.db).
		Where(contentmodule.PodcastAuthorColumn(s.db)+" = ? AND posts.channel_id = ?", userID, query.ChannelID)
	if query.Status != "" {
		db = db.Where("posts.status = ?", strings.TrimSpace(query.Status))
	}
	if visibility, _ := studioVisibilityToDB(query.Visibility); visibility != "" {
		db = db.Where("posts.visibility = ?", visibility)
	}
	if search := strings.ToLower(strings.TrimSpace(query.Search)); search != "" {
		like := "%" + search + "%"
		db = db.Where("LOWER(posts.title) LIKE ? OR LOWER(posts.summary) LIKE ? OR LOWER(episodes.shownotes) LIKE ?", like, like, like)
	}
	switch strings.TrimSpace(query.Issue) {
	case "draft":
		db = db.Where("posts.status = ?", "draft")
	case "stale_draft":
		db = db.Where("posts.status = ? AND GREATEST(posts.updated_at, episodes.updated_at) < ?", "draft", staleDraftBefore())
	case "missing_cover":
		db = db.Where("TRIM(COALESCE(episodes.episode_cover_url, '')) = ''")
	case "missing_collection":
		db = db.Where("NOT EXISTS (SELECT 1 FROM content_collection_memberships memberships WHERE memberships.content_id = posts.id)")
	case "missing_audio":
		db = db.Where("TRIM(COALESCE(episodes.audio_url, '')) = ''")
	}
	if query.CollectionID != uuid.Nil {
		db = db.Where("EXISTS (SELECT 1 FROM content_collection_memberships memberships WHERE memberships.content_id = posts.id AND memberships.collection_id = ?)", query.CollectionID)
	}
	var total int64
	if err := db.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	episodes, err := contentmodule.LoadPodcastEpisodes(s.db, db.
		Order("episodes.updated_at DESC, episodes.episode_id DESC").
		Offset((query.Page-1)*query.PageSize).Limit(query.PageSize))
	if err != nil {
		return nil, 0, err
	}
	items := make([]StudioContentItem, 0, len(episodes))
	for _, episode := range episodes {
		if episode.Post == nil {
			continue
		}
		post := *episode.Post
		item := studioPostItem(ModulePodcast, episode.ID, post, post.Collections)
		item.CoverURL = episode.EpisodeCoverURL
		item.DurationSec = episode.DurationSec
		item.CreatedAt = earlierTime(post.CreatedAt, episode.CreatedAt)
		item.UpdatedAt = laterTime(post.UpdatedAt, episode.UpdatedAt)
		items = append(items, item)
	}
	if err := s.enrichContentMetrics(ModulePodcast, items); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *Service) listVideoContents(userID uuid.UUID, query ContentQuery) ([]StudioContentItem, int64, error) {
	db := contentmodule.VideoQuery(s.db).Where(contentmodule.VideoAuthorColumn(s.db)+" = ? AND posts.channel_id = ?", userID, query.ChannelID)
	if query.Status != "" {
		db = db.Where("posts.status = ?", strings.TrimSpace(query.Status))
	}
	if visibility, _ := studioVisibilityToDB(query.Visibility); visibility != "" {
		db = db.Where("posts.visibility = ?", visibility)
	}
	if search := strings.ToLower(strings.TrimSpace(query.Search)); search != "" {
		like := "%" + search + "%"
		db = db.Where("LOWER(posts.title) LIKE ? OR LOWER(posts.summary) LIKE ?", like, like)
	}
	switch strings.TrimSpace(query.Issue) {
	case "draft":
		db = db.Where("posts.status = ?", "draft")
	case "stale_draft":
		db = db.Where("posts.status = ? AND GREATEST(posts.updated_at, videos.updated_at) < ?", "draft", staleDraftBefore())
	case "missing_cover":
		db = db.Where("TRIM(COALESCE(videos.thumbnail_url, '')) = ''")
	case "missing_collection":
		db = db.Where("NOT EXISTS (SELECT 1 FROM content_collection_memberships memberships WHERE memberships.content_id = posts.id)")
	case "processing_failed":
		db = db.Where("videos.processing_status = ?", "failed")
	case "external_unplayable":
		db = db.Where("videos.storage_type = ? AND TRIM(COALESCE(videos.video_url, '')) = ''", "external")
	}
	if query.CollectionID != uuid.Nil {
		db = db.Where("EXISTS (SELECT 1 FROM content_collection_memberships memberships WHERE memberships.content_id = posts.id AND memberships.collection_id = ?)", query.CollectionID)
	}
	var total int64
	if err := db.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	videos, err := contentmodule.LoadVideos(s.db, db.
		Order("videos.updated_at DESC, videos.video_id DESC").
		Offset((query.Page-1)*query.PageSize).Limit(query.PageSize))
	if err != nil {
		return nil, 0, err
	}
	items := make([]StudioContentItem, 0, len(videos))
	for _, video := range videos {
		channelID := uuid.Nil
		if video.ChannelID != nil {
			channelID = *video.ChannelID
		}
		item := StudioContentItem{
			ID: video.ID, Module: ModuleVideo, ChannelID: channelID,
			Title: video.Title, Summary: video.Description, CoverURL: video.ThumbnailURL,
			Status: video.Status, Visibility: studioVisibilityFromDB(video.Visibility),
			Collections: studioCollectionSummaries(video.Collections), CollectionConflict: video.CollectionConflict, DurationSec: video.DurationSec,
			ViewCount: int64(video.ViewCount), ProcessingStatus: video.ProcessingStatus,
			PublishedAt: video.PublishedAt, ScheduledAt: video.ScheduledAt,
			CreatedAt: video.CreatedAt, UpdatedAt: video.UpdatedAt,
		}
		if video.Collection != nil {
			item.Collection = &StudioCollectionSummary{ID: video.Collection.ID, Name: video.Collection.Name}
		}
		items = append(items, item)
	}
	if err := s.enrichContentMetrics(ModuleVideo, items); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

type contentMetricRow struct {
	ContentID uuid.UUID `gorm:"column:content_id"`
	Metric    string
	Count     int64
}

func (s *Service) enrichContentMetrics(module Module, items []StudioContentItem) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(items))
	index := make(map[uuid.UUID]int, len(items))
	primary := map[Module]string{ModuleBlog: "view", ModulePodcast: "play", ModuleVideo: "play"}[module]
	for itemIndex := range items {
		ids = append(ids, items[itemIndex].ID)
		index[items[itemIndex].ID] = itemIndex
		items[itemIndex].Metrics = emptyMetricMap(metricNamesByModule[module])
		items[itemIndex].Metrics[primary] = items[itemIndex].ViewCount
	}
	apply := func(rows []contentMetricRow) {
		for _, row := range rows {
			itemIndex, ok := index[row.ContentID]
			if !ok {
				continue
			}
			if row.Metric == primary && row.Count < items[itemIndex].Metrics[primary] {
				continue
			}
			items[itemIndex].Metrics[row.Metric] = row.Count
		}
	}

	var rows []contentMetricRow
	if err := s.db.Model(&model.StudioMetricEvent{}).
		Select("content_id, metric, COUNT(*) AS count").
		Where("content_type = ? AND content_id IN ?", module, ids).
		Group("content_id, metric").Scan(&rows).Error; err != nil {
		return err
	}
	apply(rows)

	targetKind := map[Module]string{ModuleBlog: "blog_post", ModulePodcast: "podcast_episode", ModuleVideo: "video"}[module]
	rows = nil
	if err := s.db.Model(&model.CommentEntry{}).
		Select("discussion_targets.resource_id AS content_id, 'comment' AS metric, COUNT(*) AS count").
		Joins("JOIN discussion_targets ON discussion_targets.id = comment_entries.target_id").
		Where("discussion_targets.kind = ? AND discussion_targets.resource_id IN ? AND comment_entries.status = ?", targetKind, ids, "active").
		Group("discussion_targets.resource_id").Scan(&rows).Error; err != nil {
		return err
	}
	apply(rows)

	switch module {
	case ModuleBlog, ModuleVideo:
		targetType := "post"
		if module == ModuleVideo {
			targetType = "video"
		}
		rows = nil
		if err := s.db.Model(&model.Like{}).
			Select("target_id AS content_id, 'like' AS metric, COUNT(*) AS count").
			Where("target_type = ? AND target_id IN ?", targetType, ids).
			Group("target_id").Scan(&rows).Error; err != nil {
			return err
		}
		apply(rows)
	}

	rows = nil
	switch module {
	case ModuleBlog:
		if err := s.db.Model(&model.Bookmark{}).Select("content_id, 'bookmark' AS metric, COUNT(*) AS count").Where("content_id IN ?", ids).Group("content_id").Scan(&rows).Error; err != nil {
			return err
		}
	case ModulePodcast:
		if err := s.db.Model(&model.PodcastEpisodeBookmark{}).Select("episode_id AS content_id, 'bookmark' AS metric, COUNT(*) AS count").Where("episode_id IN ? AND kind = ?", ids, "favorite").Group("episode_id").Scan(&rows).Error; err != nil {
			return err
		}
	case ModuleVideo:
		if err := s.db.Model(&model.VideoBookmark{}).Select("video_id AS content_id, 'bookmark' AS metric, COUNT(*) AS count").Where("video_id IN ?", ids).Group("video_id").Scan(&rows).Error; err != nil {
			return err
		}
	}
	apply(rows)
	return nil
}

func applyPostContentFilters(db *gorm.DB, query ContentQuery) *gorm.DB {
	if query.Status != "" {
		db = db.Where("posts.status = ?", strings.TrimSpace(query.Status))
	}
	if visibility, _ := studioVisibilityToDB(query.Visibility); visibility != "" {
		db = db.Where("posts.visibility = ?", visibility)
	}
	if search := strings.ToLower(strings.TrimSpace(query.Search)); search != "" {
		like := "%" + search + "%"
		db = db.Where("LOWER(posts.title) LIKE ? OR LOWER(posts.summary) LIKE ?", like, like)
	}
	return db
}

func studioPostItem(module Module, id uuid.UUID, post model.Post, collections []model.Collection) StudioContentItem {
	channelID := uuid.Nil
	if post.ChannelID != nil {
		channelID = *post.ChannelID
	}
	item := StudioContentItem{
		ID: id, Module: module, ChannelID: channelID,
		Title: post.Title, Summary: post.Summary, CoverURL: post.CoverURL,
		Status: post.Status, Visibility: studioVisibilityFromDB(post.Visibility),
		Collections: studioCollectionSummaries(collections), CollectionConflict: post.CollectionConflict, ViewCount: post.ViewCount,
		PublishedAt: post.PublishedAt, ScheduledAt: post.ScheduledAt,
		CreatedAt: post.CreatedAt, UpdatedAt: post.UpdatedAt,
	}
	if post.Collection != nil {
		item.Collection = &StudioCollectionSummary{ID: post.Collection.ID, Name: post.Collection.Name}
	}
	return item
}

func studioCollectionSummaries(collections []model.Collection) []StudioCollectionSummary {
	result := make([]StudioCollectionSummary, 0, len(collections))
	seen := make(map[uuid.UUID]struct{}, len(collections))
	for _, collection := range collections {
		if _, exists := seen[collection.ID]; exists {
			continue
		}
		seen[collection.ID] = struct{}{}
		result = append(result, StudioCollectionSummary{ID: collection.ID, Name: collection.Name})
	}
	return result
}

func studioVisibilityToDB(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "", nil
	case "public":
		return "public", nil
	case "subscribers":
		return "followers", nil
	case "private":
		return "private", nil
	default:
		return "", apperr.BadRequest("studio.invalid_visibility", "visibility must be public, subscribers, or private")
	}
}

func studioVisibilityFromDB(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "followers":
		return "subscribers"
	case "private":
		return "private"
	default:
		return "public"
	}
}

func normalizeContentPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func earlierTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}

func laterTime(left, right time.Time) time.Time {
	if left.After(right) {
		return left
	}
	return right
}
