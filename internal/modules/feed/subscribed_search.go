package feed

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	feedSearchRankRelevance = "relevance"
	feedSearchRankRecent    = "recent"
	feedSearchCandidateCap  = 1000
)

// SubscribedSearchHit is a timeline row plus the plain-text evidence for a match.
// Reader and full-text HTML stay out of the payload; search_snippet carries the excerpt.
type SubscribedSearchHit struct {
	TimelineItemDTO
	SearchSnippet    string `json:"search_snippet,omitempty"`
	SearchMatchField string `json:"search_match_field,omitempty"`
}

type searchCandidate struct {
	Kind        string
	ID          uuid.UUID
	Title       string
	Summary     string
	Author      string
	SourceTitle string
	Body        string
	PublishedAt time.Time
	Score       float64
	MatchField  string
	Post        *model.Post
	Video       *model.Video
	Note        *model.ShortNote
	NoteRead    bool
}

// searchSubscribedFeed godoc
// @Summary 搜索已订阅内容
// @Description 在当前用户的订阅流里按相关度或时间检索标题、摘要、来源和正文。不读取去重快照。
// @Tags feed
// @Produce json
// @Param q query string true "搜索词"
// @Param rank query string false "排序" Enums(relevance,recent)
// @Param page query int false "页码"
// @Param limit query int false "每页数量"
// @Param source_id query string false "订阅 UUID"
// @Param group_id query string false "分组 UUID"
// @Param unread_only query bool false "只看未读"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/search [get]
func (h *Handler) searchSubscribedFeed(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	query, err := queryFromContext(c)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	rank := strings.ToLower(strings.TrimSpace(c.DefaultQuery("rank", feedSearchRankRelevance)))
	if rank != feedSearchRankRelevance && rank != feedSearchRankRecent {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "rank must be relevance or recent"))
		return
	}
	hits, total, err := h.service.SearchSubscribedFeed(user, query, rank)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	page := normalizedPage(query.Page)
	pageSize := normalizedPageSize(query.PageSize)
	httpx.OKMeta(c, http.StatusOK, hits, gin.H{
		"page":      page,
		"page_size": pageSize,
		"total":     total,
		"has_more":  int64(page*pageSize) < total,
		"rank":      rank,
	})
}

func (s *Service) SearchSubscribedFeed(user authctx.CurrentUser, query FeedQuery, rank string) ([]SubscribedSearchHit, int64, error) {
	needle := strings.ToLower(strings.TrimSpace(query.Search))
	if needle == "" {
		return nil, 0, apperr.BadRequest("validation.invalid_request", "q is required")
	}
	if rank != feedSearchRankRecent {
		rank = feedSearchRankRelevance
	}
	query.viewerID = user.ID
	query.Search = needle
	subscriptions, err := s.repo.ListSubscriptionsWithSources(user.ID, query)
	if err != nil {
		return nil, 0, err
	}
	followedUserIDs := make([]uuid.UUID, 0)
	if query.SourceID == uuid.Nil && query.GroupID == uuid.Nil && (query.SourceType == "" || query.SourceType == "internal_user") {
		followedUserIDs, err = s.repo.ListFollowedUserIDs(user.ID)
		if err != nil {
			return nil, 0, err
		}
	}
	sources := buildSubscribedFeedSources(subscriptions, followedUserIDs, query)
	now := time.Now().UTC()
	page := normalizedPage(query.Page)
	pageSize := normalizedPageSize(query.PageSize)
	offset := (page - 1) * pageSize
	window := offset + pageSize
	if window > feedSearchCandidateCap {
		window = feedSearchCandidateCap
	}

	candidates := make([]searchCandidate, 0)
	var total int64
	if query.ContentType != "blog" && len(sources.feedSourceIDs) > 0 && offset < feedSearchCandidateCap {
		rows, count, err := s.listFeedItemSearchCandidates(sources.feedSourceIDs, sources.visibleAfter, query, rank, needle, now, window)
		if err != nil {
			return nil, 0, err
		}
		total += count
		candidates = append(candidates, rows...)
	}
	if (query.ContentType == "" || query.ContentType == "blog") && offset < feedSearchCandidateCap {
		rows, count, err := s.listBlogSearchCandidates(sources, query, rank, needle, now, window)
		if err != nil {
			return nil, 0, err
		}
		total += count
		candidates = append(candidates, rows...)
	}
	if query.ContentType == "" {
		extra, err := s.listSupplementalSearchCandidates(user.ID, sources, query, needle, now)
		if err != nil {
			return nil, 0, err
		}
		total += int64(len(extra))
		candidates = append(candidates, extra...)
	}
	if len(candidates) == 0 {
		return []SubscribedSearchHit{}, total, nil
	}
	sortSearchCandidates(candidates, rank)
	if offset >= len(candidates) || offset >= feedSearchCandidateCap {
		return []SubscribedSearchHit{}, total, nil
	}
	end := offset + pageSize
	if end > len(candidates) {
		end = len(candidates)
	}
	hits, err := s.hydrateSearchCandidates(user.ID, candidates[offset:end], needle)
	if err != nil {
		return nil, 0, err
	}
	return hits, total, nil
}

