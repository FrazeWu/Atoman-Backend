package feed

import (
	"atoman/internal/feedclass"
	"atoman/internal/model"
	blogmodule "atoman/internal/modules/blog"
	contentmodule "atoman/internal/modules/content"
	"atoman/internal/platform/apperr"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Repo struct{ db *gorm.DB }

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

type PostEngagementCount struct {
	PostID         uuid.UUID `gorm:"column:post_id"`
	LikesCount     int64     `gorm:"column:likes_count"`
	CommentsCount  int64     `gorm:"column:comments_count"`
	BookmarksCount int64     `gorm:"column:bookmarks_count"`
	RatingScore    float64   `gorm:"column:rating_score"`
	RatingCount    int64     `gorm:"column:rating_count"`
}

type ExploreSourceRow struct {
	ID                uuid.UUID                 `json:"id"`
	Title             string                    `json:"title"`
	RSSURL            string                    `json:"rss_url"`
	CoverURL          string                    `json:"cover_url"`
	Category          string                    `json:"category"`
	LanguageCode      string                    `json:"language_code,omitempty"`
	SubscriptionCount int64                     `json:"subscription_count"`
	RecentItemCount   int64                     `json:"recent_item_count"`
	LastPublishedAt   *time.Time                `json:"last_published_at"`
	RecentItems       []ExploreSourceRecentItem `json:"recent_items"`
	Subscribed        bool                      `json:"subscribed" gorm:"-"`
}

type ExploreSourceRecentItem struct {
	ID            uuid.UUID `json:"id"`
	Title         string    `json:"title"`
	Link          string    `json:"link"`
	PublishedAt   time.Time `json:"published_at"`
	EnclosureType string    `json:"enclosure_type"`
}

func (r *Repo) ListSubscriptionsWithSources(userID uuid.UUID, query FeedQuery) ([]model.Subscription, error) {
	db := r.db.Model(&model.Subscription{}).
		Joins("JOIN feed_sources ON feed_sources.id = subscriptions.feed_source_id").
		Where("subscriptions.user_id = ? AND feed_sources.hidden = ?", userID, false)
	if query.SourceType != "" {
		db = db.Where("feed_sources.source_type = ?", query.SourceType)
	}
	if query.SourceID != uuid.Nil {
		db = db.Where("subscriptions.id = ?", query.SourceID)
	}
	if query.GroupID != uuid.Nil {
		db = db.Where("subscriptions.subscription_group_id = ?", query.GroupID)
	}
	var subscriptions []model.Subscription
	err := db.Preload("FeedSource").Find(&subscriptions).Error
	return subscriptions, err
}

func (r *Repo) ListFollowedUserIDs(userID uuid.UUID) ([]uuid.UUID, error) {
	if !r.db.Migrator().HasTable(&model.Follow{}) {
		return []uuid.UUID{}, nil
	}
	var ids []uuid.UUID
	err := r.db.Model(&model.Follow{}).
		Where("follower_id = ?", userID).
		Pluck("following_id", &ids).Error
	return ids, err
}

func (r *Repo) GetPublicExternalFeedSource(feedSourceID uuid.UUID) (model.FeedSource, error) {
	var source model.FeedSource
	err := r.db.Where("id = ? AND source_type = ? AND hidden = ?", feedSourceID, "external_rss", false).First(&source).Error
	return source, err
}

func (r *Repo) ListVisibleFeedSources(query FeedQuery) ([]model.FeedSource, error) {
	db := r.db.Model(&model.FeedSource{}).Where("hidden = ?", false)
	if query.SourceType != "" {
		db = db.Where("source_type = ?", query.SourceType)
	}
	if query.SourceID != uuid.Nil {
		db = db.Where("id = ?", query.SourceID)
	}
	var sources []model.FeedSource
	err := db.Find(&sources).Error
	return sources, err
}

func (r *Repo) listPublishedCanonicalBlogPosts(scope string, ids []uuid.UUID) ([]model.Post, error) {
	if len(ids) == 0 {
		return []model.Post{}, nil
	}
	query := blogmodule.CanonicalBlogPostsQuery(r.db).
		Where("posts.status = ?", "published")
	switch scope {
	case "user_id":
		query = query.Where("posts.author_id IN ?", ids)
	case "channel_id":
		query = query.Where("posts.channel_id IN ?", ids)
	case "collection_id":
		query = query.Where("EXISTS (SELECT 1 FROM content_collection_memberships links WHERE links.content_id = posts.id AND links.collection_id IN ?)", ids)
	default:
		return nil, fmt.Errorf("unsupported canonical blog scope %q", scope)
	}
	return blogmodule.LoadCanonicalBlogPosts(r.db, query)
}

func (r *Repo) listPublishedCanonicalPodcastPosts(scope string, ids []uuid.UUID) ([]model.Post, error) {
	if len(ids) == 0 {
		return []model.Post{}, nil
	}
	query := contentmodule.PodcastQuery(r.db).Where("posts.status = ?", "published")
	switch scope {
	case "user_id":
		query = query.Where(contentmodule.PodcastAuthorColumn(r.db)+" IN ?", ids)
	case "channel_id":
		query = query.Where("posts.channel_id IN ?", ids)
	case "collection_id":
		query = query.Where("EXISTS (SELECT 1 FROM content_collection_memberships links WHERE links.content_id = posts.id AND links.collection_id IN ?)", ids)
	default:
		return nil, fmt.Errorf("unsupported canonical podcast scope %q", scope)
	}
	episodes, err := contentmodule.LoadPodcastEpisodes(r.db, query)
	if err != nil {
		return nil, err
	}
	posts := make([]model.Post, 0, len(episodes))
	for _, episode := range episodes {
		if episode.Post != nil {
			posts = append(posts, *episode.Post)
		}
	}
	return posts, nil
}

func (r *Repo) ListPublishedPostsByUserIDs(userIDs []uuid.UUID, contentType string) ([]model.Post, error) {
	if len(userIDs) == 0 {
		return []model.Post{}, nil
	}
	switch contentType {
	case "blog":
		return r.listPublishedCanonicalBlogPosts("user_id", userIDs)
	case "podcast":
		return r.listPublishedCanonicalPodcastPosts("user_id", userIDs)
	case "video":
		return []model.Post{}, nil
	default:
		podcastPosts, err := r.listPublishedCanonicalPodcastPosts("user_id", userIDs)
		if err != nil {
			return nil, err
		}
		blogPosts, err := r.listPublishedCanonicalBlogPosts("user_id", userIDs)
		return append(podcastPosts, blogPosts...), err
	}
}

func (r *Repo) ListPublishedPostsByChannelIDs(channelIDs []uuid.UUID, contentType string) ([]model.Post, error) {
	if len(channelIDs) == 0 {
		return []model.Post{}, nil
	}
	switch contentType {
	case "blog":
		return r.listPublishedCanonicalBlogPosts("channel_id", channelIDs)
	case "podcast":
		return r.listPublishedCanonicalPodcastPosts("channel_id", channelIDs)
	case "video":
		return []model.Post{}, nil
	default:
		podcastPosts, err := r.listPublishedCanonicalPodcastPosts("channel_id", channelIDs)
		if err != nil {
			return nil, err
		}
		blogPosts, err := r.listPublishedCanonicalBlogPosts("channel_id", channelIDs)
		return append(podcastPosts, blogPosts...), err
	}
}

func (r *Repo) ListPublishedPostsByCollectionIDs(collectionIDs []uuid.UUID, contentType string) ([]model.Post, error) {
	if len(collectionIDs) == 0 {
		return []model.Post{}, nil
	}
	switch contentType {
	case "blog":
		return r.listPublishedCanonicalBlogPosts("collection_id", collectionIDs)
	case "podcast":
		return r.listPublishedCanonicalPodcastPosts("collection_id", collectionIDs)
	case "video":
		return []model.Post{}, nil
	default:
		podcastPosts, err := r.listPublishedCanonicalPodcastPosts("collection_id", collectionIDs)
		if err != nil {
			return nil, err
		}
		blogPosts, err := r.listPublishedCanonicalBlogPosts("collection_id", collectionIDs)
		return append(podcastPosts, blogPosts...), err
	}

}

func filterPostContentType(db *gorm.DB, contentType string) *gorm.DB {
	if contentType == "podcast" || contentType == "video" {
		return db.Where("1 = 0")
	}
	return db
}

func hasCanonicalBlogExtensions(db *gorm.DB) bool {
	return db.Migrator().HasTable(&model.ContentBlogExtension{})
}
func (r *Repo) ListPodcastEpisodesByPostIDs(postIDs []uuid.UUID) ([]model.PodcastEpisode, error) {
	if len(postIDs) == 0 {
		return []model.PodcastEpisode{}, nil
	}
	return contentmodule.LoadPodcastEpisodes(r.db, contentmodule.PodcastQuery(r.db).Where("episodes.legacy_post_id IN ?", postIDs))
}

func (r *Repo) ListPublishedVideosByScope(userIDs, channelIDs, collectionIDs []uuid.UUID, contentType string) ([]model.Video, error) {
	if contentType != "" && contentType != "video" {
		return []model.Video{}, nil
	}
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 3)
	if len(userIDs) > 0 {
		conditions = append(conditions, contentmodule.VideoAuthorColumn(r.db)+" IN ?")
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
		return []model.Video{}, nil
	}
	query := contentmodule.VideoQuery(r.db).
		Where("posts.status = ? AND posts.visibility IN ?", "published", []string{"public", "followers"}).
		Where("("+strings.Join(conditions, " OR ")+")", args...).
		Order("videos.created_at DESC, videos.video_id DESC")
	return contentmodule.LoadVideos(r.db, query)
}

