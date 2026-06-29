package feed

import (
	"errors"
	"sort"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	legacyfeed "atoman/internal/service"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Service struct {
	db   *gorm.DB
	repo *Repo
}

func NewService(db *gorm.DB) *Service { return &Service{db: db, repo: NewRepo(db)} }

func (s *Service) GetPublicFeedBySourceID(feedSourceID uuid.UUID, query FeedQuery) ([]TimelineItemDTO, int64, error) {
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
		return s.GetPublicFeed(query)
	}

	subscriptions, err := s.repo.ListSubscriptionsWithSources(user.ID, query)
	if err != nil {
		return nil, 0, err
	}
	if len(subscriptions) == 0 {
		return []TimelineItemDTO{}, 0, nil
	}

	userIDs := make([]uuid.UUID, 0)
	channelIDs := make([]uuid.UUID, 0)
	collectionIDs := make([]uuid.UUID, 0)
	feedSourceIDs := make([]uuid.UUID, 0)
	for _, sub := range subscriptions {
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
			feedSourceIDs = append(feedSourceIDs, sub.FeedSource.ID)
		}
	}

	userIDs = dedupeUUIDs(userIDs)
	channelIDs = dedupeUUIDs(channelIDs)
	collectionIDs = dedupeUUIDs(collectionIDs)
	feedSourceIDs = dedupeUUIDs(feedSourceIDs)
	if len(userIDs) == 0 && len(channelIDs) == 0 && len(collectionIDs) == 0 && !query.HideDuplicates && strings.TrimSpace(query.Search) == "" {
		return s.getSubscribedExternalFeed(user.ID, feedSourceIDs, query)
	}

	posts := make([]model.Post, 0)
	userPosts, err := s.repo.ListPublishedPostsByUserIDs(userIDs)
	if err != nil {
		return nil, 0, err
	}
	posts = append(posts, userPosts...)
	channelPosts, err := s.repo.ListPublishedPostsByChannelIDs(channelIDs)
	if err != nil {
		return nil, 0, err
	}
	posts = append(posts, channelPosts...)
	collectionPosts, err := s.repo.ListPublishedPostsByCollectionIDs(collectionIDs)
	if err != nil {
		return nil, 0, err
	}
	posts = append(posts, collectionPosts...)
	posts = dedupePosts(posts)

	feedItems, err := s.repo.ListFeedItemsBySourceIDs(feedSourceIDs)
	if err != nil {
		return nil, 0, err
	}
	legacyfeed.AnnotateDuplicateFeedItems(feedItems)

	readMap, err := s.readMap(user.ID, feedItems)
	if err != nil {
		return nil, 0, err
	}

	items := make([]TimelineItemDTO, 0, len(posts)+len(feedItems))
	for i := range posts {
		items = append(items, TimelineItemDTO{
			Type:        "post",
			Post:        &posts[i],
			PublishedAt: posts[i].CreatedAt,
			IsRead:      false,
		})
	}
	for i := range feedItems {
		items = append(items, TimelineItemDTO{
			Type:        "feed_item",
			FeedItem:    &feedItems[i],
			PublishedAt: feedItems[i].PublishedAt,
			IsRead:      readMap[feedItems[i].ID],
		})
	}

	items = filterTimeline(items, query)
	sortTimeline(items)
	paged, total := paginateTimeline(items, normalizedPage(query.Page), normalizedPageSize(query.PageSize))
	return paged, total, nil
}