func (s *Service) listFeedItemSearchCandidates(sourceIDs []uuid.UUID, visibleAfter map[uuid.UUID]time.Time, query FeedQuery, rank, needle string, now time.Time, window int) ([]searchCandidate, int64, error) {
	rows, err := s.scanFeedItemSearch(sourceIDs, visibleAfter, query, rank, needle, now, window, true)
	if err != nil && missingFeedSearchDocument(err) {
		rows, err = s.scanFeedItemSearch(sourceIDs, visibleAfter, query, rank, needle, now, window, false)
	}
	if err != nil {
		return nil, 0, err
	}
	count, err := s.countFeedItemSearch(sourceIDs, visibleAfter, query, needle, true)
	if err != nil && missingFeedSearchDocument(err) {
		count, err = s.countFeedItemSearch(sourceIDs, visibleAfter, query, needle, false)
	}
	if err != nil {
		return nil, 0, err
	}
	candidates := make([]searchCandidate, 0, len(rows))
	for _, row := range rows {
		_, field := feedSearchFieldWeight(row.Title, row.Summary, row.Author, row.SourceTitle, needle, true)
		candidates = append(candidates, searchCandidate{
			Kind: "feed_item", ID: row.ID, Title: row.Title, Summary: row.Summary, Author: row.Author,
			SourceTitle: row.SourceTitle, PublishedAt: row.PublishedAt, Score: row.SearchScore, MatchField: field,
		})
	}
	return candidates, count, nil
}

type feedItemSearchRow struct {
	ID          uuid.UUID `gorm:"column:id"`
	Title       string    `gorm:"column:title"`
	Summary     string    `gorm:"column:summary"`
	Author      string    `gorm:"column:author"`
	SourceTitle string    `gorm:"column:source_title"`
	PublishedAt time.Time `gorm:"column:published_at"`
	SearchScore float64   `gorm:"column:search_score"`
}

type blogSearchRow struct {
	ID          uuid.UUID `gorm:"column:id"`
	Title       string    `gorm:"column:title"`
	Summary     string    `gorm:"column:summary"`
	SourceTitle string    `gorm:"column:source_title"`
	PublishedAt time.Time `gorm:"column:published_at"`
	SearchScore float64   `gorm:"column:search_score"`
}