func (r *Repo) ListPostEngagementCounts(postIDs []uuid.UUID) ([]PostEngagementCount, error) {
	if len(postIDs) == 0 {
		return []PostEngagementCount{}, nil
	}
	canonicalIDs := append([]uuid.UUID(nil), postIDs...)
	type legacyMapping struct {
		ContentID    uuid.UUID `gorm:"column:content_id"`
		LegacyPostID uuid.UUID `gorm:"column:legacy_post_id"`
	}
	var mappings []legacyMapping
	if err := r.db.Table("content_episode_extensions").
		Select("content_id, legacy_post_id").Where("legacy_post_id IN ?", postIDs).Scan(&mappings).Error; err != nil {
		return nil, err
	}
	for _, mapping := range mappings {
		canonicalIDs = append(canonicalIDs, mapping.ContentID)
	}
	counts, err := r.listEngagementCounts(canonicalIDs)
	if err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]PostEngagementCount, len(counts))
	for _, count := range counts {
		byID[count.PostID] = count
	}
	for _, mapping := range mappings {
		if count, ok := byID[mapping.ContentID]; ok {
			count.PostID = mapping.LegacyPostID
			byID[mapping.LegacyPostID] = count
		}
	}
	result := make([]PostEngagementCount, 0, len(postIDs))
	for _, postID := range postIDs {
		if count, ok := byID[postID]; ok {
			result = append(result, count)
		}
	}
	return result, nil
}

func (r *Repo) ListCanonicalBlogEngagementCounts(postIDs []uuid.UUID) ([]PostEngagementCount, error) {
	return r.listEngagementCounts(postIDs)
}

func (r *Repo) listEngagementCounts(postIDs []uuid.UUID) ([]PostEngagementCount, error) {
	if len(postIDs) == 0 {
		return []PostEngagementCount{}, nil
	}
	selectSQL := `posts.id AS post_id,
		(SELECT COUNT(*) FROM likes WHERE likes.target_type = 'post' AND likes.target_id = posts.id AND likes.deleted_at IS NULL) AS likes_count,
		COALESCE((SELECT targets.comment_count FROM discussion_targets AS targets WHERE targets.kind = 'blog_post' AND targets.resource_id = posts.id AND targets.deleted_at IS NULL LIMIT 1), 0) AS comments_count`
	if r.db.Migrator().HasTable(&model.Bookmark{}) {
		selectSQL += `,
			(SELECT COUNT(*) FROM bookmarks WHERE bookmarks.content_id = posts.id AND bookmarks.deleted_at IS NULL) AS bookmarks_count`
	} else {
		selectSQL += `, 0 AS bookmarks_count`
	}
	var counts []PostEngagementCount
	query := r.db.Table("content_entries AS posts")
	if err := query.Select(selectSQL).Where("posts.id IN ?", postIDs).Scan(&counts).Error; err != nil {
		return nil, err
	}
	if r.db.Migrator().HasTable(&model.PostRating{}) {
		type ratingAggregate struct {
			PostID      uuid.UUID `gorm:"column:post_id"`
			RatingScore float64   `gorm:"column:rating_score"`
			RatingCount int64     `gorm:"column:rating_count"`
		}
		var ratings []ratingAggregate
		if err := r.db.Model(&model.PostRating{}).
			Select("content_id AS post_id, AVG(score) AS rating_score, COUNT(*) AS rating_count").
			Where("content_id IN ?", postIDs).
			Group("content_id").Scan(&ratings).Error; err != nil {
			return nil, err
		}
		countsByPostID := make(map[uuid.UUID]*PostEngagementCount, len(counts))
		for index := range counts {
			countsByPostID[counts[index].PostID] = &counts[index]
		}
		for _, rating := range ratings {
			if count := countsByPostID[rating.PostID]; count != nil {
				count.RatingScore = math.Round(rating.RatingScore*10) / 10
				count.RatingCount = rating.RatingCount
			}
		}
	}
	return counts, nil
}

func (r *Repo) ListSubscribedBlogPosts(
	userIDs []uuid.UUID,
	channelIDs []uuid.UUID,
	collectionIDs []uuid.UUID,
	followedUserIDs []uuid.UUID,
	followedChannelIDs []uuid.UUID,
	query FeedQuery,
) ([]model.Post, int64, error) {
	if len(userIDs) == 0 && len(channelIDs) == 0 && len(collectionIDs) == 0 {
		return []model.Post{}, 0, nil
	}

	buildQuery := func() *gorm.DB {
		db := blogmodule.CanonicalBlogPostsQuery(r.db).
			Joins("JOIN channels ON channels.id = posts.channel_id").
			Where("posts.status = ? AND channels.deleted_at IS NULL", "published")

		sourceConditions := make([]string, 0, 3)
		sourceArgs := make([]interface{}, 0, 3)
		if len(userIDs) > 0 {
			sourceConditions = append(sourceConditions, "posts.author_id IN ?")
			sourceArgs = append(sourceArgs, userIDs)
		}
		if len(channelIDs) > 0 {
			sourceConditions = append(sourceConditions, "posts.channel_id IN ?")
			sourceArgs = append(sourceArgs, channelIDs)
		}
		if len(collectionIDs) > 0 {
			sourceConditions = append(sourceConditions, "EXISTS (SELECT 1 FROM content_collection_memberships links WHERE links.content_id = posts.id AND links.collection_id IN ?)")
			sourceArgs = append(sourceArgs, collectionIDs)
		}
		db = db.Where("("+strings.Join(sourceConditions, " OR ")+")", sourceArgs...)

		visibility := "COALESCE(posts.visibility, '') IN ?"
		visibilityArgs := []interface{}{[]string{"", "public"}}
		followerConditions := make([]string, 0, 2)
		if len(followedUserIDs) > 0 {
			followerConditions = append(followerConditions, "posts.author_id IN ?")
			visibilityArgs = append(visibilityArgs, followedUserIDs)
		}
		if len(followedChannelIDs) > 0 {
			followerConditions = append(followerConditions, "posts.channel_id IN ?")
			visibilityArgs = append(visibilityArgs, followedChannelIDs)
		}
		if len(followerConditions) > 0 {
			visibility += " OR (posts.visibility = ? AND (" + strings.Join(followerConditions, " OR ") + "))"
			visibilityArgs = append([]interface{}{[]string{"", "public"}, "followers"}, visibilityArgs[1:]...)
		}
		db = db.Where("("+visibility+")", visibilityArgs...)

		if search := strings.ToLower(strings.TrimSpace(query.Search)); search != "" {
			like := "%" + search + "%"
			db = db.Where(
				"LOWER(posts.title) LIKE ? OR LOWER(posts.summary) LIKE ? OR LOWER(channels.name) LIKE ? OR LOWER(channels.slug) LIKE ?",
				like, like, like, like,
			)
		}
		return db
	}

	var total int64
	if err := buildQuery().Distinct("posts.id").Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page := normalizedPage(query.Page)
	pageSize := normalizedPageSize(query.PageSize)
	posts, err := blogmodule.LoadCanonicalBlogPosts(r.db, buildQuery().
		Order("COALESCE(posts.published_at, posts.created_at) DESC").
		Order("posts.created_at DESC").
		Order("posts.id DESC").
		Offset((page-1)*pageSize).
		Limit(pageSize))
	return posts, total, err
}

