package portal

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"atoman/internal/model"
	blogmodule "atoman/internal/modules/blog"
	contentmodule "atoman/internal/modules/content"

	"github.com/google/uuid"

	"gorm.io/gorm"
)

type Service struct {
	db       *gorm.DB
	cacheMu  sync.Mutex
	hotCache map[int]hotCacheEntry
}

const (
	hotCacheTTL       = time.Minute
	spotlightPageSize = 4
)

var spotlightModuleOrder = []string{
	"blog", "feed", "video", "podcast", "music", "forum", "debate", "timeline",
}

type hotCacheEntry struct {
	items     []HotItem
	sections  []HotSection
	expiresAt time.Time
}

func (s *Service) HotContent(limit int) (HotResponse, error) {
	return s.HotContentAtOffset(limit, 0)
}

func (s *Service) HotContentAtOffset(limit int, spotlightOffset int) (HotResponse, error) {
	if limit < 1 {
		limit = 6
	}
	if spotlightOffset < 0 {
		spotlightOffset = 0
	}

	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	cached, ok := s.hotCache[limit]
	if !ok || !time.Now().Before(cached.expiresAt) {
		sections, items, err := s.loadHotContent(limit)
		if err != nil {
			return HotResponse{}, err
		}
		cached = hotCacheEntry{
			items:     items,
			sections:  sections,
			expiresAt: time.Now().Add(hotCacheTTL),
		}
		s.hotCache[limit] = cached
	}

	return HotResponse{
		Featured:      spotlightBatch(cached.items, spotlightOffset),
		FeaturedTotal: len(cached.items),
		Sections:      cached.sections,
	}, nil
}