func (s *Service) feedItemSearchBase(sourceIDs []uuid.UUID, visibleAfter map[uuid.UUID]time.Time, query FeedQuery, needle string, useDocument bool) *gorm.DB {
	db := s.db.Table("feed_items").
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
		Where("feed_items.deleted_at IS NULL AND feed_sources.deleted_at IS NULL AND feed_sources.hidden = ?", false).
		Where("feed_items.feed_source_id IN ?", sourceIDs)
	db = applyFeedSourceVisibleAfter(db, visibleAfter)
	if query.Category != "" {
		db = db.Where(recommendationFeedItemCategorySQL()+" = ?", query.Category)
	}
	if query.LanguageCode != "" {
		db = db.Where("COALESCE(NULLIF(feed_items.language_code, ''), NULLIF(feed_sources.language_code, '')) = ?", query.LanguageCode)
	}
	if query.IsRead != nil {
		readClause := "EXISTS"
		if !*query.IsRead {
			readClause = "NOT EXISTS"
		}
		db = db.Where(readClause+" (SELECT 1 FROM feed_item_reads WHERE feed_item_reads.feed_item_id = feed_items.id AND feed_item_reads.user_id = ?)", query.viewerID)
	}
	matchSQL, matchArgs := feedItemSearchMatch(needle, useDocument)
	return db.Where(matchSQL, matchArgs...)
}

func (s *Service) scanFeedItemSearch(sourceIDs []uuid.UUID, visibleAfter map[uuid.UUID]time.Time, query FeedQuery, rank, needle string, now time.Time, window int, useDocument bool) ([]feedItemSearchRow, error) {
	scoreSQL, scoreArgs := feedSearchScoreSQL("feed_items.title", "feed_items.summary", "feed_items.author", "COALESCE(feed_sources.title, '')", "feed_items.published_at", s.db.Dialector.Name(), needle, now)
	order := "search_score DESC, feed_items.published_at DESC, feed_items.id ASC"
	if rank == feedSearchRankRecent {
		order = "feed_items.published_at DESC, feed_items.id ASC"
	}
	var rows []feedItemSearchRow
	err := s.feedItemSearchBase(sourceIDs, visibleAfter, query, needle, useDocument).
		Select("feed_items.id, feed_items.title, COALESCE(feed_items.summary, '') AS summary, COALESCE(feed_items.author, '') AS author, COALESCE(feed_sources.title, '') AS source_title, feed_items.published_at, ("+scoreSQL+") AS search_score", scoreArgs...).
		Order(order).Limit(window).Scan(&rows).Error
	return rows, err
}

func (s *Service) countFeedItemSearch(sourceIDs []uuid.UUID, visibleAfter map[uuid.UUID]time.Time, query FeedQuery, needle string, useDocument bool) (int64, error) {
	var count int64
	err := s.feedItemSearchBase(sourceIDs, visibleAfter, query, needle, useDocument).Count(&count).Error
	return count, err
}

func (s *Service) listBlogSearchCandidates(sources subscribedFeedSources, query FeedQuery, rank, needle string, now time.Time, window int) ([]searchCandidate, int64, error) {
	if len(sources.userIDs) == 0 && len(sources.channelIDs) == 0 && len(sources.collectionIDs) == 0 {
		return nil, 0, nil
	}
	base := s.blogSearchBase(sources, query, needle)
	var count int64
	if err := base.Session(&gorm.Session{}).Count(&count).Error; err != nil {
		return nil, 0, err
	}
	scoreSQL, scoreArgs := feedSearchScoreSQL("posts.title", "posts.summary", "''", "channels.name", "COALESCE(posts.published_at, posts.created_at)", s.db.Dialector.Name(), needle, now)
	order := "search_score DESC, published_at DESC, posts.id ASC"
	if rank == feedSearchRankRecent {
		order = "published_at DESC, posts.id ASC"
	}
	var rows []blogSearchRow
	err := base.Session(&gorm.Session{}).
		Select("posts.id, posts.title, COALESCE(posts.summary, '') AS summary, COALESCE(channels.name, '') AS source_title, COALESCE(posts.published_at, posts.created_at) AS published_at, ("+scoreSQL+") AS search_score", scoreArgs...).
		Order(order).Limit(window).Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	candidates := make([]searchCandidate, 0, len(rows))
	for _, row := range rows {
		_, field := feedSearchFieldWeight(row.Title, row.Summary, "", row.SourceTitle, needle, true)
		if field == "body" {
			field = "content"
		}
		candidates = append(candidates, searchCandidate{
			Kind: "post", ID: row.ID, Title: row.Title, Summary: row.Summary, SourceTitle: row.SourceTitle,
			PublishedAt: row.PublishedAt, Score: row.SearchScore, MatchField: field,
		})
	}
	return candidates, count, nil
}