func (r *Repo) ListFeedItemsBySourceIDs(feedSourceIDs []uuid.UUID, visibleAfter ...map[uuid.UUID]time.Time) ([]model.FeedItem, error) {
	if len(feedSourceIDs) == 0 {
		return []model.FeedItem{}, nil
	}
	var items []model.FeedItem
	db := r.db.Preload("FeedSource").
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
		Where("feed_items.feed_source_id IN ? AND feed_sources.hidden = ?", feedSourceIDs, false)
	if len(visibleAfter) > 0 {
		db = applyFeedSourceVisibleAfter(db, visibleAfter[0])
	}
	err := db.Order("feed_items.published_at DESC, feed_items.id DESC").Find(&items).Error
	return items, err
}

func (r *Repo) ListFeedItemsBySourceIDsPaged(feedSourceIDs []uuid.UUID, limit int, offset int, visibleAfter ...map[uuid.UUID]time.Time) ([]model.FeedItem, error) {
	if len(feedSourceIDs) == 0 {
		return []model.FeedItem{}, nil
	}
	var items []model.FeedItem
	db := r.db.Preload("FeedSource").
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
		Where("feed_items.feed_source_id IN ? AND feed_sources.hidden = ?", feedSourceIDs, false)
	if len(visibleAfter) > 0 {
		db = applyFeedSourceVisibleAfter(db, visibleAfter[0])
	}
	err := db.Order("feed_items.published_at DESC, feed_items.id DESC").
		Offset(offset).
		Limit(limit).
		Find(&items).Error
	return items, err
}

func (r *Repo) ListFeedItemsBySourceIDsFiltered(feedSourceIDs []uuid.UUID, query FeedQuery) ([]model.FeedItem, error) {
	if len(feedSourceIDs) == 0 {
		return []model.FeedItem{}, nil
	}
	var items []model.FeedItem
	db := r.buildFeedItemsBySourceIDsQuery(feedSourceIDs, query).
		Preload("FeedSource").
		Order("feed_items.published_at DESC, feed_items.id DESC").
		Offset((normalizedPage(query.Page) - 1) * normalizedPageSize(query.PageSize)).
		Limit(normalizedPageSize(query.PageSize))
	return items, db.Find(&items).Error
}

func (r *Repo) CountFeedItemsBySourceIDsFiltered(feedSourceIDs []uuid.UUID, query FeedQuery) (int64, error) {
	if len(feedSourceIDs) == 0 {
		return 0, nil
	}
	var count int64
	return count, r.buildFeedItemsBySourceIDsQuery(feedSourceIDs, query).Count(&count).Error
}

func applyFeedSourceVisibleAfter(db *gorm.DB, visibleAfter map[uuid.UUID]time.Time) *gorm.DB {
	if len(visibleAfter) == 0 {
		return db
	}
	conditions := make([]string, 0, len(visibleAfter))
	args := make([]any, 0, len(visibleAfter)*2)
	for sourceID, resumedAfter := range visibleAfter {
		if resumedAfter.IsZero() {
			continue
		}
		conditions = append(conditions, "(feed_items.feed_source_id = ? AND feed_items.published_at >= ?)")
		args = append(args, sourceID, resumedAfter)
	}
	if len(conditions) == 0 {
		return db
	}
	return db.Where(strings.Join(conditions, " OR "), args...)
}

func (r *Repo) buildFeedItemsBySourceIDsQuery(feedSourceIDs []uuid.UUID, query FeedQuery) *gorm.DB {
	db := r.db.Model(&model.FeedItem{}).
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
		Where("feed_items.feed_source_id IN ? AND feed_sources.hidden = ?", feedSourceIDs, false)
	db = applyFeedSourceVisibleAfter(db, query.sourceVisibleAfter)
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
	if search := strings.TrimSpace(query.Search); search != "" {
		like := escapedContainsPattern(search)
		db = db.Where(
			`(LOWER(feed_items.title) LIKE ? ESCAPE '\' OR
			 LOWER(feed_items.summary) LIKE ? ESCAPE '\' OR
			 LOWER(feed_items.reader_html) LIKE ? ESCAPE '\' OR
			 LOWER(feed_items.full_text_html) LIKE ? ESCAPE '\' OR
			 LOWER(feed_sources.title) LIKE ? ESCAPE '\' OR
			 LOWER(feed_sources.rss_url) LIKE ? ESCAPE '\')`,
			like, like, like, like, like, like,
		)
	}
	return db
}

func (r *Repo) ListFeedItemsBySourceID(feedSourceID uuid.UUID, limit int, offset int) ([]model.FeedItem, error) {
	var items []model.FeedItem
	err := r.db.Preload("FeedSource").
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
		Where("feed_items.feed_source_id = ?", feedSourceID).
		Where("feed_sources.hidden = ?", false).
		Order("feed_items.published_at DESC, feed_items.id DESC").
		Offset(offset).
		Limit(limit).
		Find(&items).Error
	return items, err
}

func (r *Repo) CountFeedItemsBySourceID(feedSourceID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.Model(&model.FeedItem{}).
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
		Where("feed_items.feed_source_id = ?", feedSourceID).
		Where("feed_sources.hidden = ?", false).
		Count(&count).Error
	return count, err
}

func (r *Repo) CountFeedItemsBySourceIDs(feedSourceIDs []uuid.UUID, visibleAfter ...map[uuid.UUID]time.Time) (int64, error) {
	if len(feedSourceIDs) == 0 {
		return 0, nil
	}
	db := r.db.Model(&model.FeedItem{}).
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
		Where("feed_items.feed_source_id IN ? AND feed_sources.hidden = ?", feedSourceIDs, false)
	if len(visibleAfter) > 0 {
		db = applyFeedSourceVisibleAfter(db, visibleAfter[0])
	}
	var count int64
	err := db.Count(&count).Error
	return count, err
}

func (r *Repo) ListReadItems(userID uuid.UUID, feedItemIDs []uuid.UUID) ([]model.FeedItemRead, error) {
	if len(feedItemIDs) == 0 {
		return []model.FeedItemRead{}, nil
	}
	var reads []model.FeedItemRead
	err := r.db.Where("user_id = ? AND feed_item_id IN ?", userID, feedItemIDs).Find(&reads).Error
	return reads, err
}

