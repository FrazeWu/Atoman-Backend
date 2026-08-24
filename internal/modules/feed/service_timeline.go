package feed

import (
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	legacyfeed "atoman/internal/service"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) GetPublicFeedBySourceID(feedSourceID uuid.UUID, query FeedQuery) ([]TimelineItemDTO, int64, error) {
	if _, err := s.repo.GetPublicExternalFeedSource(feedSourceID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, apperr.NotFound("feed.source_not_found", "Feed source not found")
		}
		return nil, 0, err
	}
	if query.ContentType == "blog" {
		return []TimelineItemDTO{}, 0, nil
	}
	page := normalizedPage(query.Page)
	limit := normalizedPageSize(query.PageSize)
	offset := (page - 1) * limit

	feedItems, err := s.repo.ListFeedItemsBySourceID(feedSourceID, limit, offset)
	if err != nil {
		return nil, 0, err
	}

	items := make([]TimelineItemDTO, 0, len(feedItems))
	for i := range feedItems {
		items = append(items, TimelineItemDTO{
			Type:        "feed_item",
			FeedItem:    &feedItems[i],
			PublishedAt: feedItems[i].PublishedAt,
		})
	}

	total, err := s.repo.CountFeedItemsBySourceID(feedSourceID)
	if err != nil {
		return nil, 0, err
	}

	return items, total, nil
}