func (s *Service) getSubscribedExternalFeed(userID uuid.UUID, feedSourceIDs []uuid.UUID, query FeedQuery) ([]TimelineItemDTO, int64, error) {
	if len(feedSourceIDs) == 0 {
		return []TimelineItemDTO{}, 0, nil
	}
	if query.IsRead != nil {
		return s.getSubscribedExternalFeedWithReadFilter(userID, feedSourceIDs, query)
	}

	page := normalizedPage(query.Page)
	limit := normalizedPageSize(query.PageSize)
	offset := (page - 1) * limit
	feedItems, err := s.repo.ListFeedItemsBySourceIDsPaged(feedSourceIDs, limit, offset)
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
	total, err := s.repo.CountFeedItemsBySourceIDs(feedSourceIDs)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *Service) getSubscribedExternalFeedWithReadFilter(userID uuid.UUID, feedSourceIDs []uuid.UUID, query FeedQuery) ([]TimelineItemDTO, int64, error) {
	feedItems, err := s.repo.ListFeedItemsBySourceIDs(feedSourceIDs)
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
	items = filterTimeline(items, query)
	paged, total := paginateTimeline(items, normalizedPage(query.Page), normalizedPageSize(query.PageSize))
	return paged, total, nil
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
	posts, err := s.repo.ListExplorePosts(candidateLimit, 0)
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
	feedItems, err := s.repo.ListFeedItemsBySourceIDsPaged(dedupeUUIDs(feedSourceIDs), candidateLimit, 0)
	if err != nil {
		return nil, 0, err
	}

	items := make([]TimelineItemDTO, 0, len(posts)+len(feedItems))
	for i := range posts {
		items = append(items, TimelineItemDTO{Type: "post", Post: &posts[i], PublishedAt: posts[i].CreatedAt})
	}
	for i := range feedItems {
		items = append(items, TimelineItemDTO{Type: "feed_item", FeedItem: &feedItems[i], PublishedAt: feedItems[i].PublishedAt})
	}

	items = filterTimeline(items, query)
	sortTimeline(items)
	paged, _ := paginateTimeline(items, page, limit)
	postTotal, err := s.repo.CountExplorePosts()
	if err != nil {
		return nil, 0, err
	}
	feedTotal, err := s.repo.CountFeedItemsBySourceIDs(dedupeUUIDs(feedSourceIDs))
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
		items = append(items, TimelineItemDTO{Type: "post", Post: &posts[i], PublishedAt: posts[i].CreatedAt})
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
	offset := (page - 1) * limit
	posts, err := s.repo.ListExplorePosts(limit, offset)
	if err != nil {
		return nil, 0, err
	}
	feedItems, err := s.repo.ListExploreFeedItems(strings.TrimSpace(query.Sort), limit, offset)
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
		items = append(items, TimelineItemDTO{Type: "post", Post: &posts[i], PublishedAt: posts[i].CreatedAt})
	}
	for i := range feedItems {
		items = append(items, TimelineItemDTO{Type: "feed_item", FeedItem: &feedItems[i], PublishedAt: feedItems[i].PublishedAt, IsRead: readMap[feedItems[i].ID]})
	}

	sortTimeline(items)
	if len(items) > limit {
		items = items[:limit]
	}
	postTotal, err := s.repo.CountExplorePosts()
	if err != nil {
		return nil, 0, err
	}
	feedTotal, err := s.repo.CountExploreFeedItems()
	if err != nil {
		return nil, 0, err
	}
	return items, postTotal + feedTotal, nil
}

func (s *Service) MarkRead(user authctx.CurrentUser, ids []uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.repo.MarkRead(user.ID, dedupeUUIDs(ids))
}

func (s *Service) MarkUnread(user authctx.CurrentUser, ids []uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.repo.DeleteReads(user.ID, dedupeUUIDs(ids))
}

func (s *Service) MarkAllRead(user authctx.CurrentUser) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	items, err := s.repo.ListSubscribedExternalFeedItems(user.ID)
	if err != nil {
		return err
	}
	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return s.repo.MarkRead(user.ID, ids)
}

func (s *Service) MarkAllUnread(user authctx.CurrentUser) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	items, err := s.repo.ListSubscribedExternalFeedItems(user.ID)
	if err != nil {
		return err
	}
	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return s.repo.DeleteReads(user.ID, ids)
}