func (r *Repo) MarkRead(userID uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	now := r.db.NowFunc()
	for _, id := range ids {
		read := model.FeedItemRead{UserID: userID, FeedItemID: id, ReadAt: now}
		if err := r.db.Where("user_id = ? AND feed_item_id = ?", userID, id).FirstOrCreate(&read).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) ListSubscribedExternalFeedItems(userID uuid.UUID) ([]model.FeedItem, error) {
	var items []model.FeedItem
	err := r.db.
		Joins("JOIN subscriptions ON subscriptions.feed_source_id = feed_items.feed_source_id").
		Joins("JOIN feed_sources ON feed_sources.id = subscriptions.feed_source_id").
		Where("subscriptions.user_id = ? AND feed_sources.source_type = ? AND feed_sources.hidden = ?", userID, "external_rss", false).
		Preload("FeedSource").
		Find(&items).Error
	return items, err
}

func (r *Repo) DeleteReads(userID uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	return r.db.Where("user_id = ? AND feed_item_id IN ?", userID, ids).Delete(&model.FeedItemRead{}).Error
}

func escapedContainsPattern(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
	return "%" + value + "%"
}

func (r *Repo) ListExplorePosts(limit int, offset int) ([]model.Post, error) {
	return r.ListExplorePostsPage(FeedQuery{}, limit, offset)
}

func (r *Repo) buildExplorePostsQuery(query FeedQuery) *gorm.DB {
	db := blogmodule.CanonicalBlogPostsQuery(r.db).
		Where("posts.status = ?", "published").
		Where("COALESCE(posts.visibility, '') IN ?", []string{"", "public"})
	if query.Category != "" && query.Category != "blog" {
		return db.Where("1 = 0")
	}
	if query.LanguageCode != "" {
		db = db.Where("blog_extensions.language_code = ?", query.LanguageCode)
	}
	if query.IsRead != nil && *query.IsRead {
		return db.Where("1 = 0")
	}
	if search := strings.TrimSpace(query.Search); search != "" {
		like := escapedContainsPattern(search)
		db = db.Joins("LEFT JOIN channels ON channels.id = posts.channel_id").Where(
			`(LOWER(posts.title) LIKE ? ESCAPE '\' OR
			 LOWER(posts.summary) LIKE ? ESCAPE '\' OR
			 LOWER(blog_extensions.content) LIKE ? ESCAPE '\' OR
			 LOWER(channels.name) LIKE ? ESCAPE '\' OR
			 LOWER(channels.slug) LIKE ? ESCAPE '\')`,
			like, like, like, like, like,
		)
	}
	return db
}

func (r *Repo) ListExplorePostsPage(query FeedQuery, limit int, offset int) ([]model.Post, error) {
	dbQuery := r.buildExplorePostsQuery(query).
		Order("COALESCE(posts.published_at, posts.created_at) DESC").
		Order("posts.created_at DESC, posts.id DESC").
		Offset(offset)
	if limit > 0 {
		dbQuery = dbQuery.Limit(limit)
	}
	posts, err := blogmodule.LoadCanonicalBlogPosts(r.db, dbQuery)
	if err != nil {
		return nil, err
	}
	return posts, nil
}

func (r *Repo) CountExplorePostsMatching(query FeedQuery) (int64, error) {
	var count int64
	return count, r.buildExplorePostsQuery(query).Count(&count).Error
}

func (r *Repo) ListExplorePostsAll(query FeedQuery) ([]model.Post, error) {
	return r.ListExplorePostsPage(query, 0, 0)
}

func (r *Repo) CountExplorePosts() (int64, error) {
	return r.CountExplorePostsMatching(FeedQuery{})
}

func (r *Repo) ListRecommendationPosts() ([]model.Post, error) {
	return blogmodule.LoadCanonicalBlogPosts(r.db, blogmodule.CanonicalBlogPostsQuery(r.db).
		Where("posts.status = ?", "published").
		Order("posts.created_at ASC, posts.id ASC"))
}

type RecommendationArticlePostRow struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	ChannelID     *uuid.UUID
	CreatedAt     time.Time
	PublishedAt   time.Time
	ContentLength int64
	Content       string
	HasSummary    bool
	HasCover      bool
	Title         string
	Summary       string
	LanguageCode  string
}

type RecommendationArticleFeedItemRow struct {
	ID                 uuid.UUID
	FeedSourceID       uuid.UUID
	Link               string
	Title              string
	Summary            string
	SummaryLength      int
	HasSummary         bool
	HasImage           bool
	HasFullText        bool
	ReaderQualityScore int
	FullTextWordCount  int
	ReaderSource       string
	EnclosureType      string
	PublishedAt        time.Time
	SourceCategory     string
	LanguageCode       string
}

func (r *Repo) ListRecommendationArticlePosts(includeText bool, publishedAfter time.Time, keywords []string, languageCode string, search string, limit int) ([]RecommendationArticlePostRow, error) {
	columns := []string{
		"posts.id",
		"posts.author_id AS user_id",
		"posts.channel_id",
		"posts.created_at",
		"COALESCE(posts.published_at, posts.created_at) AS published_at",
		"LENGTH(COALESCE(blog_extensions.content, '')) AS content_length",
		"blog_extensions.content",
		"COALESCE(posts.summary, '') <> '' AS has_summary",
		"COALESCE(posts.cover_url, '') <> '' AS has_cover",
		"'' AS title",
		"'' AS summary",
		"blog_extensions.language_code",
	}
	if includeText {
		columns[9] = "posts.title"
		columns[10] = "posts.summary"
	}

	var posts []RecommendationArticlePostRow
	db := r.db.Table("content_entries AS posts").
		Joins("JOIN content_blog_extensions AS blog_extensions ON blog_extensions.content_id = posts.id").
		Select(columns).
		Where("posts.kind = ? AND posts.status = ?", "blog", "published").
		Where("COALESCE(posts.visibility, '') IN ?", []string{"", "public"}).
		Where(recommendationArticlePostQualityPredicate("blog_extensions.content"), recommendationInternalArticleMinimumLength).
		Where("COALESCE(posts.published_at, posts.created_at) >= ?", publishedAfter).
		Order("COALESCE(posts.published_at, posts.created_at) DESC, posts.id DESC").
		Limit(limit)
	db = applyRecommendationLanguageFilter(db, "blog_extensions.language_code", languageCode)
	db = applyRecommendationTextFilter(db, "posts.title", "posts.summary", keywords)
	db = applyRecommendationSearchFilter(db, []string{"posts.title", "posts.summary"}, search)
	err := db.Scan(&posts).Error
	return posts, err
}

func (r *Repo) listLegacyRecommendationArticlePosts(includeText bool, publishedAfter time.Time, keywords []string, languageCode string, search string, limit int) ([]RecommendationArticlePostRow, error) {
	columns := []string{
		"posts.id",
		"posts.user_id",
		"posts.channel_id",
		"posts.created_at",
		"COALESCE(posts.published_at, posts.created_at) AS published_at",
		"LENGTH(COALESCE(posts.content, '')) AS content_length",
		"posts.content",
		"COALESCE(posts.summary, '') <> '' AS has_summary",
		"COALESCE(posts.cover_url, '') <> '' AS has_cover",
		"'' AS title",
		"'' AS summary",
		"posts.language_code",
	}
	if includeText {
		columns[9] = "posts.title"
		columns[10] = "posts.summary"
	}

	var posts []RecommendationArticlePostRow
	db := r.db.Table("posts").
		Select(columns).
		Where("posts.deleted_at IS NULL AND posts.status = ?", "published").
		Where("COALESCE(posts.visibility, '') IN ?", []string{"", "public"}).
		Where(recommendationArticlePostQualityPredicate("posts.content"), recommendationInternalArticleMinimumLength).
		Where("COALESCE(posts.published_at, posts.created_at) >= ?", publishedAfter).
		Order("COALESCE(posts.published_at, posts.created_at) DESC, posts.id DESC").
		Limit(limit)
	db = applyRecommendationLanguageFilter(db, "posts.language_code", languageCode)
	db = applyRecommendationTextFilter(db, "posts.title", "posts.summary", keywords)
	db = applyRecommendationSearchFilter(db, []string{"posts.title", "posts.summary"}, search)
	err := db.Scan(&posts).Error
	return posts, err
}