func (s *Service) blogSearchBase(sources subscribedFeedSources, query FeedQuery, needle string) *gorm.DB {
	db := s.db.Table("content_entries AS posts").
		Joins("JOIN content_blog_extensions AS blog_extensions ON blog_extensions.content_id = posts.id").
		Joins("JOIN channels ON channels.id = posts.channel_id").
		Where("posts.kind = ? AND posts.status = ? AND posts.deleted_at IS NULL AND channels.deleted_at IS NULL", "blog", "published")
	sourceSQL, sourceArgs := subscribedContentSourceSQL(sources.userIDs, sources.channelIDs, sources.collectionIDs)
	db = db.Where(sourceSQL, sourceArgs...)
	visibilitySQL, visibilityArgs := subscribedContentVisibilitySQL(sources.userIDs, sources.channelIDs)
	db = db.Where(visibilitySQL, visibilityArgs...)
	pattern := escapedContainsPattern(needle)
	db = db.Where(`(LOWER(COALESCE(posts.title, '')) LIKE ? ESCAPE '\' OR LOWER(COALESCE(posts.summary, '')) LIKE ? ESCAPE '\' OR LOWER(COALESCE(channels.name, '')) LIKE ? ESCAPE '\' OR LOWER(COALESCE(channels.slug, '')) LIKE ? ESCAPE '\' OR LOWER(COALESCE(blog_extensions.content, '')) LIKE ? ESCAPE '\')`,
		pattern, pattern, pattern, pattern, pattern)
	if query.IsRead != nil && query.viewerID != uuid.Nil && s.db.Migrator().HasTable(&model.ContentLifecycleEvent{}) {
		readEvent := "EXISTS"
		if !*query.IsRead {
			readEvent = "NOT EXISTS"
		}
		db = db.Where(readEvent+" (SELECT 1 FROM content_lifecycle_events lifecycle WHERE lifecycle.user_id = ? AND lifecycle.content_type = ? AND lifecycle.content_id = posts.id AND lifecycle.deleted_at IS NULL)", query.viewerID, "blog")
	} else if query.IsRead != nil && *query.IsRead && !s.db.Migrator().HasTable(&model.ContentLifecycleEvent{}) {
		db = db.Where("1 = 0")
	}
	return db
}

func subscribedContentSourceSQL(userIDs, channelIDs, collectionIDs []uuid.UUID) (string, []any) {
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 3)
	if len(userIDs) > 0 {
		conditions = append(conditions, "posts.author_id IN ?")
		args = append(args, userIDs)
	}
	if len(channelIDs) > 0 {
		conditions = append(conditions, "posts.channel_id IN ?")
		args = append(args, channelIDs)
	}
	if len(collectionIDs) > 0 {
		conditions = append(conditions, "EXISTS (SELECT 1 FROM content_collection_memberships links WHERE links.content_id = posts.id AND links.collection_id IN ?)")
		args = append(args, collectionIDs)
	}
	if len(conditions) == 0 {
		return "1 = 0", nil
	}
	return "(" + strings.Join(conditions, " OR ") + ")", args
}

func subscribedContentVisibilitySQL(followedUserIDs, followedChannelIDs []uuid.UUID) (string, []any) {
	visibility := "COALESCE(posts.visibility, '') IN ?"
	args := []any{[]string{"", "public"}}
	conditions := make([]string, 0, 2)
	conditionArgs := make([]any, 0, 2)
	if len(followedUserIDs) > 0 {
		conditions = append(conditions, "posts.author_id IN ?")
		conditionArgs = append(conditionArgs, followedUserIDs)
	}
	if len(followedChannelIDs) > 0 {
		conditions = append(conditions, "posts.channel_id IN ?")
		conditionArgs = append(conditionArgs, followedChannelIDs)
	}
	if len(conditions) == 0 {
		return "(" + visibility + ")", args
	}
	return "(" + visibility + " OR (posts.visibility = ? AND (" + strings.Join(conditions, " OR ") + ")))", append(append(args, "followers"), conditionArgs...)
}

