package portal

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"atoman/internal/model"

	"gorm.io/gorm"
)

type Service struct {
	db *gorm.DB
}

func (s *Service) HotContent(limit int) (HotResponse, error) {
	if limit < 1 {
		limit = 6
	}

	sections := make([]HotSection, 0, 8)
	all := make([]HotItem, 0, limit*4)

	loaders := []func(int) ([]HotItem, error){
		s.hotBlogPosts,
		s.hotVideos,
		s.hotMusicAlbums,
		s.hotForumTopics,
		s.hotDebates,
		s.hotPodcastEpisodes,
		s.hotFeedItems,
		s.hotTimelineEvents,
	}

	for _, load := range loaders {
		items, err := load(limit)
		if err != nil {
			if isMissingTableError(err) {
				continue
			}
			return HotResponse{}, err
		}
		if len(items) == 0 {
			continue
		}
		sections = append(sections, HotSection{
			Module: items[0].Module,
			Title:  sectionTitle(items[0].Module),
			Items:  items,
		})
		all = append(all, items...)
	}

	sortHotItems(all)
	if len(all) > 4 {
		all = all[:4]
	}

	return HotResponse{Featured: all, Sections: sections}, nil
}

type blogHotRow struct {
	model.Post
	LikesCount    int64
	CommentsCount int64
}

func (s *Service) hotBlogPosts(limit int) ([]HotItem, error) {
	var rows []blogHotRow
	err := s.db.Model(&model.Post{}).
		Select("posts.*, COUNT(DISTINCT likes.id) AS likes_count, COALESCE(MAX(discussion_targets.comment_count), 0) AS comments_count").
		Joins("LEFT JOIN likes ON likes.target_id = posts.id AND likes.target_type = ?", "post").
		Joins("LEFT JOIN discussion_targets ON discussion_targets.resource_id = posts.id AND discussion_targets.kind = ?", "blog_post").
		Where("posts.status = ? AND posts.visibility = ?", "published", "public").
		Group("posts.id").
		Order("(COUNT(DISTINCT likes.id) * 3 + COALESCE(MAX(discussion_targets.comment_count), 0) * 2) DESC").
		Order("posts.updated_at DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	items := make([]HotItem, 0, len(rows))
	for _, row := range rows {
		score := float64(row.LikesCount*3 + row.CommentsCount*2)
		items = append(items, HotItem{
			ID:          row.ID.String(),
			Module:      "blog",
			Kind:        "post",
			Title:       row.Title,
			Summary:     excerpt(row.Summary, row.Content),
			ImageURL:    row.CoverURL,
			TargetPath:  "/posts/post/" + row.ID.String(),
			Score:       score,
			ScoreLabel:  countLabel(row.LikesCount, "赞", row.CommentsCount, "评论"),
			PublishedAt: timePtr(row.UpdatedAt),
		})
	}
	return items, nil
}

func (s *Service) hotVideos(limit int) ([]HotItem, error) {
	var videos []model.Video
	err := s.db.Where("status = ? AND visibility = ?", "published", "public").
		Order("view_count DESC").
		Order("updated_at DESC").
		Limit(limit).
		Find(&videos).Error
	if err != nil {
		return nil, err
	}

	items := make([]HotItem, 0, len(videos))
	for _, video := range videos {
		items = append(items, HotItem{
			ID:          video.ID.String(),
			Module:      "video",
			Kind:        "video",
			Title:       video.Title,
			Summary:     excerpt(video.Description, ""),
			ImageURL:    video.ThumbnailURL,
			TargetPath:  "/videos/watch/" + video.ID.String(),
			Score:       float64(video.ViewCount),
			ScoreLabel:  fmt.Sprintf("%d 次播放", video.ViewCount),
			PublishedAt: timePtr(video.UpdatedAt),
		})
	}
	return items, nil
}

func (s *Service) hotMusicAlbums(limit int) ([]HotItem, error) {
	var albums []model.Album
	err := s.db.Preload("Artists").
		Where("status <> ?", "closed").
		Order("hot_score DESC").
		Order("updated_at DESC").
		Limit(limit).
		Find(&albums).Error
	if err != nil {
		return nil, err
	}

	items := make([]HotItem, 0, len(albums))
	for _, album := range albums {
		items = append(items, HotItem{
			ID:          album.ID.String(),
			Module:      "music",
			Kind:        "album",
			Title:       album.Title,
			Summary:     artistNames(album.Artists),
			ImageURL:    album.CoverURL,
			TargetPath:  "/music",
			Score:       album.HotScore,
			ScoreLabel:  fmt.Sprintf("热度 %.0f", album.HotScore),
			PublishedAt: timePtr(album.UpdatedAt),
		})
	}
	return items, nil
}

func (s *Service) hotForumTopics(limit int) ([]HotItem, error) {
	var topics []model.ForumTopic
	err := s.db.Where("closed = ?", false).
		Order("(like_count * 3 + reply_count * 2 + view_count) DESC").
		Order("updated_at DESC").
		Limit(limit).
		Find(&topics).Error
	if err != nil {
		return nil, err
	}

	items := make([]HotItem, 0, len(topics))
	for _, topic := range topics {
		score := float64(topic.LikeCount*3 + topic.ReplyCount*2 + topic.ViewCount)
		items = append(items, HotItem{
			ID:          topic.ID.String(),
			Module:      "forum",
			Kind:        "topic",
			Title:       topic.Title,
			Summary:     excerpt(topic.Content, ""),
			TargetPath:  "/forum/topic/" + topic.ID.String(),
			Score:       score,
			ScoreLabel:  countLabel(int64(topic.LikeCount), "赞", int64(topic.ReplyCount), "回复"),
			PublishedAt: timePtr(topic.UpdatedAt),
		})
	}
	return items, nil
}

func (s *Service) hotDebates(limit int) ([]HotItem, error) {
	var debates []model.Debate
	err := s.db.Where("status <> ?", "closed").
		Order("(argument_count * 3 + vote_count * 2 + view_count) DESC").
		Order("updated_at DESC").
		Limit(limit).
		Find(&debates).Error
	if err != nil {
		return nil, err
	}

	items := make([]HotItem, 0, len(debates))
	for _, debate := range debates {
		score := float64(debate.ArgumentCount*3 + debate.VoteCount*2 + debate.ViewCount)
		items = append(items, HotItem{
			ID:          debate.ID.String(),
			Module:      "debate",
			Kind:        "debate",
			Title:       debate.Title,
			Summary:     excerpt(debate.Description, debate.Content),
			TargetPath:  "/debate/" + debate.ID.String(),
			Score:       score,
			ScoreLabel:  countLabel(int64(debate.ArgumentCount), "论点", int64(debate.VoteCount), "投票"),
			PublishedAt: timePtr(debate.UpdatedAt),
		})
	}
	return items, nil
}

func (s *Service) hotPodcastEpisodes(limit int) ([]HotItem, error) {
	var episodes []model.PodcastEpisode
	err := s.db.Preload("Post").
		Order("updated_at DESC").
		Limit(limit).
		Find(&episodes).Error
	if err != nil {
		return nil, err
	}

	items := make([]HotItem, 0, len(episodes))
	for _, episode := range episodes {
		if episode.Post == nil || episode.Post.Status != "published" || episode.Post.Visibility != "public" {
			continue
		}
		items = append(items, HotItem{
			ID:          episode.ID.String(),
			Module:      "podcast",
			Kind:        "episode",
			Title:       episode.Post.Title,
			Summary:     excerpt(episode.Post.Summary, episode.Post.Content),
			ImageURL:    firstNonEmpty(episode.EpisodeCoverURL, episode.Post.CoverURL),
			TargetPath:  "/podcasts/episode/" + episode.ID.String(),
			Score:       recencyScore(episode.UpdatedAt),
			ScoreLabel:  "近期热门",
			PublishedAt: timePtr(episode.UpdatedAt),
		})
	}
	return items, nil
}

func (s *Service) hotFeedItems(limit int) ([]HotItem, error) {
	var items []model.FeedItem
	err := s.db.Order("published_at DESC").
		Limit(limit).
		Find(&items).Error
	if err != nil {
		return nil, err
	}

	result := make([]HotItem, 0, len(items))
	for _, item := range items {
		result = append(result, HotItem{
			ID:          item.ID.String(),
			Module:      "feed",
			Kind:        "feed_item",
			Title:       item.Title,
			Summary:     excerpt(item.Summary, ""),
			ImageURL:    item.ImageURL,
			TargetPath:  "/feed/item/" + item.ID.String(),
			Score:       recencyScore(item.PublishedAt),
			ScoreLabel:  "近期热门",
			PublishedAt: timePtr(item.PublishedAt),
		})
	}
	return result, nil
}

func (s *Service) hotTimelineEvents(limit int) ([]HotItem, error) {
	var events []model.TimelineEvent
	err := s.db.Where("is_public = ?", true).
		Order("updated_at DESC").
		Limit(limit).
		Find(&events).Error
	if err != nil {
		return nil, err
	}

	items := make([]HotItem, 0, len(events))
	for _, event := range events {
		items = append(items, HotItem{
			ID:          event.ID.String(),
			Module:      "timeline",
			Kind:        "event",
			Title:       event.Title,
			Summary:     excerpt(event.Description, event.Content),
			TargetPath:  "/timeline",
			Score:       recencyScore(event.UpdatedAt),
			ScoreLabel:  "近期热门",
			PublishedAt: timePtr(event.EventDate),
		})
	}
	return items, nil
}

func sortHotItems(items []HotItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score == items[j].Score {
			return publishedAfter(items[i].PublishedAt, items[j].PublishedAt)
		}
		return items[i].Score > items[j].Score
	})
}