func (r *Repo) ListRecommendationArticleFeedItems(includeText bool, category string, publishedAfter time.Time, keywords []string, languageCode string, search string, limit int) ([]RecommendationArticleFeedItemRow, error) {
	columns := []string{
		"feed_items.id",
		"feed_items.feed_source_id",
		"feed_items.link",
		"'' AS title",
		"'' AS summary",
		"LENGTH(COALESCE(feed_items.summary, '')) AS summary_length",
		"COALESCE(feed_items.summary, '') <> '' AS has_summary",
		"COALESCE(feed_items.image_url, '') <> '' AS has_image",
		"COALESCE(feed_items.reader_html, '') <> '' OR COALESCE(feed_items.full_text_html, '') <> '' AS has_full_text",
		"COALESCE(feed_items.reader_quality_score, 0) AS reader_quality_score",
		"COALESCE(feed_items.full_text_word_count, 0) AS full_text_word_count",
		"COALESCE(feed_items.reader_source, '') AS reader_source",
		"feed_items.enclosure_type",
		"feed_items.published_at",
		"feed_sources.category AS source_category",
		"feed_items.language_code",
	}
	if includeText {
		columns[2] = "feed_items.title"
		columns[3] = "feed_items.summary"
	}

	var items []RecommendationArticleFeedItemRow
	db := r.db.Table("feed_items").
		Select(columns).
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
		Where("feed_sources.hidden = ?", false).
		Where("feed_items.deleted_at IS NULL").
		Where("feed_items.published_at >= ?", publishedAfter).
		Where(recommendationFeedItemQualityPredicate(), recommendationFeedReaderQualityThreshold, recommendationFeedFallbackWordCount, recommendationFeedFallbackSummaryLength).
		Where(recommendationFeedItemCategorySQL()+" = ?", category)
	db = applyRecommendationLanguageFilter(db, "feed_items.language_code", languageCode)
	db = applyRecommendationTextFilter(db, "feed_items.title", "feed_items.summary", keywords)
	db = applyRecommendationFeedItemSearchFilter(db, search)
	db = db.Order("feed_items.published_at DESC, feed_items.id DESC")
	db = db.Limit(limit)
	err := db.Scan(&items).Error
	return items, err
}

func applyRecommendationLanguageFilter(db *gorm.DB, column string, languageCode string) *gorm.DB {
	if strings.TrimSpace(languageCode) == "" {
		return db
	}
	return db.Where(column+" = ?", languageCode)
}

func applyRecommendationSearchFilter(db *gorm.DB, columns []string, search string) *gorm.DB {
	if strings.TrimSpace(search) == "" || len(columns) == 0 {
		return db
	}
	like := escapedContainsPattern(search)
	clauses := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))
	for _, column := range columns {
		clauses = append(clauses, "LOWER("+column+") LIKE ? ESCAPE '\\'")
		args = append(args, like)
	}
	return db.Where("("+strings.Join(clauses, " OR ")+")", args...)
}

func applyRecommendationFeedItemSearchFilter(db *gorm.DB, search string) *gorm.DB {
	if strings.TrimSpace(search) == "" {
		return db
	}
	like := escapedContainsPattern(search)
	return db.Where(`feed_items.id IN (
		SELECT matched_items.id
		FROM feed_items AS matched_items
		JOIN feed_sources AS matched_sources ON matched_sources.id = matched_items.feed_source_id
		WHERE matched_sources.hidden = false
		  AND matched_items.deleted_at IS NULL
		  AND (LOWER(matched_items.title) LIKE ? ESCAPE '\' OR LOWER(matched_items.summary) LIKE ? ESCAPE '\')
		UNION
		SELECT matched_items.id
		FROM feed_items AS matched_items
		JOIN feed_sources AS matched_sources ON matched_sources.id = matched_items.feed_source_id
		WHERE matched_sources.hidden = false
		  AND matched_items.deleted_at IS NULL
		  AND (LOWER(matched_sources.title) LIKE ? ESCAPE '\' OR LOWER(matched_sources.rss_url) LIKE ? ESCAPE '\')
	)`, like, like, like, like)
}

func applyRecommendationTextFilter(db *gorm.DB, titleColumn string, summaryColumn string, keywords []string) *gorm.DB {
	if len(keywords) == 0 {
		return db
	}
	clauses := make([]string, 0, len(keywords))
	args := make([]any, 0, len(keywords)*2)
	for _, keyword := range keywords {
		pattern := "%" + strings.ToLower(strings.TrimSpace(keyword)) + "%"
		clauses = append(clauses, "(LOWER(COALESCE("+titleColumn+", '')) LIKE ? OR LOWER(COALESCE("+summaryColumn+", '')) LIKE ?)")
		args = append(args, pattern, pattern)
	}
	return db.Where(strings.Join(clauses, " OR "), args...)
}

func recommendationArticlePostQualityPredicate(contentColumn string) string {
	return "LENGTH(COALESCE(" + contentColumn + ", '')) >= ?"
}

func recommendationFeedItemQualityPredicate() string {
	return `(
		COALESCE(feed_items.reader_quality_score, 0) >= ?
		OR (
			COALESCE(feed_items.reader_quality_score, 0) = 0
			AND (
				COALESCE(feed_items.full_text_word_count, 0) >= ?
				OR LENGTH(COALESCE(feed_items.summary, '')) >= ?
			)
		)
	)`
}

func recommendationFeedItemCategorySQL() string {
	return `CASE
		WHEN LOWER(COALESCE(feed_items.enclosure_type, '')) LIKE 'video/%' THEN 'video'
		WHEN LOWER(COALESCE(feed_items.enclosure_type, '')) LIKE 'audio/%' THEN 'podcast'
		WHEN LOWER(COALESCE(feed_sources.category, '')) IN ('blog', 'news', 'social', 'video', 'forum', 'podcast')
			THEN LOWER(feed_sources.category)
		ELSE 'blog'
	END`
}

func (r *Repo) ListRecommendationPostsByIDs(ids []uuid.UUID) ([]model.Post, error) {
	if len(ids) == 0 {
		return []model.Post{}, nil
	}
	return blogmodule.LoadCanonicalBlogPosts(r.db, blogmodule.CanonicalBlogPostsQuery(r.db).Where("posts.id IN ?", ids))
}

func (r *Repo) ListRecommendationFeedItemsByIDs(ids []uuid.UUID) ([]model.FeedItem, error) {
	if len(ids) == 0 {
		return []model.FeedItem{}, nil
	}
	var items []model.FeedItem
	err := r.db.Model(&model.FeedItem{}).
		Preload("FeedSource").
		Select("id", "title", "summary", "image_url", "language_code", "feed_source_id").
		Where("id IN ?", ids).
		Find(&items).Error
	return items, err
}

type RecommendationChannelRow struct {
	ChannelID             uuid.UUID
	Slug                  string
	Name                  string
	Description           string
	CoverURL              string
	PublishedCount        int64
	RecentPostCount       int64
	AverageViews          float64
	LatestPublishedAtUnix sql.NullInt64
	LanguageCode          string
}

func (r *Repo) ListRecommendationChannels(languageCode string) ([]RecommendationChannelRow, error) {
	rows := make([]RecommendationChannelRow, 0)
	latestPublishedExpr := recommendationChannelLatestPublishedExpr(r.db.Dialector.Name())
	db := r.db.Table("channels").
		Select(`
			channels.id AS channel_id,
			channels.slug AS slug,
			channels.name AS name,
			channels.description AS description,
			channels.cover_url AS cover_url,
			COUNT(posts.id) AS published_count,
			SUM(CASE WHEN posts.created_at >= ? THEN 1 ELSE 0 END) AS recent_post_count,
			COALESCE(AVG(blog_extensions.view_count), 0) AS average_views,
			`+latestPublishedExpr+` AS latest_published_at_unix,
			MAX(blog_extensions.language_code) AS language_code
		`, time.Now().Add(-7*24*time.Hour)).
		Joins("JOIN content_entries AS posts ON posts.channel_id = channels.id").
		Joins("JOIN content_blog_extensions AS blog_extensions ON blog_extensions.content_id = posts.id").
		Where("channels.deleted_at IS NULL AND posts.deleted_at IS NULL AND posts.kind = ? AND posts.status = ?", "blog", "published")
	db = db.Where("COALESCE(posts.visibility, '') IN ?", []string{"", "public"})
	if languageCode != "" {
		db = db.Where("blog_extensions.language_code = ?", languageCode)
	}
	err := db.
		Group("channels.id").
		Order("MAX(posts.created_at) DESC").
		Scan(&rows).Error
	return rows, err
}