func (s *Service) loadHotContent(limit int) ([]HotSection, []HotItem, error) {
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

	type loadResult struct {
		items []HotItem
		err   error
	}
	results := make([]loadResult, len(loaders))
	var wg sync.WaitGroup
	for index, load := range loaders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[index].items, results[index].err = load(limit)
		}()
	}
	wg.Wait()

	for _, result := range results {
		items, err := result.items, result.err
		if err != nil {
			if isMissingTableError(err) {
				continue
			}
			return nil, nil, err
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
	return sections, arrangeSpotlightItems(all), nil
}

func arrangeSpotlightItems(items []HotItem) []HotItem {
	byModule := make(map[string][]HotItem)
	moduleOrder := make([]string, 0, len(spotlightModuleOrder))
	seenModules := make(map[string]struct{})

	for _, item := range items {
		byModule[item.Module] = append(byModule[item.Module], item)
	}
	for _, module := range spotlightModuleOrder {
		if len(byModule[module]) == 0 {
			continue
		}
		moduleOrder = append(moduleOrder, module)
		seenModules[module] = struct{}{}
	}
	for _, item := range items {
		if _, seen := seenModules[item.Module]; seen {
			continue
		}
		moduleOrder = append(moduleOrder, item.Module)
		seenModules[item.Module] = struct{}{}
	}

	arranged := make([]HotItem, 0, len(items))
	for index := 0; len(arranged) < len(items); index++ {
		for _, module := range moduleOrder {
			candidates := byModule[module]
			if index >= len(candidates) {
				continue
			}
			arranged = append(arranged, candidates[index])
		}
	}
	return arranged
}

func spotlightBatch(items []HotItem, offset int) []HotItem {
	if len(items) == 0 {
		return []HotItem{}
	}
	if offset < 0 || offset >= len(items) {
		offset = 0
	}

	end := offset + spotlightPageSize
	if end > len(items) {
		end = len(items)
	}
	return append([]HotItem(nil), items[offset:end]...)
}

type blogHotRow struct {
	ID              uuid.UUID `gorm:"column:id"`
	Title           string
	Summary         string
	Content         string `gorm:"column:content"`
	CoverURL        string `gorm:"column:cover_url"`
	AuthorName      string `gorm:"column:author_name"`
	AuthorUsername  string `gorm:"column:author_username"`
	AuthorAvatarURL string `gorm:"column:author_avatar_url"`
	UpdatedAt       time.Time
	LikesCount      int64
	CommentsCount   int64
}

func (s *Service) hotBlogPosts(limit int) ([]HotItem, error) {
	var rows []blogHotRow
	err := blogmodule.CanonicalBlogPostsQuery(s.db).
		Select(`posts.id, posts.title, posts.summary, posts.cover_url, posts.updated_at,
			blog_extensions.content,
			COALESCE(NULLIF(authors.display_name, ''), authors.username, '') AS author_name,
			COALESCE(authors.username, '') AS author_username,
			COALESCE(authors.avatar_url, '') AS author_avatar_url,
			COUNT(DISTINCT likes.id) AS likes_count,
			COALESCE(MAX(discussion_targets.comment_count), 0) AS comments_count`).
		Joins(`LEFT JOIN "Users" AS authors ON authors.uuid = posts.author_id AND authors.deleted_at IS NULL`).
		Joins("LEFT JOIN likes ON likes.target_id = posts.id AND likes.target_type = ? AND likes.deleted_at IS NULL", "post").
		Joins("LEFT JOIN discussion_targets ON discussion_targets.resource_id = posts.id AND discussion_targets.kind = ? AND discussion_targets.deleted_at IS NULL", "blog_post").
		Where("posts.status = ? AND COALESCE(posts.visibility, '') IN ?", "published", []string{"", "public"}).
		Group("posts.id, blog_extensions.content, authors.display_name, authors.username, authors.avatar_url").
		Order("(COUNT(DISTINCT likes.id) * 3 + COALESCE(MAX(discussion_targets.comment_count), 0) * 2) DESC").
		Order("posts.updated_at DESC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	items := make([]HotItem, 0, len(rows))
	for _, row := range rows {
		score := float64(row.LikesCount*3 + row.CommentsCount*2)
		items = append(items, HotItem{
			ID: row.ID.String(), Module: "blog", Kind: "post", Title: row.Title,
			Summary: excerpt(row.Summary, row.Content), ImageURL: row.CoverURL,
			AuthorName: row.AuthorName, AuthorUsername: row.AuthorUsername, AuthorAvatarURL: row.AuthorAvatarURL,
			TargetPath: "/posts/post/" + row.ID.String(), Score: score,
			ScoreLabel: countLabel(row.LikesCount, "赞", row.CommentsCount, "评论"), PublishedAt: timePtr(row.UpdatedAt),
		})
	}
	return items, nil
}

func (s *Service) hotVideos(limit int) ([]HotItem, error) {
	videos, err := contentmodule.LoadVideos(s.db, contentmodule.VideoQuery(s.db).
		Where("posts.status = ? AND posts.visibility IN ?", "published", []string{"", "public"}).
		Order("videos.view_count DESC").
		Order("videos.updated_at DESC").
		Limit(limit))
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
		Where("status NOT IN ?", []string{"closed", "rejected", "draft"}).
		Order("hot_score DESC").
		Order("updated_at DESC").
		Limit(limit).
		Find(&albums).Error
	if err != nil {
		return nil, err
	}

	bookmarkCounts := make(map[uuid.UUID]int64)
	if len(albums) > 0 {
		albumIDs := make([]uuid.UUID, 0, len(albums))
		for _, album := range albums {
			albumIDs = append(albumIDs, album.ID)
		}
		var bookmarkRows []struct {
			AlbumID uuid.UUID `gorm:"column:album_id"`
			Count   int64     `gorm:"column:count"`
		}
		err = s.db.Model(&model.AlbumBookmark{}).
			Select("album_id, COUNT(*) AS count").
			Where("album_id IN ?", albumIDs).
			Group("album_id").
			Scan(&bookmarkRows).Error
		if err != nil && !isMissingTableError(err) {
			return nil, err
		}
		for _, row := range bookmarkRows {
			bookmarkCounts[row.AlbumID] = row.Count
		}
	}

	return hotMusicItems(albums, bookmarkCounts), nil
}

func hotMusicItems(albums []model.Album, bookmarkCounts map[uuid.UUID]int64) []HotItem {
	items := make([]HotItem, 0, len(albums))
	for _, album := range albums {
		var publishedAt *time.Time
		if !album.ReleaseDate.IsZero() {
			publishedAt = &album.ReleaseDate
		} else if album.ReleaseYear > 0 {
			t := time.Date(album.ReleaseYear, 1, 1, 0, 0, 0, 0, time.UTC)
			publishedAt = &t
		} else if album.Year > 0 {
			t := time.Date(album.Year, 1, 1, 0, 0, 0, 0, time.UTC)
			publishedAt = &t
		} else if !album.CreatedAt.IsZero() {
			publishedAt = &album.CreatedAt
		} else {
			publishedAt = timePtr(album.UpdatedAt)
		}

		items = append(items, HotItem{
			ID:            album.ID.String(),
			Module:        "music",
			Kind:          "album",
			Title:         album.Title,
			Summary:       artistNames(album.Artists),
			ImageURL:      album.CoverURL,
			Artists:       hotArtists(album.Artists),
			PlayCount:     album.PlayCount,
			BookmarkCount: bookmarkCounts[album.ID],
			TargetPath:    "/music/album/" + album.ID.String(),
			Score:         album.HotScore,
			ScoreLabel:    fmt.Sprintf("热度 %.0f", album.HotScore),
			PublishedAt:   publishedAt,
		})
	}
	return items
}

func (s *Service) hotForumTopics(limit int) ([]HotItem, error) {
	var topics []model.ForumTopic
	err := s.db.Where("closed = ?", false).
		Where(`NOT EXISTS (
			SELECT 1 FROM forum_category_permissions fcp
			WHERE fcp.category_id = forum_topics.category_id AND fcp.deleted_at IS NULL
		)`).
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
	type debateRow struct {
		model.Debate
		CommentCount int `gorm:"column:comment_count"`
		VoteCount    int `gorm:"column:community_vote_count"`
	}
	var debates []debateRow
	err := s.db.Model(&model.Debate{}).Select(`debates.*,
		COALESCE((SELECT comment_count FROM discussion_targets WHERE kind = 'debate' AND resource_id = debates.id AND deleted_at IS NULL LIMIT 1), 0) AS comment_count,
		COALESCE((SELECT COUNT(*) FROM debate_votes WHERE debate_id = debates.id AND deleted_at IS NULL), 0) AS community_vote_count,
		(COALESCE((SELECT comment_count FROM discussion_targets WHERE kind = 'debate' AND resource_id = debates.id AND deleted_at IS NULL LIMIT 1), 0) * 3
		 + COALESCE((SELECT COUNT(*) FROM debate_votes WHERE debate_id = debates.id AND deleted_at IS NULL), 0) * 2
		 + debates.view_count) AS debate_hot_score`).
		Where("debates.status = ?", model.DebateStatusActive).
		Order("debate_hot_score DESC").
		Order("updated_at DESC").
		Limit(limit).
		Find(&debates).Error
	if err != nil {
		return nil, err
	}

	items := make([]HotItem, 0, len(debates))
	for _, debate := range debates {
		score := float64(debate.CommentCount*3 + debate.VoteCount*2 + debate.ViewCount)
		items = append(items, HotItem{
			ID:          debate.ID.String(),
			Module:      "debate",
			Kind:        "debate",
			Title:       debate.Title,
			Summary:     excerpt(debate.Description, debate.Content),
			TargetPath:  "/debate/" + debate.ID.String(),
			Score:       score,
			ScoreLabel:  countLabel(int64(debate.CommentCount), "评论", int64(debate.VoteCount), "投票"),
			PublishedAt: timePtr(debate.UpdatedAt),
		})
	}
	return items, nil
}

func (s *Service) hotPodcastEpisodes(limit int) ([]HotItem, error) {
	episodes, err := contentmodule.LoadPodcastEpisodes(s.db, contentmodule.PodcastQuery(s.db).
		Where("posts.status = ? AND posts.visibility IN ?", "published", []string{"", "public"}).
		Order("episodes.updated_at DESC").Limit(limit))
	if err != nil {
		return nil, err
	}

	items := make([]HotItem, 0, len(episodes))
	for _, episode := range episodes {
		if episode.Post == nil {
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
	err := s.db.Preload("FeedSource").
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id AND feed_sources.deleted_at IS NULL AND feed_sources.hidden = ?", false).
		Order("feed_items.published_at DESC").
		Limit(limit).
		Find(&items).Error
	if err != nil {
		return nil, err
	}

	result := make([]HotItem, 0, len(items))
	for _, item := range items {
		sourceName := ""
		sourceImageURL := ""
		if item.FeedSource != nil {
			sourceName = item.FeedSource.Title
			sourceImageURL = item.FeedSource.CoverURL
		}
		result = append(result, HotItem{
			ID:             item.ID.String(),
			Module:         "feed",
			Kind:           "feed_item",
			Title:          item.Title,
			Summary:        excerpt(item.Summary, ""),
			ImageURL:       item.ImageURL,
			SourceName:     sourceName,
			SourceImageURL: sourceImageURL,
			TargetPath:     "/feed/item/" + item.ID.String(),
			Score:          recencyScore(item.PublishedAt),
			ScoreLabel:     "近期热门",
			PublishedAt:    timePtr(item.PublishedAt),
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
			TargetPath:  "/timeline?event=" + event.ID.String(),
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

func hotArtists(artists []model.Artist) []HotArtist {
	items := make([]HotArtist, 0, len(artists))
	for _, artist := range artists {
		items = append(items, HotArtist{ID: artist.ID.String(), Name: artist.Name})
	}
	return items
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