func (s *Service) listSupplementalSearchCandidates(userID uuid.UUID, sources subscribedFeedSources, query FeedQuery, needle string, now time.Time) ([]searchCandidate, error) {
	candidates := make([]searchCandidate, 0)
	notes, noteRead, err := s.listSubscribedShortNotes(userID, sources.userIDs)
	if err != nil {
		return nil, err
	}
	for i := range notes {
		note := notes[i]
		author := ""
		if note.User != nil {
			author = note.User.DisplayName
			if author == "" {
				author = note.User.Username
			}
		}
		weight, field := feedSearchFieldWeight(note.Content, "", author, "", needle, false)
		if weight == 0 {
			continue
		}
		if field == "title" {
			field = "content"
		}
		if query.IsRead != nil && noteRead[note.ID] != *query.IsRead {
			continue
		}
		copied := note
		candidates = append(candidates, searchCandidate{
			Kind: "short_note", ID: note.ID, Title: note.Content, Author: author, PublishedAt: note.CreatedAt,
			Score: feedSearchFreshness(note.CreatedAt, now) + float64(weight), MatchField: field, Note: &copied, NoteRead: noteRead[note.ID],
		})
	}
	videos, err := s.repo.ListPublishedVideosByScope(sources.userIDs, sources.channelIDs, sources.collectionIDs, "")
	if err != nil {
		return nil, err
	}
	for i := range videos {
		video := videos[i]
		weight, field := feedSearchFieldWeight(video.Title, video.Description, "", "", needle, false)
		if weight == 0 {
			continue
		}
		copied := video
		candidates = append(candidates, searchCandidate{
			Kind: "video", ID: video.ID, Title: video.Title, Summary: video.Description, PublishedAt: video.CreatedAt,
			Score: feedSearchFreshness(video.CreatedAt, now) + float64(weight), MatchField: field, Video: &copied,
		})
	}
	podcasts, err := s.listScopedPosts(sources, "podcast")
	if err != nil {
		return nil, err
	}
	seen := map[uuid.UUID]struct{}{}
	for i := range podcasts {
		post := podcasts[i]
		if _, ok := seen[post.ID]; ok {
			continue
		}
		seen[post.ID] = struct{}{}
		source := ""
		if post.Channel != nil {
			source = post.Channel.Name
		}
		weight, field := feedSearchFieldWeight(post.Title, post.Summary, "", source, needle, strings.Contains(strings.ToLower(post.Content), needle))
		if weight == 0 {
			continue
		}
		if field == "body" {
			field = "content"
		}
		copied := post
		published := postTimelinePublishedAt(post)
		candidates = append(candidates, searchCandidate{
			Kind: "podcast", ID: post.ID, Title: post.Title, Summary: post.Summary, SourceTitle: source, Body: post.Content,
			PublishedAt: published, Score: feedSearchFreshness(published, now) + float64(weight), MatchField: field, Post: &copied,
		})
	}
	return candidates, nil
}

func (s *Service) listScopedPosts(sources subscribedFeedSources, contentType string) ([]model.Post, error) {
	posts := make([]model.Post, 0)
	userPosts, err := s.repo.ListPublishedPostsByUserIDs(sources.userIDs, contentType)
	if err != nil {
		return nil, err
	}
	posts = append(posts, userPosts...)
	channelPosts, err := s.repo.ListPublishedPostsByChannelIDs(sources.channelIDs, contentType)
	if err != nil {
		return nil, err
	}
	posts = append(posts, channelPosts...)
	collectionPosts, err := s.repo.ListPublishedPostsByCollectionIDs(sources.collectionIDs, contentType)
	if err != nil {
		return nil, err
	}
	return append(posts, collectionPosts...), nil
}