func (r *Repo) ListRecentPublishedPostsByChannelID(channelID uuid.UUID, limit int) ([]model.Post, error) {
	query := blogmodule.CanonicalBlogPostsQuery(r.db).
		Where("posts.channel_id = ? AND posts.status = ?", channelID, "published").
		Where("COALESCE(posts.visibility, '') IN ?", []string{"", "public"}).
		Order("posts.created_at DESC").
		Limit(limit)
	return blogmodule.LoadCanonicalBlogPosts(r.db, query)
}

func (r *Repo) ListRecentPublishedPostTitlesByChannelIDs(channelIDs []uuid.UUID, limit int) (map[uuid.UUID][]string, error) {
	titlesByChannelID := make(map[uuid.UUID][]string, len(channelIDs))
	if len(channelIDs) == 0 || limit <= 0 {
		return titlesByChannelID, nil
	}

	type recentPostTitleRow struct {
		ChannelID uuid.UUID `gorm:"column:channel_id"`
		Title     string    `gorm:"column:title"`
	}
	rows := make([]recentPostTitleRow, 0, len(channelIDs)*limit)
	rankedPosts := r.db.Table("content_entries AS posts").
		Select(`
			posts.channel_id,
			posts.title,
			ROW_NUMBER() OVER (
				PARTITION BY posts.channel_id
				ORDER BY posts.created_at DESC, posts.id DESC
			) AS row_number
		`).
		Joins("JOIN content_blog_extensions AS blog_extensions ON blog_extensions.content_id = posts.id").
		Where("posts.channel_id IN ?", channelIDs).
		Where("posts.deleted_at IS NULL AND posts.kind = ? AND posts.status = ?", "blog", "published").
		Where("COALESCE(posts.visibility, '') IN ?", []string{"", "public"})
	if err := r.db.Table("(?) AS ranked_posts", rankedPosts).
		Select("channel_id, title").
		Where("row_number <= ?", limit).
		Order("channel_id, row_number").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		titlesByChannelID[row.ChannelID] = append(titlesByChannelID[row.ChannelID], row.Title)
	}
	return titlesByChannelID, nil
}

func recommendationChannelLatestPublishedExpr(dialect string) string {
	switch dialect {
	case "postgres":
		return "CAST(EXTRACT(EPOCH FROM MAX(posts.created_at)) AS bigint)"
	default:
		return "MAX(unixepoch(posts.created_at))"
	}
}

func (r *Repo) ListExploreFeedItems(sort string, limit int, offset int) ([]model.FeedItem, error) {
	return r.ListExploreFeedItemsPage(sort, FeedQuery{}, limit, offset)
}

func (r *Repo) buildExploreFeedItemsQuery(query FeedQuery) *gorm.DB {
	db := r.db.Model(&model.FeedItem{}).
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
		Where("feed_sources.hidden = ?", false)
	if query.SourceType != "" {
		db = db.Where("feed_sources.source_type = ?", query.SourceType)
	}
	if query.SourceID != uuid.Nil {
		db = db.Where("feed_sources.id = ?", query.SourceID)
	}
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
	if search := strings.TrimSpace(query.Search); search != "" {
		like := escapedContainsPattern(search)
		db = db.Where(`feed_items.id IN (
			SELECT matched_items.id
			FROM feed_items AS matched_items
			JOIN feed_sources AS matched_sources ON matched_sources.id = matched_items.feed_source_id
			WHERE matched_sources.hidden = false
			  AND matched_items.deleted_at IS NULL
			  AND (LOWER(matched_items.title) LIKE ? ESCAPE '\' OR LOWER(matched_items.summary) LIKE ? ESCAPE '\')
			UNION
			SELECT matched_items.id
			FROM feed_items AS matched_items
			JOIN feed_sources AS matched_sources ON matched_sources.id = matched_items.feed_source_id
			WHERE matched_sources.hidden = false
			  AND matched_items.deleted_at IS NULL
			  AND (LOWER(matched_sources.title) LIKE ? ESCAPE '\' OR LOWER(matched_sources.rss_url) LIKE ? ESCAPE '\')
		)`, like, like, like, like)
	}
	return db
}

func (r *Repo) ListExploreFeedItemsPage(sort string, query FeedQuery, limit int, offset int) ([]model.FeedItem, error) {
	var items []model.FeedItem
	db := r.buildExploreFeedItemsQuery(query).
		Preload("FeedSource").
		Select("feed_items.*").
		Offset(offset)
	if limit > 0 {
		db = db.Limit(limit)
	}
	db = applyExploreFeedSort(db, sort)
	return items, db.Find(&items).Error
}

func (r *Repo) ListExploreFeedItemsAll(sort string, query FeedQuery) ([]model.FeedItem, error) {
	return r.ListExploreFeedItemsPage(sort, query, 0, 0)
}

func applyExploreFeedSort(db *gorm.DB, sort string) *gorm.DB {
	switch normalizeExploreSort(sort) {
	case "popular":
		return db.Select("feed_items.*, (SELECT COUNT(*) FROM feed_item_stars WHERE feed_item_stars.feed_item_id = feed_items.id) as star_count").
			Order("star_count DESC, published_at DESC, feed_items.id DESC")
	case "random":
		return db.Order("RANDOM()")
	default:
		return db.Order("published_at DESC, feed_items.id DESC")
	}
}

func normalizeExploreSort(sort string) string {
	switch strings.TrimSpace(strings.ToLower(sort)) {
	case "popular":
		return "popular"
	case "random":
		return "random"
	default:
		return "recent"
	}
}

func (r *Repo) countExploreFeedItemsSearch(search string) (int64, error) {
	pattern := escapedContainsPattern(search)
	var count int64
	err := r.db.Raw(`
		SELECT COUNT(*)
		FROM (
			SELECT feed_items.id
			FROM feed_items
			JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id
			WHERE feed_sources.hidden = false
			  AND feed_sources.deleted_at IS NULL
			  AND feed_items.deleted_at IS NULL
			  AND (LOWER(feed_items.title) LIKE ? ESCAPE '\' OR LOWER(feed_items.summary) LIKE ? ESCAPE '\')
			UNION
			SELECT feed_items.id
			FROM feed_items
			JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id
			WHERE feed_sources.hidden = false
			  AND feed_sources.deleted_at IS NULL
			  AND feed_items.deleted_at IS NULL
			  AND (LOWER(feed_sources.title) LIKE ? ESCAPE '\' OR LOWER(feed_sources.rss_url) LIKE ? ESCAPE '\')
		) AS matched_feed_items`, pattern, pattern, pattern, pattern).Scan(&count).Error
	return count, err
}

func (r *Repo) CountExploreFeedItemsMatching(query FeedQuery) (int64, error) {
	if strings.TrimSpace(query.Search) != "" && query.Category == "" && query.LanguageCode == "" && query.SourceType == "" && query.SourceID == uuid.Nil && query.IsRead == nil {
		return r.countExploreFeedItemsSearch(query.Search)
	}
	var count int64
	return count, r.buildExploreFeedItemsQuery(query).Count(&count).Error
}

func (r *Repo) CountExploreFeedItems() (int64, error) {
	return r.CountExploreFeedItemsMatching(FeedQuery{})
}