func (s *Service) GetSubscribedFeed(user authctx.CurrentUser, query FeedQuery) ([]TimelineItemDTO, int64, error) {
	if user.ID == uuid.Nil {
		if query.ContentType == "blog" {
			return []TimelineItemDTO{}, 0, nil
		}
		return s.GetPublicFeed(query)
	}
	query.viewerID = user.ID

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
	if len(subscriptions) == 0 && len(followedUserIDs) == 0 {
		return []TimelineItemDTO{}, 0, nil
	}

	userIDs := append([]uuid.UUID(nil), followedUserIDs...)
	channelIDs := make([]uuid.UUID, 0)
	collectionIDs := make([]uuid.UUID, 0)
	feedSourceIDs := make([]uuid.UUID, 0)
	feedSourceVisibleAfter := make(map[uuid.UUID]time.Time)
	for _, sub := range subscriptions {
		if sub.IsPaused {
			continue
		}
		if sub.FeedSource == nil {
			continue
		}
		switch sub.FeedSource.SourceType {
		case "internal_user":
			if sub.FeedSource.SourceID != nil {
				userIDs = append(userIDs, *sub.FeedSource.SourceID)
			}
		case "internal_channel":
			if sub.FeedSource.SourceID != nil {
				channelIDs = append(channelIDs, *sub.FeedSource.SourceID)
			}
		case "internal_collection":
			if sub.FeedSource.SourceID != nil {
				collectionIDs = append(collectionIDs, *sub.FeedSource.SourceID)
			}
		case "external_rss":
			if query.ContentType != "blog" {
				feedSourceIDs = append(feedSourceIDs, sub.FeedSource.ID)
				if sub.ResumedAfter != nil {
					feedSourceVisibleAfter[sub.FeedSource.ID] = *sub.ResumedAfter
				}
			}
		}
	}

	userIDs = dedupeUUIDs(userIDs)
	channelIDs = dedupeUUIDs(channelIDs)
	collectionIDs = dedupeUUIDs(collectionIDs)
	feedSourceIDs = dedupeUUIDs(feedSourceIDs)
	if query.ContentType == "blog" {
		return s.getSubscribedBlogFeed(user.ID, userIDs, channelIDs, collectionIDs, query)
	}
	query.sourceVisibleAfter = feedSourceVisibleAfter
	if len(userIDs) == 0 && len(channelIDs) == 0 && len(collectionIDs) == 0 && !query.HideDuplicates && strings.TrimSpace(query.Search) == "" {
		return s.getSubscribedExternalFeed(user.ID, feedSourceIDs, query, feedSourceVisibleAfter)
	}

	posts := make([]model.Post, 0)
	userPosts, err := s.repo.ListPublishedPostsByUserIDs(userIDs, query.ContentType)
	if err != nil {
		return nil, 0, err
	}
	posts = append(posts, userPosts...)
	channelPosts, err := s.repo.ListPublishedPostsByChannelIDs(channelIDs, query.ContentType)
	if err != nil {
		return nil, 0, err
	}
	posts = append(posts, channelPosts...)
	collectionPosts, err := s.repo.ListPublishedPostsByCollectionIDs(collectionIDs, query.ContentType)
	if err != nil {
		return nil, 0, err
	}
	posts = append(posts, collectionPosts...)
	posts = dedupePosts(posts)
	posts = filterVisibleSubscribedPosts(posts, user.ID, userIDs, channelIDs, collectionIDs)
	postIDs := make([]uuid.UUID, 0, len(posts))
	for i := range posts {
		postIDs = append(postIDs, posts[i].ID)
	}
	engagementCounts, err := s.repo.ListPostEngagementCounts(postIDs)
	if err != nil {
		return nil, 0, err
	}
	engagementByPostID := make(map[uuid.UUID]PostEngagementCount, len(engagementCounts))
	for _, count := range engagementCounts {
		engagementByPostID[count.PostID] = count
	}
	episodes, err := s.repo.ListPodcastEpisodesByPostIDs(postIDs)
	if err != nil {
		return nil, 0, err
	}
	episodeByPostID := make(map[uuid.UUID]model.PodcastEpisode, len(episodes))
	for _, episode := range episodes {
		episodeByPostID[episode.PostID] = episode
	}
	videos, err := s.repo.ListPublishedVideosByScope(userIDs, channelIDs, collectionIDs, query.ContentType)
	if err != nil {
		return nil, 0, err
	}
	videos = dedupeVideos(videos)

	feedItems, err := s.repo.ListFeedItemsBySourceIDs(feedSourceIDs, feedSourceVisibleAfter)
	if err != nil {
		return nil, 0, err
	}
	if query.HideDuplicates {
		legacyfeed.AnnotateDuplicateFeedItems(feedItems)
	}

	readMap, err := s.readMap(user.ID, feedItems)
	if err != nil {
		return nil, 0, err
	}

	items := make([]TimelineItemDTO, 0, len(posts)+len(videos)+len(feedItems))
	for i := range posts {
		if episode, ok := episodeByPostID[posts[i].ID]; ok {
			episode.Post = &posts[i]
			items = append(items, TimelineItemDTO{
				Type:           "podcast_episode",
				PodcastEpisode: &episode,
				PublishedAt:    postTimelinePublishedAt(posts[i]),
				IsRead:         false,
			})
			continue
		}
		items = append(items, TimelineItemDTO{
			Type:        "post",
			Post:        timelinePostDTO(posts[i], engagementByPostID[posts[i].ID]),
			PublishedAt: postTimelinePublishedAt(posts[i]),
			IsRead:      false,
		})
	}
	for i := range videos {
		items = append(items, TimelineItemDTO{
			Type:        "video",
			Video:       &videos[i],
			PublishedAt: videos[i].CreatedAt,
			IsRead:      false,
		})
	}
	for i := range feedItems {
		items = append(items, TimelineItemDTO{
			Type:        "feed_item",
			FeedItem:    &feedItems[i],
			PublishedAt: feedItems[i].PublishedAt,
			IsRead:      feedItemClusterRead(feedItems[i], readMap),
		})
	}

	items = filterTimeline(items, query)
	sortTimeline(items)
	paged, total := paginateTimeline(items, normalizedPage(query.Page), normalizedPageSize(query.PageSize))
	return paged, total, nil
}