func sortSearchCandidates(candidates []searchCandidate, rank string) {
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if rank == feedSearchRankRecent || left.Score == right.Score {
			if !left.PublishedAt.Equal(right.PublishedAt) {
				return left.PublishedAt.After(right.PublishedAt)
			}
			return left.ID.String() < right.ID.String()
		}
		return left.Score > right.Score
	})
}

func (s *Service) hydrateSearchCandidates(userID uuid.UUID, page []searchCandidate, needle string) ([]SubscribedSearchHit, error) {
	feedIDs := make([]uuid.UUID, 0)
	postIDs := make([]uuid.UUID, 0)
	podcastIDs := make([]uuid.UUID, 0)
	for _, candidate := range page {
		switch candidate.Kind {
		case "feed_item":
			feedIDs = append(feedIDs, candidate.ID)
		case "post":
			postIDs = append(postIDs, candidate.ID)
		case "podcast":
			podcastIDs = append(podcastIDs, candidate.ID)
		}
	}
	feedItems, err := s.repo.LoadFeedItemsForTimelinePage(feedIDs)
	if err != nil {
		return nil, err
	}
	loaded := make([]model.FeedItem, 0, len(feedItems))
	for _, item := range feedItems {
		loaded = append(loaded, item)
	}
	readMap, err := s.readMap(userID, loaded)
	if err != nil {
		return nil, err
	}
	posts, err := s.repo.ListRecommendationPostsByIDs(postIDs)
	if err != nil {
		return nil, err
	}
	postByID := make(map[uuid.UUID]model.Post, len(posts))
	for _, post := range posts {
		postByID[post.ID] = post
	}
	bodies, err := s.blogSearchBodies(postIDs)
	if err != nil {
		return nil, err
	}
	episodes, err := s.repo.ListPodcastEpisodesByPostIDs(podcastIDs)
	if err != nil {
		return nil, err
	}
	episodeByPost := make(map[uuid.UUID]model.PodcastEpisode, len(episodes))
	for _, episode := range episodes {
		episodeByPost[episode.PostID] = episode
	}
	contentRead, err := s.subscriptionContentReadMap(userID)
	if err != nil {
		return nil, err
	}
	hits := make([]SubscribedSearchHit, 0, len(page))
	for _, candidate := range page {
		hit := SubscribedSearchHit{SearchMatchField: candidate.MatchField}
		switch candidate.Kind {
		case "feed_item":
			item, ok := feedItems[candidate.ID]
			if !ok {
				continue
			}
			body := strings.Join([]string{item.ReaderHTML, item.FullTextHTML, item.FeedContentHTML}, " ")
			hit.TimelineItemDTO = TimelineItemDTO{Type: timelineFeedItemType(&item), FeedItem: &item, PublishedAt: item.PublishedAt, IsRead: readMap[item.ID]}
			hit.SearchSnippet = feedSearchSnippetFor(candidate.MatchField, item.Title, item.Summary, body, needle)
		case "post":
			post, ok := postByID[candidate.ID]
			if !ok {
				continue
			}
			hit.TimelineItemDTO = TimelineItemDTO{Type: "post", Post: timelinePostDTO(post, PostEngagementCount{}), PublishedAt: postTimelinePublishedAt(post), IsRead: contentRead["blog"][post.ID]}
			hit.SearchSnippet = feedSearchSnippetFor(candidate.MatchField, post.Title, post.Summary, bodies[post.ID], needle)
		case "podcast":
			if candidate.Post == nil {
				continue
			}
			episode, ok := episodeByPost[candidate.ID]
			if ok {
				episode.Post = candidate.Post
				hit.TimelineItemDTO = TimelineItemDTO{Type: "podcast_episode", PodcastEpisode: timelinePodcastEpisodeDTO(episode), PublishedAt: candidate.PublishedAt}
			} else {
				hit.TimelineItemDTO = TimelineItemDTO{Type: "post", Post: timelinePostDTO(*candidate.Post, PostEngagementCount{}), PublishedAt: candidate.PublishedAt}
			}
			hit.SearchSnippet = feedSearchSnippetFor(candidate.MatchField, candidate.Title, candidate.Summary, candidate.Body, needle)
		case "video":
			if candidate.Video == nil {
				continue
			}
			hit.TimelineItemDTO = TimelineItemDTO{Type: "video", Video: timelineVideoDTO(*candidate.Video), PublishedAt: candidate.PublishedAt, IsRead: contentRead["video"][candidate.Video.ID]}
			hit.SearchSnippet = feedSearchSnippetFor(candidate.MatchField, candidate.Title, candidate.Summary, "", needle)
		case "short_note":
			if candidate.Note == nil {
				continue
			}
			hit.TimelineItemDTO = TimelineItemDTO{Type: "short_note", ShortNote: timelineShortNoteDTO(*candidate.Note), PublishedAt: candidate.PublishedAt, IsRead: candidate.NoteRead}
			hit.SearchSnippet = feedSearchSnippet(candidate.Note.Content, needle)
			hit.SearchMatchField = "content"
		default:
			continue
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

func (s *Service) blogSearchBodies(ids []uuid.UUID) (map[uuid.UUID]string, error) {
	bodies := make(map[uuid.UUID]string, len(ids))
	if len(ids) == 0 {
		return bodies, nil
	}
	var rows []struct {
		ContentID uuid.UUID `gorm:"column:content_id"`
		Content   string    `gorm:"column:content"`
	}
	if err := s.db.Table("content_blog_extensions").Select("content_id, content").Where("content_id IN ?", ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		bodies[row.ContentID] = row.Content
	}
	return bodies, nil
}

func feedItemSearchMatch(needle string, useDocument bool) (string, []any) {
	pattern := escapedContainsPattern(needle)
	if useDocument {
		return `(public.atoman_feed_search_document_v1(feed_items.title, feed_items.summary, feed_items.author, feed_items.reader_html, feed_items.full_text_html, feed_items.feed_content_html) LIKE ? ESCAPE '\' OR LOWER(COALESCE(feed_sources.title, '')) LIKE ? ESCAPE '\' OR LOWER(COALESCE(feed_sources.rss_url, '')) LIKE ? ESCAPE '\')`,
			[]any{pattern, pattern, pattern}
	}
	return `(LOWER(COALESCE(feed_items.title, '')) LIKE ? ESCAPE '\' OR LOWER(COALESCE(feed_items.summary, '')) LIKE ? ESCAPE '\' OR LOWER(COALESCE(feed_items.author, '')) LIKE ? ESCAPE '\' OR LOWER(COALESCE(feed_items.reader_html, '')) LIKE ? ESCAPE '\' OR LOWER(COALESCE(feed_items.full_text_html, '')) LIKE ? ESCAPE '\' OR LOWER(COALESCE(feed_items.feed_content_html, '')) LIKE ? ESCAPE '\' OR LOWER(COALESCE(feed_sources.title, '')) LIKE ? ESCAPE '\' OR LOWER(COALESCE(feed_sources.rss_url, '')) LIKE ? ESCAPE '\')`,
		[]any{pattern, pattern, pattern, pattern, pattern, pattern, pattern, pattern}
}

func feedSearchScoreSQL(titleExpr, summaryExpr, authorExpr, sourceExpr, publishedExpr, dialect, needle string, now time.Time) (string, []any) {
	exact := strings.ToLower(strings.TrimSpace(needle))
	prefix := strings.TrimPrefix(escapedContainsPattern(needle), "%")
	contains := escapedContainsPattern(needle)
	freshSQL, freshArgs := feedSearchFreshnessSQL(dialect, publishedExpr, now)
	args := []any{exact, prefix, contains, contains, contains, contains}
	args = append(args, freshArgs...)
	return `CASE
		WHEN LOWER(COALESCE(` + titleExpr + `, '')) = ? THEN 100
		WHEN LOWER(COALESCE(` + titleExpr + `, '')) LIKE ? ESCAPE '\' THEN 80
		WHEN LOWER(COALESCE(` + titleExpr + `, '')) LIKE ? ESCAPE '\' THEN 60
		WHEN LOWER(COALESCE(` + summaryExpr + `, '')) LIKE ? ESCAPE '\' THEN 40
		WHEN LOWER(COALESCE(` + authorExpr + `, '')) LIKE ? ESCAPE '\' OR LOWER(COALESCE(` + sourceExpr + `, '')) LIKE ? ESCAPE '\' THEN 30
		ELSE 20
	END + ` + freshSQL, args
}

func feedSearchFreshnessSQL(dialect, publishedExpr string, now time.Time) (string, []any) {
	if dialect != "postgres" && dialect != "pgx" {
		return "0", nil
	}
	return `CASE WHEN ` + publishedExpr + ` >= ? THEN LEAST(10, GREATEST(0, 10.0 * (1.0 - EXTRACT(EPOCH FROM (?::timestamptz - ` + publishedExpr + `)) / 1209600.0))) ELSE 0 END`,
		[]any{now.Add(-14 * 24 * time.Hour), now}
}

func feedSearchFieldWeight(title, summary, author, source, needle string, matchedBody bool) (int, string) {
	needle = strings.ToLower(strings.TrimSpace(needle))
	if needle == "" {
		return 0, ""
	}
	titleFold := strings.ToLower(title)
	switch {
	case titleFold == needle:
		return 100, "title"
	case strings.HasPrefix(titleFold, needle):
		return 80, "title"
	case strings.Contains(titleFold, needle):
		return 60, "title"
	case strings.Contains(strings.ToLower(summary), needle):
		return 40, "summary"
	case author != "" && strings.Contains(strings.ToLower(author), needle):
		return 30, "author"
	case source != "" && strings.Contains(strings.ToLower(source), needle):
		return 30, "source"
	case matchedBody:
		return 20, "body"
	default:
		return 0, ""
	}
}

func feedSearchFreshness(published, now time.Time) float64 {
	if published.IsZero() {
		return 0
	}
	age := now.Sub(published)
	if age <= 0 {
		return 10
	}
	const horizon = 14 * 24 * time.Hour
	if age >= horizon {
		return 0
	}
	return 10 * (1 - float64(age)/float64(horizon))
}

func missingFeedSearchDocument(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "atoman_feed_search_document_v1") && strings.Contains(message, "does not exist")
}

var feedSearchTagPattern = regexp.MustCompile(`(?s)<[^>]*>`)

func stripFeedSearchHTML(value string) string {
	plain := feedSearchTagPattern.ReplaceAllString(value, " ")
	return strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&nbsp;", " ").Replace(plain)
}

func feedSearchSnippetFor(field, title, summary, body, needle string) string {
	switch field {
	case "title":
		if strings.Contains(strings.ToLower(summary), strings.ToLower(needle)) {
			return feedSearchSnippet(summary, needle)
		}
		return ""
	case "summary":
		return feedSearchSnippet(summary, needle)
	case "content", "body":
		return feedSearchSnippet(body, needle)
	default:
		return ""
	}
}

func feedSearchSnippet(value, needle string) string {
	plain := strings.Join(strings.Fields(stripFeedSearchHTML(value)), " ")
	if plain == "" {
		return ""
	}
	needle = strings.ToLower(strings.TrimSpace(needle))
	lower := strings.ToLower(plain)
	index := strings.Index(lower, needle)
	runes := []rune(plain)
	const radius = 48
	if index < 0 {
		if len(runes) > 140 {
			return string(runes[:140]) + "…"
		}
		return plain
	}
	start := utf8.RuneCountInString(lower[:index]) - radius
	if start < 0 {
		start = 0
	}
	end := utf8.RuneCountInString(lower[:index]) + utf8.RuneCountInString(needle) + radius
	if end > len(runes) {
		end = len(runes)
	}
	snippet := string(runes[start:end])
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(runes) {
		snippet += "…"
	}
	return snippet
}