func (r *Repo) ListExploreSources(limit int, offset int, category string, query ...string) ([]ExploreSourceRow, error) {
	type exploreSourceRowRaw struct {
		ID                uuid.UUID
		Title             string
		RSSURL            string
		CoverURL          string
		Category          string
		LanguageCode      string
		SubscriptionCount int64
		RecentItemCount   int64
		LastPublishedAt   sql.NullString
	}

	queryValue := ""
	languageValue := ""
	if len(query) > 0 {
		queryValue = query[0]
	}
	if len(query) > 1 {
		languageValue = query[1]
	}

	var rawRows []exploreSourceRowRaw
	db := r.db.Table("feed_sources").
		Select(`
			feed_sources.id,
			feed_sources.title,
			feed_sources.rss_url,
			feed_sources.cover_url,
			feed_sources.category,
			feed_sources.language_code,
			COUNT(DISTINCT subscriptions.id) AS subscription_count,
			COUNT(DISTINCT feed_items.id) AS recent_item_count,
			MAX(feed_items.published_at) AS last_published_at
		`).
		Joins("LEFT JOIN subscriptions ON subscriptions.feed_source_id = feed_sources.id AND subscriptions.deleted_at IS NULL").
		Joins("LEFT JOIN feed_items ON feed_items.feed_source_id = feed_sources.id AND feed_items.deleted_at IS NULL").
		Where("feed_sources.source_type = ?", "external_rss").
		Where("feed_sources.hidden = ?", false).
		Where("feed_sources.deleted_at IS NULL")
	if languageValue != "" {
		db = db.Where("feed_sources.language_code = ?", languageValue)
	}
	if strings.TrimSpace(queryValue) != "" {
		like := escapedContainsPattern(queryValue)
		db = db.Where(`
			(LOWER(feed_sources.title) LIKE ? ESCAPE '\' OR
			LOWER(feed_sources.rss_url) LIKE ? ESCAPE '\')`, like, like)
	}
	if normalizedCategory := normalizeFeedSourceCategory(category); normalizedCategory != "" {
		db = applyExploreSourceCategoryFilter(db, normalizedCategory)
	}
	queryDB := db.
		Group("feed_sources.id").
		Having("COUNT(DISTINCT feed_items.id) > 0").
		Order("subscription_count DESC").
		Order("last_published_at DESC NULLS LAST").
		Order("feed_sources.created_at DESC")
	if normalizeFeedSourceCategory(category) == "" {
		queryDB = queryDB.Offset(offset).Limit(limit)
	}
	if err := queryDB.Scan(&rawRows).Error; err != nil {
		return nil, err
	}

	rows := make([]ExploreSourceRow, 0, len(rawRows))
	sourceIDs := make([]uuid.UUID, 0, len(rawRows))
	for _, raw := range rawRows {
		row := ExploreSourceRow{
			ID:                raw.ID,
			Title:             raw.Title,
			RSSURL:            raw.RSSURL,
			CoverURL:          raw.CoverURL,
			Category:          raw.Category,
			LanguageCode:      raw.LanguageCode,
			SubscriptionCount: raw.SubscriptionCount,
			RecentItemCount:   raw.RecentItemCount,
		}
		if raw.LastPublishedAt.Valid {
			parsed, parseErr := parseExploreSourceTimestamp(raw.LastPublishedAt.String)
			if parseErr != nil {
				return nil, parseErr
			}
			row.LastPublishedAt = &parsed
		}
		rows = append(rows, row)
		sourceIDs = append(sourceIDs, raw.ID)
	}

	if err := r.attachExploreSourceRecentItems(rows, sourceIDs); err != nil {
		return nil, err
	}

	normalizedCategory := normalizeFeedSourceCategory(category)
	normalizedQuery := strings.ToLower(strings.TrimSpace(queryValue))
	if normalizedCategory != "" {
		rows = filterExploreSourceRowsByCategory(rows, normalizedCategory)
	}
	if normalizedQuery != "" {
		rows = filterExploreSourceRowsByQuery(rows, normalizedQuery)
	}
	if normalizedCategory == "" {
		return rows, nil
	}
	if offset >= len(rows) {
		return []ExploreSourceRow{}, nil
	}
	end := offset + limit
	if end > len(rows) {
		end = len(rows)
	}
	return rows[offset:end], nil
}

func (r *Repo) attachExploreSourceRecentItems(rows []ExploreSourceRow, sourceIDs []uuid.UUID) error {
	if len(rows) == 0 {
		return nil
	}

	type recentItemRow struct {
		ID            uuid.UUID
		FeedSourceID  uuid.UUID
		Title         string
		Link          string
		PublishedAt   time.Time
		EnclosureType string
	}
	var items []recentItemRow
	if err := r.db.Raw(`
		SELECT id, feed_source_id, title, link, published_at, enclosure_type
		FROM (
			SELECT id, feed_source_id, title, link, published_at, enclosure_type,
				ROW_NUMBER() OVER (
					PARTITION BY feed_source_id
					ORDER BY published_at DESC, created_at DESC
				) AS row_number
			FROM feed_items
			WHERE feed_source_id IN ? AND deleted_at IS NULL
		) ranked_items
		WHERE row_number <= 3
		ORDER BY feed_source_id, published_at DESC
	`, sourceIDs).Scan(&items).Error; err != nil {
		return err
	}

	rowIndexBySourceID := make(map[uuid.UUID]int, len(rows))
	for i, row := range rows {
		rowIndexBySourceID[row.ID] = i
	}

	for _, item := range items {
		rowIndex, ok := rowIndexBySourceID[item.FeedSourceID]
		if !ok {
			continue
		}
		rows[rowIndex].RecentItems = append(rows[rowIndex].RecentItems, ExploreSourceRecentItem{
			ID:            item.ID,
			Title:         item.Title,
			Link:          item.Link,
			PublishedAt:   item.PublishedAt,
			EnclosureType: item.EnclosureType,
		})
	}

	for i := range rows {
		inferredCategory := inferFeedSourceCategory(rows[i])
		if inferredCategory != "blog" {
			rows[i].Category = inferredCategory
			continue
		}
		if normalized := normalizeFeedSourceCategory(rows[i].Category); normalized != "" {
			rows[i].Category = normalized
			continue
		}
		rows[i].Category = inferredCategory
	}

	return nil
}

func (r *Repo) CountExploreSources(category string, query string, language ...string) (int64, error) {
	languageValue := ""
	if len(language) > 0 {
		languageValue = language[0]
	}
	db := r.db.Table("feed_sources").
		Select("feed_sources.id").
		Joins("LEFT JOIN feed_items ON feed_items.feed_source_id = feed_sources.id AND feed_items.deleted_at IS NULL").
		Where("feed_sources.source_type = ? AND feed_sources.hidden = ? AND feed_sources.deleted_at IS NULL", "external_rss", false)
	if normalizedCategory := normalizeFeedSourceCategory(category); normalizedCategory != "" {
		db = applyExploreSourceCategoryFilter(db, normalizedCategory)
	}
	if languageValue != "" {
		db = db.Where("feed_sources.language_code = ?", languageValue)
	}
	if strings.TrimSpace(query) != "" {
		like := escapedContainsPattern(query)
		db = db.Where(`
			(LOWER(feed_sources.title) LIKE ? ESCAPE '\' OR
			LOWER(feed_sources.rss_url) LIKE ? ESCAPE '\')`, like, like)
	}
	subquery := db.Group("feed_sources.id").Having("COUNT(DISTINCT feed_items.id) > 0")
	var count int64
	err := r.db.Table("(?) AS explore_sources", subquery).Count(&count).Error
	return count, err
}

func (r *Repo) CountSubscriptionsByFeedSourceID(feedSourceID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.Model(&model.Subscription{}).Where("feed_source_id = ?", feedSourceID).Count(&count).Error
	return count, err
}

func (r *Repo) CountReadEvents(sourceType string, sourceID string) (int64, error) {
	var count int64
	err := r.db.Model(&model.SourceReadEvent{}).
		Where("source_type = ? AND source_id = ?", sourceType, sourceID).
		Count(&count).Error
	return count, err
}

func (r *Repo) CreateSourceReadEvent(event *model.SourceReadEvent) error {
	return r.db.Create(event).Error
}

