package studio

import (
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
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
	if status := strings.TrimSpace(query.Status); status != "" && status != "draft" && status != "published" {
		return apperr.BadRequest("studio.invalid_status", "status must be draft or published")
	}
	if _, err := studioVisibilityToDB(query.Visibility); err != nil {
		return err
	}
	if query.CollectionID == uuid.Nil {
		return nil
	}
	collection, err := s.repo.GetCollection(query.CollectionID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.NotFound("studio.collection_not_found", "Collection not found")
	}
	if err != nil {
		return err
	}
	if _, err := s.ownedChannel(userID, collection.ChannelID); err != nil {
		return err
	}
	if collection.ContentType != string(module) {
		return apperr.BadRequest("studio.collection_module_mismatch", "Collection does not belong to this module")
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

func (s *Service) listBlogContents(userID uuid.UUID, query ContentQuery) ([]StudioContentItem, int64, error) {
	db := s.db.Model(&model.Post{}).
		Where("posts.user_id = ? AND posts.channel_id = ?", userID, query.ChannelID).
		Where("NOT EXISTS (SELECT 1 FROM podcast_episodes WHERE podcast_episodes.post_id = posts.id AND podcast_episodes.deleted_at IS NULL)")
	db = applyPostContentFilters(db, query)
	if query.CollectionID != uuid.Nil {
		db = db.Where("posts.collection_id = ?", query.CollectionID)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var posts []model.Post
	if err := db.Preload("Collection").
		Order("posts.updated_at DESC").Order("posts.id DESC").
		Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize).
		Find(&posts).Error; err != nil {
		return nil, 0, err
	}
	items := make([]StudioContentItem, 0, len(posts))
	for _, post := range posts {
		collections := make([]model.Collection, 0, 1)
		if post.Collection != nil {
			collections = append(collections, *post.Collection)
		}
		items = append(items, studioPostItem(ModuleBlog, post.ID, post, collections))
	}
	return items, total, nil
}

func (s *Service) listPodcastContents(userID uuid.UUID, query ContentQuery) ([]StudioContentItem, int64, error) {
	db := s.db.Model(&model.PodcastEpisode{}).
		Joins("JOIN posts ON posts.id = podcast_episodes.post_id AND posts.deleted_at IS NULL").
		Where("posts.user_id = ? AND podcast_episodes.channel_id = ?", userID, query.ChannelID)
	db = applyPostContentFilters(db, query)
	if query.CollectionID != uuid.Nil {
		db = db.Where("posts.collection_id = ? OR EXISTS (SELECT 1 FROM post_collections WHERE post_collections.post_id = posts.id AND post_collections.collection_id = ?)", query.CollectionID, query.CollectionID)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var episodes []model.PodcastEpisode
	if err := db.Preload("Post").Preload("Post.Collection").Preload("Post.Collections", "content_type = ?", string(ModulePodcast)).
		Order("CASE WHEN podcast_episodes.updated_at > posts.updated_at THEN podcast_episodes.updated_at ELSE posts.updated_at END DESC").
		Order("podcast_episodes.id DESC").
		Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize).
		Find(&episodes).Error; err != nil {
		return nil, 0, err
	}
	items := make([]StudioContentItem, 0, len(episodes))
	for _, episode := range episodes {
		if episode.Post == nil {
			continue
		}
		post := *episode.Post
		collections := append([]model.Collection{}, post.Collections...)
		if post.Collection != nil && post.Collection.ContentType == string(ModulePodcast) {
			collections = append(collections, *post.Collection)
		}
		item := studioPostItem(ModulePodcast, episode.ID, post, collections)
		item.CoverURL = episode.EpisodeCoverURL
		item.DurationSec = episode.DurationSec
		item.CreatedAt = earlierTime(post.CreatedAt, episode.CreatedAt)
		item.UpdatedAt = laterTime(post.UpdatedAt, episode.UpdatedAt)
		items = append(items, item)
	}
	return items, total, nil
}

func (s *Service) listVideoContents(userID uuid.UUID, query ContentQuery) ([]StudioContentItem, int64, error) {
	db := s.db.Model(&model.Video{}).Where("videos.user_id = ? AND videos.channel_id = ?", userID, query.ChannelID)
	if query.Status != "" {
		db = db.Where("videos.status = ?", strings.TrimSpace(query.Status))
	}
	if visibility, _ := studioVisibilityToDB(query.Visibility); visibility != "" {
		db = db.Where("videos.visibility = ?", visibility)
	}
	if search := strings.ToLower(strings.TrimSpace(query.Search)); search != "" {
		like := "%" + search + "%"
		db = db.Where("LOWER(videos.title) LIKE ? OR LOWER(videos.description) LIKE ?", like, like)
	}
	if query.CollectionID != uuid.Nil {
		db = db.Where("EXISTS (SELECT 1 FROM video_collections WHERE video_collections.video_id = videos.id AND video_collections.collection_id = ?)", query.CollectionID)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var videos []model.Video
	if err := db.Preload("Collections", "content_type = ?", string(ModuleVideo)).
		Order("videos.updated_at DESC").Order("videos.id DESC").
		Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize).
		Find(&videos).Error; err != nil {
		return nil, 0, err
	}
	items := make([]StudioContentItem, 0, len(videos))
	for _, video := range videos {
		channelID := uuid.Nil
		if video.ChannelID != nil {
			channelID = *video.ChannelID
		}
		items = append(items, StudioContentItem{
			ID: video.ID, Module: ModuleVideo, ChannelID: channelID,
			Title: video.Title, Summary: video.Description, CoverURL: video.ThumbnailURL,
			Status: video.Status, Visibility: studioVisibilityFromDB(video.Visibility),
			Collections: studioCollectionSummaries(video.Collections), DurationSec: video.DurationSec,
			ViewCount: int64(video.ViewCount), ProcessingStatus: video.ProcessingStatus,
			CreatedAt: video.CreatedAt, UpdatedAt: video.UpdatedAt,
		})
	}
	return items, total, nil
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
	return StudioContentItem{
		ID: id, Module: module, ChannelID: channelID,
		Title: post.Title, Summary: post.Summary, CoverURL: post.CoverURL,
		Status: post.Status, Visibility: studioVisibilityFromDB(post.Visibility),
		Collections: studioCollectionSummaries(collections), ViewCount: post.ViewCount,
		PublishedAt: post.PublishedAt, CreatedAt: post.CreatedAt, UpdatedAt: post.UpdatedAt,
	}
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