func sectionTitle(module string) string {
	switch module {
	case "feed":
		return "订阅热读"
	case "music":
		return "热门音乐"
	case "blog":
		return "热门文章"
	case "forum":
		return "论坛热帖"
	case "debate":
		return "热门辩题"
	case "timeline":
		return "时间线精选"
	case "podcast":
		return "播客热听"
	case "video":
		return "视频热播"
	default:
		return "热门内容"
	}
}

func excerpt(primary string, fallback string) string {
	value := strings.TrimSpace(firstNonEmpty(primary, fallback))
	if len([]rune(value)) <= 120 {
		return value
	}
	return string([]rune(value)[:120])
}

func countLabel(first int64, firstName string, second int64, secondName string) string {
	parts := make([]string, 0, 2)
	if first > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", first, firstName))
	}
	if second > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", second, secondName))
	}
	if len(parts) == 0 {
		return "近期热门"
	}
	return strings.Join(parts, " / ")
}

func artistNames(artists []model.Artist) string {
	names := make([]string, 0, len(artists))
	for _, artist := range artists {
		names = append(names, artist.Name)
	}
	return strings.Join(names, " / ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func timePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func publishedAfter(left *time.Time, right *time.Time) bool {
	if left == nil {
		return false
	}
	if right == nil {
		return true
	}
	return left.After(*right)
}

func recencyScore(value time.Time) float64 {
	if value.IsZero() {
		return 0
	}
	hours := time.Since(value).Hours()
	if hours < 0 {
		hours = 0
	}
	return 1000 / (1 + hours/24)
}

func isMissingTableError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such table") ||
		strings.Contains(message, "does not exist")
}