func (s *Service) ToggleStar(user authctx.CurrentUser, feedItemID uuid.UUID) (bool, error) {
	if user.ID == uuid.Nil {
		return false, apperr.Unauthorized("Login required")
	}
	if feedItemID == uuid.Nil {
		return false, apperr.BadRequest("validation.invalid_request", "feed_item_id is required")
	}
	if err := s.ensureFeedItemExists(feedItemID); err != nil {
		return false, err
	}
	_, err := s.repo.FindStar(user.ID, feedItemID)
	if err == nil {
		return false, s.repo.DeleteStar(user.ID, feedItemID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	star := model.FeedItemStar{UserID: user.ID, FeedItemID: feedItemID, StarredAt: time.Now()}
	if err := s.repo.CreateStar(&star); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) ToggleReadingList(user authctx.CurrentUser, feedItemID uuid.UUID) (bool, error) {
	if user.ID == uuid.Nil {
		return false, apperr.Unauthorized("Login required")
	}
	if feedItemID == uuid.Nil {
		return false, apperr.BadRequest("validation.invalid_request", "feed_item_id is required")
	}
	if err := s.ensureFeedItemExists(feedItemID); err != nil {
		return false, err
	}
	_, err := s.repo.FindReadingListItem(user.ID, feedItemID)
	if err == nil {
		return false, s.repo.DeleteReadingListItem(user.ID, feedItemID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	item := model.ReadingListItem{UserID: user.ID, FeedItemID: feedItemID, CreatedAt: time.Now()}
	if err := s.repo.CreateReadingListItem(&item); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) ListReadingList(user authctx.CurrentUser, query FeedQuery) ([]model.ReadingListItem, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	page := normalizedPage(query.Page)
	limit := normalizedPageSize(query.PageSize)
	items, err := s.repo.ListReadingListItems(user.ID, limit, (page-1)*limit)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.repo.CountReadingListItems(user.ID)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *Service) RemoveReadingListItem(user authctx.CurrentUser, feedItemID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if feedItemID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "feed item id must be a valid uuid")
	}
	return s.repo.DeleteReadingListItem(user.ID, feedItemID)
}

func (s *Service) readMap(userID uuid.UUID, items []model.FeedItem) (map[uuid.UUID]bool, error) {
	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	reads, err := s.repo.ListReadItems(userID, ids)
	if err != nil {
		return nil, err
	}
	result := make(map[uuid.UUID]bool, len(reads))
	for _, read := range reads {
		result[read.FeedItemID] = true
	}
	return result, nil
}

func (s *Service) ensureFeedItemExists(feedItemID uuid.UUID) error {
	exists, err := s.repo.FeedItemExists(feedItemID)
	if err != nil {
		return err
	}
	if !exists {
		return apperr.NotFound("feed.feed_item_not_found", "Feed item not found")
	}
	return nil
}

func filterTimeline(items []TimelineItemDTO, query FeedQuery) []TimelineItemDTO {
	filtered := items[:0]
	for _, item := range items {
		if query.IsRead != nil && item.IsRead != *query.IsRead {
			continue
		}
		if query.HideDuplicates && item.Type == "feed_item" && item.FeedItem != nil && item.FeedItem.IsDuplicate {
			continue
		}
		if !matchesTimelineSearch(item, query.Search) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func matchesTimelineSearch(item TimelineItemDTO, search string) bool {
	needle := strings.ToLower(strings.TrimSpace(search))
	if needle == "" {
		return true
	}
	for _, value := range timelineSearchValues(item) {
		if strings.Contains(strings.ToLower(value), needle) {
			return true
		}
	}
	return false
}

func timelineSearchValues(item TimelineItemDTO) []string {
	values := make([]string, 0, 6)
	if item.Post != nil {
		values = append(values, item.Post.Title, item.Post.Summary)
		if item.Post.Channel != nil {
			values = append(values, item.Post.Channel.Name, item.Post.Channel.Slug)
		}
	}
	if item.FeedItem != nil {
		values = append(values, item.FeedItem.Title, item.FeedItem.Summary)
		if item.FeedItem.FeedSource != nil {
			values = append(values, item.FeedItem.FeedSource.Title, item.FeedItem.FeedSource.RssURL)
		}
	}
	return values
}

func sortTimeline(items []TimelineItemDTO) {
	sort.Slice(items, func(i, j int) bool {
		return items[i].PublishedAt.After(items[j].PublishedAt)
	})
}

func paginateTimeline(items []TimelineItemDTO, page int, pageSize int) ([]TimelineItemDTO, int64) {
	total := int64(len(items))
	start := (page - 1) * pageSize
	if start > len(items) {
		start = len(items)
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], total
}

func normalizedPage(page int) int {
	if page < 1 {
		return 1
	}
	return page
}

func normalizedPageSize(pageSize int) int {
	if pageSize < 1 {
		return 20
	}
	if pageSize > 100 {
		return 100
	}
	return pageSize
}

func dedupeUUIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(ids))
	result := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id == uuid.Nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

func dedupePosts(posts []model.Post) []model.Post {
	seen := make(map[uuid.UUID]struct{}, len(posts))
	result := make([]model.Post, 0, len(posts))
	for _, post := range posts {
		if post.ID == uuid.Nil {
			continue
		}
		if _, ok := seen[post.ID]; ok {
			continue
		}
		seen[post.ID] = struct{}{}
		result = append(result, post)
	}
	return result
}