func (s *Service) getSubscribedBlogFeed(
	userID uuid.UUID,
	userIDs []uuid.UUID,
	channelIDs []uuid.UUID,
	collectionIDs []uuid.UUID,
	query FeedQuery,
) ([]TimelineItemDTO, int64, error) {
	if query.IsRead != nil && *query.IsRead {
		return []TimelineItemDTO{}, 0, nil
	}
	allSubscriptions, err := s.repo.ListSubscriptionsWithSources(userID, FeedQuery{})
	if err != nil {
		return nil, 0, err
	}
	followedUserIDs := make([]uuid.UUID, 0)
	followedChannelIDs := make([]uuid.UUID, 0)
	if query.SourceID == uuid.Nil && query.GroupID == uuid.Nil && (query.SourceType == "" || query.SourceType == "internal_user") {
		followedUserIDs, err = s.repo.ListFollowedUserIDs(userID)
		if err != nil {
			return nil, 0, err
		}
	}
	for _, subscription := range allSubscriptions {
		if subscription.FeedSource == nil || subscription.FeedSource.SourceID == nil {
			continue
		}
		switch subscription.FeedSource.SourceType {
		case "internal_user":
			followedUserIDs = append(followedUserIDs, *subscription.FeedSource.SourceID)
		case "internal_channel":
			followedChannelIDs = append(followedChannelIDs, *subscription.FeedSource.SourceID)
		}
	}
	posts, total, err := s.repo.ListSubscribedBlogPosts(
		userIDs,
		channelIDs,
		collectionIDs,
		dedupeUUIDs(followedUserIDs),
		dedupeUUIDs(followedChannelIDs),
		query,
	)
	if err != nil {
		return nil, 0, err
	}
	postIDs := make([]uuid.UUID, 0, len(posts))
	for i := range posts {
		postIDs = append(postIDs, posts[i].ID)
	}
	engagementCounts, err := s.repo.ListCanonicalBlogEngagementCounts(postIDs)
	if err != nil {
		return nil, 0, err
	}
	engagementByPostID := make(map[uuid.UUID]PostEngagementCount, len(engagementCounts))
	for _, count := range engagementCounts {
		engagementByPostID[count.PostID] = count
	}
	items := make([]TimelineItemDTO, 0, len(posts))
	for i := range posts {
		items = append(items, TimelineItemDTO{
			Type:        "post",
			Post:        timelinePostDTO(posts[i], engagementByPostID[posts[i].ID]),
			PublishedAt: postTimelinePublishedAt(posts[i]),
			IsRead:      false,
		})
	}
	return items, total, nil
}

func filterVisibleSubscribedPosts(posts []model.Post, viewerID uuid.UUID, userIDs, channelIDs, collectionIDs []uuid.UUID) []model.Post {
	visible := make([]model.Post, 0, len(posts))
	for _, post := range posts {
		switch post.Visibility {
		case "", "public":
			visible = append(visible, post)
		case "followers":
			if post.UserID == viewerID || containsUUID(userIDs, post.UserID) ||
				(post.ChannelID != nil && containsUUID(channelIDs, *post.ChannelID)) ||
				(post.CollectionID != nil && containsUUID(collectionIDs, *post.CollectionID)) {
				visible = append(visible, post)
			}
		case "private":
			if post.UserID == viewerID {
				visible = append(visible, post)
			}
		}
	}
	return visible
}

func containsUUID(values []uuid.UUID, target uuid.UUID) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func postTimelinePublishedAt(post model.Post) time.Time {
	if post.PublishedAt != nil {
		return *post.PublishedAt
	}
	return post.CreatedAt
}