func normalizeFeedSourceCategory(category string) string {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "blog", "news", "social", "video", "forum", "podcast":
		return strings.ToLower(strings.TrimSpace(category))
	default:
		return ""
	}
}

func applyExploreSourceCategoryFilter(db *gorm.DB, normalizedCategory string) *gorm.DB {
	storedCategory := "LOWER(COALESCE(feed_sources.category, '')) = ?"
	if normalizedCategory == "blog" {
		nonBlogCategories := []string{"news", "social", "video", "forum", "podcast"}
		inferred := make([]string, 0, len(nonBlogCategories))
		for _, value := range nonBlogCategories {
			inferred = append(inferred, "("+exploreSourceInferredCategorySQL(value)+")")
		}
		return db.Where("("+storedCategory+" OR (COALESCE(feed_sources.category, '') = '' AND NOT ("+strings.Join(inferred, " OR ")+")))", normalizedCategory)
	}
	inferredCategory := exploreSourceInferredCategorySQL(normalizedCategory)
	return db.Where("("+storedCategory+" OR ("+inferredCategory+"))", normalizedCategory)
}

func defaultFeedSourceCategory(category string) string {
	if normalized := normalizeFeedSourceCategory(category); normalized != "" {
		return normalized
	}
	return "blog"
}

func exploreSourceInferredCategorySQL(category string) string {
	textValue := "LOWER(COALESCE(feed_sources.title, '') || ' ' || COALESCE(feed_sources.rss_url, ''))"
	switch category {
	case "news":
		return textValue + " LIKE '%news%' OR " + textValue + " LIKE '%新闻%' OR " + textValue + " LIKE '%36kr%' OR " + textValue + " LIKE '%36氪%' OR " + textValue + " LIKE '%ftchinese%' OR " + textValue + " LIKE '%nytimes%' OR " + textValue + " LIKE '%media%' OR " + textValue + " LIKE '%gov.cn%' OR " + textValue + " LIKE '%stats.gov%' OR " + textValue + " LIKE '%统计%' OR " + textValue + " LIKE '%数据发布%'"
	case "social":
		return textValue + " LIKE '%x.com%' OR " + textValue + " LIKE '%twitter%' OR " + textValue + " LIKE '%zhihu%' OR " + textValue + " LIKE '%jike%' OR " + textValue + " LIKE '%reddit%' OR " + textValue + " LIKE '%社交%'"
	case "video":
		return textValue + " LIKE '%youtube%' OR " + textValue + " LIKE '%bilibili%' OR " + textValue + " LIKE '%video%' OR " + textValue + " LIKE '%视频%' OR EXISTS (SELECT 1 FROM feed_items category_items WHERE category_items.feed_source_id = feed_sources.id AND LOWER(category_items.enclosure_type) LIKE 'video/%')"
	case "forum":
		return textValue + " LIKE '%forum%' OR " + textValue + " LIKE '%bbs%' OR " + textValue + " LIKE '%discourse%' OR " + textValue + " LIKE '%v2ex%' OR " + textValue + " LIKE '%nodeseek%' OR " + textValue + " LIKE '%linux.do%' OR " + textValue + " LIKE '%论坛%'"
	case "podcast":
		return textValue + " LIKE '%xiaoyuzhou%' OR " + textValue + " LIKE '%podcast%' OR " + textValue + " LIKE '%播客%' OR EXISTS (SELECT 1 FROM feed_items category_items WHERE category_items.feed_source_id = feed_sources.id AND LOWER(category_items.enclosure_type) LIKE 'audio/%')"
	default:
		return "FALSE"
	}
}

func inferFeedSourceCategory(row ExploreSourceRow) string {
	recentItems := make([]feedclass.RecentItem, 0, len(row.RecentItems))
	for _, item := range row.RecentItems {
		recentItems = append(recentItems, feedclass.RecentItem{
			Title:         item.Title,
			Link:          item.Link,
			EnclosureType: item.EnclosureType,
		})
	}
	return feedclass.Classify(feedclass.Source{
		Title:       row.Title,
		RSSURL:      row.RSSURL,
		RecentItems: recentItems,
	})
}

func filterExploreSourceRowsByCategory(rows []ExploreSourceRow, category string) []ExploreSourceRow {
	filtered := make([]ExploreSourceRow, 0, len(rows))
	for _, row := range rows {
		if row.Category == category {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func filterExploreSourceRowsByQuery(rows []ExploreSourceRow, query string) []ExploreSourceRow {
	filtered := make([]ExploreSourceRow, 0, len(rows))
	for _, row := range rows {
		value := strings.ToLower(row.Title + " " + row.RSSURL)
		if strings.Contains(value, query) {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func parseExploreSourceTimestamp(raw string) (time.Time, error) {
	value := strings.TrimSpace(raw)
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05Z07:00",
	}
	var lastErr error
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed, nil
		}
		lastErr = err
	}
	resultErr := fmt.Errorf("parse explore source timestamp %q: %w", raw, lastErr)
	return time.Time{}, resultErr
}

func (r *Repo) FeedItemExists(id uuid.UUID) (bool, error) {
	var count int64
	err := r.db.Model(&model.FeedItem{}).Where("id = ?", id).Count(&count).Error
	return count > 0, err
}

func (r *Repo) FindStar(userID uuid.UUID, feedItemID uuid.UUID) (model.FeedItemStar, error) {
	var star model.FeedItemStar
	err := r.db.Where("user_id = ? AND feed_item_id = ?", userID, feedItemID).First(&star).Error
	return star, err
}

func (r *Repo) CreateStar(star *model.FeedItemStar) error { return r.db.Create(star).Error }

func (r *Repo) DeleteStar(userID uuid.UUID, feedItemID uuid.UUID) error {
	return r.db.Where("user_id = ? AND feed_item_id = ?", userID, feedItemID).Delete(&model.FeedItemStar{}).Error
}

func (r *Repo) FindReadingListItem(userID uuid.UUID, targetType string, targetID uuid.UUID) (model.ReadingListItem, error) {
	var item model.ReadingListItem
	err := r.db.Where("user_id = ? AND target_type = ? AND target_id = ?", userID, targetType, targetID).First(&item).Error
	return item, err
}

func (r *Repo) CreateReadingListItem(item *model.ReadingListItem) error {
	return r.db.Create(item).Error
}

func (r *Repo) ListReadingListItems(userID uuid.UUID, limit int, offset int) ([]model.ReadingListItem, error) {
	var items []model.ReadingListItem
	err := r.db.Preload("FeedItem").Preload("FeedItem.FeedSource").
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&items).Error
	if err != nil || len(items) == 0 {
		return items, err
	}
	postIDs := make([]uuid.UUID, 0)
	for _, item := range items {
		if item.TargetType == "post" {
			postIDs = append(postIDs, item.TargetID)
		}
	}
	if len(postIDs) == 0 {
		return items, nil
	}
	posts, err := blogmodule.LoadCanonicalBlogPosts(r.db, blogmodule.CanonicalBlogPostsQuery(r.db).Where("posts.id IN ?", postIDs))
	if err != nil {
		return nil, err
	}
	postsByID := make(map[uuid.UUID]*model.Post, len(posts))
	for index := range posts {
		post := posts[index]
		postsByID[post.ID] = &post
	}
	for index := range items {
		if items[index].TargetType == "post" {
			items[index].Post = postsByID[items[index].TargetID]
		}
	}
	return items, nil
}

func (r *Repo) CountReadingListItems(userID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.Model(&model.ReadingListItem{}).Where("user_id = ?", userID).Count(&count).Error
	return count, err
}

func (r *Repo) DeleteReadingListItem(userID uuid.UUID, targetType string, targetID uuid.UUID) error {
	result := r.db.Where("user_id = ? AND target_type = ? AND target_id = ?", userID, targetType, targetID).Delete(&model.ReadingListItem{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return apperr.NotFound("feed.reading_list_item_not_found", "Reading list item not found")
	}
	return nil
}