func (s *Service) getSubscribedExternalFeed(userID uuid.UUID, feedSourceIDs []uuid.UUID, query FeedQuery, visibleAfter map[uuid.UUID]time.Time) ([]TimelineItemDTO, int64, error) {
	if len(feedSourceIDs) == 0 {
		return []TimelineItemDTO{}, 0, nil
	}
	if query.IsRead != nil {
		return s.getSubscribedExternalFeedWithReadFilter(userID, feedSourceIDs, query)
	}

	page := normalizedPage(query.Page)
	limit := normalizedPageSize(query.PageSize)
	offset := (page - 1) * limit
	feedItems, err := s.repo.ListFeedItemsBySourceIDsPaged(feedSourceIDs, limit, offset, visibleAfter)
	if err != nil {
		return nil, 0, err
	}
	readMap, err := s.readMap(userID, feedItems)
	if err != nil {
		return nil, 0, err
	}
	items := make([]TimelineItemDTO, 0, len(feedItems))
	for i := range feedItems {
		items = append(items, TimelineItemDTO{
			Type:        "feed_item",
			FeedItem:    &feedItems[i],
			PublishedAt: feedItems[i].PublishedAt,
			IsRead:      readMap[feedItems[i].ID],
		})
	}
	total, err := s.repo.CountFeedItemsBySourceIDs(feedSourceIDs, visibleAfter)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *Service) getSubscribedExternalFeedWithReadFilter(userID uuid.UUID, feedSourceIDs []uuid.UUID, query FeedQuery) ([]TimelineItemDTO, int64, error) {
	feedItems, err := s.repo.ListFeedItemsBySourceIDsFiltered(feedSourceIDs, query)
	if err != nil {
		return nil, 0, err
	}
	readMap, err := s.readMap(userID, feedItems)
	if err != nil {
		return nil, 0, err
	}
	items := make([]TimelineItemDTO, 0, len(feedItems))
	for i := range feedItems {
		items = append(items, TimelineItemDTO{
			Type:        "feed_item",
			FeedItem:    &feedItems[i],
			PublishedAt: feedItems[i].PublishedAt,
			IsRead:      readMap[feedItems[i].ID],
		})
	}
	total, err := s.repo.CountFeedItemsBySourceIDsFiltered(feedSourceIDs, query)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *Service) GetPublicFeed(query FeedQuery) ([]TimelineItemDTO, int64, error) {
	page := normalizedPage(query.Page)
	limit := normalizedPageSize(query.PageSize)
	offset := (page - 1) * limit

	if query.HideDuplicates {
		return s.getPublicFeedWithDuplicateFilter(query, page, limit)
	}
	if query.IsRead != nil && *query.IsRead {
		return []TimelineItemDTO{}, 0, nil
	}

	candidateLimit := offset + limit
	posts, err := s.repo.ListExplorePostsPage(query, candidateLimit, 0)
	if err != nil {
		return nil, 0, err
	}
	feedItems, err := s.repo.ListExploreFeedItemsPage("recent", query, candidateLimit, 0)
	if err != nil {
		return nil, 0, err
	}

	items := make([]TimelineItemDTO, 0, len(posts)+len(feedItems))
	for i := range posts {
		items = append(items, TimelineItemDTO{Type: "post", Post: timelinePostDTO(posts[i], PostEngagementCount{}), PublishedAt: posts[i].CreatedAt})
	}
	for i := range feedItems {
		items = append(items, TimelineItemDTO{Type: "feed_item", FeedItem: &feedItems[i], PublishedAt: feedItems[i].PublishedAt})
	}

	items = filterTimeline(items, query)
	sortTimeline(items)
	paged, _ := paginateTimeline(items, page, limit)
	postTotal, err := s.repo.CountExplorePostsMatching(query)
	if err != nil {
		return nil, 0, err
	}
	feedTotal, err := s.repo.CountExploreFeedItemsMatching(query)
	if err != nil {
		return nil, 0, err
	}
	return paged, postTotal + feedTotal, nil
}

func (s *Service) getPublicFeedWithDuplicateFilter(query FeedQuery, page int, limit int) ([]TimelineItemDTO, int64, error) {
	offset := (page - 1) * limit
	posts, err := s.repo.ListExplorePosts(limit, offset)
	if err != nil {
		return nil, 0, err
	}
	sources, err := s.repo.ListVisibleFeedSources(query)
	if err != nil {
		return nil, 0, err
	}

	feedSourceIDs := make([]uuid.UUID, 0, len(sources))
	for _, source := range sources {
		feedSourceIDs = append(feedSourceIDs, source.ID)
	}
	feedItems, err := s.repo.ListFeedItemsBySourceIDs(dedupeUUIDs(feedSourceIDs))
	if err != nil {
		return nil, 0, err
	}
	legacyfeed.AnnotateDuplicateFeedItems(feedItems)

	items := make([]TimelineItemDTO, 0, len(posts)+len(feedItems))
	for i := range posts {
		items = append(items, TimelineItemDTO{Type: "post", Post: timelinePostDTO(posts[i], PostEngagementCount{}), PublishedAt: posts[i].CreatedAt})
	}
	for i := range feedItems {
		items = append(items, TimelineItemDTO{Type: "feed_item", FeedItem: &feedItems[i], PublishedAt: feedItems[i].PublishedAt})
	}

	items = filterTimeline(items, query)
	sortTimeline(items)
	paged, total := paginateTimeline(items, page, limit)
	return paged, total, nil
}

func (s *Service) GetExploreFeed(user authctx.CurrentUser, query FeedQuery) ([]TimelineItemDTO, int64, error) {
	page := normalizedPage(query.Page)
	limit := normalizedPageSize(query.PageSize)
	sortMode := normalizeExploreSort(strings.TrimSpace(query.Sort))
	query.viewerID = user.ID
	if query.HideDuplicates {
		return s.getExploreFeedWithDuplicateFilter(user, query, page, limit, sortMode)
	}

	candidateLimit := (page-1)*limit + limit
	posts, err := s.repo.ListExplorePostsPage(query, candidateLimit, 0)
	if err != nil {
		return nil, 0, err
	}
	feedItems, err := s.repo.ListExploreFeedItemsPage("recent", query, candidateLimit, 0)
	if err != nil {
		return nil, 0, err
	}
	readMap := map[uuid.UUID]bool{}
	if user.ID != uuid.Nil {
		readMap, err = s.readMap(user.ID, feedItems)
		if err != nil {
			return nil, 0, err
		}
	}

	items := make([]TimelineItemDTO, 0, len(posts)+len(feedItems))
	for i := range posts {
		items = append(items, TimelineItemDTO{Type: "post", Post: timelinePostDTO(posts[i], PostEngagementCount{}), PublishedAt: posts[i].CreatedAt})
	}
	for i := range feedItems {
		items = append(items, TimelineItemDTO{
			Type: "feed_item", FeedItem: &feedItems[i], PublishedAt: feedItems[i].PublishedAt,
			IsRead: readMap[feedItems[i].ID],
		})
	}

	sortTimeline(items)
	items = filterTimeline(items, query)
	paged, _ := paginateTimeline(items, page, limit)
	postTotal, err := s.repo.CountExplorePostsMatching(query)
	if err != nil {
		return nil, 0, err
	}
	feedTotal, err := s.repo.CountExploreFeedItemsMatching(query)
	if err != nil {
		return nil, 0, err
	}
	return paged, postTotal + feedTotal, nil
}

func (s *Service) getExploreFeedWithDuplicateFilter(user authctx.CurrentUser, query FeedQuery, page int, limit int, sortMode string) ([]TimelineItemDTO, int64, error) {
	duplicateQuery := query
	// Read filtering must happen after duplicate clusters are formed so a read
	// mirror cannot hide the unread canonical item, or vice versa.
	duplicateQuery.IsRead = nil
	posts, err := s.repo.ListExplorePostsAll(duplicateQuery)
	if err != nil {
		return nil, 0, err
	}
	feedItems, err := s.repo.ListExploreFeedItemsAll(sortMode, duplicateQuery)
	if err != nil {
		return nil, 0, err
	}
	legacyfeed.AnnotateDuplicateFeedItems(feedItems)
	readMap := map[uuid.UUID]bool{}
	if user.ID != uuid.Nil {
		readMap, err = s.readMap(user.ID, feedItems)
		if err != nil {
			return nil, 0, err
		}
	}

	items := make([]TimelineItemDTO, 0, len(posts)+len(feedItems))
	for i := range posts {
		items = append(items, TimelineItemDTO{Type: "post", Post: timelinePostDTO(posts[i], PostEngagementCount{}), PublishedAt: posts[i].CreatedAt})
	}
	for i := range feedItems {
		isRead := readMap[feedItems[i].ID]
		if query.HideDuplicates {
			isRead = feedItemClusterRead(feedItems[i], readMap)
		}
		items = append(items, TimelineItemDTO{Type: "feed_item", FeedItem: &feedItems[i], PublishedAt: feedItems[i].PublishedAt, IsRead: isRead})
	}

	sortTimeline(items)
	items = filterTimeline(items, query)
	paged, total := paginateTimeline(items, page, limit)
	return paged, total, nil
}

func feedItemClusterRead(item model.FeedItem, readMap map[uuid.UUID]bool) bool {
	itemIDs := item.DuplicateItemIDs
	if len(itemIDs) == 0 {
		itemIDs = []uuid.UUID{item.ID}
	}
	for _, itemID := range itemIDs {
		if !readMap[itemID] {
			return false
		}
	}
	return true
}

func timelinePostDTO(post model.Post, engagement PostEngagementCount) *TimelinePostDTO {
	post.BookmarksCount = engagement.BookmarksCount
	post.RatingScore = engagement.RatingScore
	post.RatingCount = engagement.RatingCount
	return &TimelinePostDTO{
		Post:          post,
		LikesCount:    engagement.LikesCount,
		CommentsCount: engagement.CommentsCount,
	}
}
