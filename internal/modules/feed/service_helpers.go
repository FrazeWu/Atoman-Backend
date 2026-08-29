package feed

import (
	"sort"
	"strings"

	"atoman/internal/model"

	"github.com/google/uuid"
)

func filterTimeline(items []TimelineItemDTO, query FeedQuery) []TimelineItemDTO {
	hasSearch := strings.TrimSpace(query.Search) != ""
	mergedSearchMatches := make(map[uuid.UUID]bool)
	if query.HideDuplicates && hasSearch {
		for _, item := range items {
			if item.Type != "feed_item" || item.FeedItem == nil || !matchesTimelineSearch(item, query.Search) {
				continue
			}
			primaryID := item.FeedItem.ID
			if item.FeedItem.DuplicateOfID != nil {
				primaryID = *item.FeedItem.DuplicateOfID
			}
			mergedSearchMatches[primaryID] = true
		}
	}

	filtered := items[:0]
	for _, item := range items {
		if query.Category != "" && timelineItemCategory(item) != query.Category {
			continue
		}
		if query.LanguageCode != "" && timelineItemLanguageCode(item) != query.LanguageCode {
			continue
		}
		if query.IsRead != nil && item.IsRead != *query.IsRead {
			continue
		}
		if query.HideDuplicates && item.Type == "feed_item" && item.FeedItem != nil && item.FeedItem.IsDuplicate {
			continue
		}
		if query.HideDuplicates && hasSearch && item.Type == "feed_item" && item.FeedItem != nil {
			if !mergedSearchMatches[item.FeedItem.ID] {
				continue
			}
		} else if !matchesTimelineSearch(item, query.Search) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func timelineItemCategory(item TimelineItemDTO) string {
	if item.Post != nil {
		return "blog"
	}
	if item.FeedItem != nil {
		enclosureType := strings.ToLower(strings.TrimSpace(item.FeedItem.EnclosureType))
		if strings.HasPrefix(enclosureType, "video/") {
			return "video"
		}
		if strings.HasPrefix(enclosureType, "audio/") {
			return "podcast"
		}
		if item.FeedItem.FeedSource != nil {
			return defaultFeedSourceCategory(item.FeedItem.FeedSource.Category)
		}
	}
	return ""
}

func timelineItemLanguageCode(item TimelineItemDTO) string {
	if item.Post != nil {
		return strings.TrimSpace(item.Post.LanguageCode)
	}
	if item.FeedItem != nil {
		language := strings.TrimSpace(item.FeedItem.LanguageCode)
		if language != "" {
			return language
		}
		if item.FeedItem.FeedSource != nil {
			return strings.TrimSpace(item.FeedItem.FeedSource.LanguageCode)
		}
	}
	return ""
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
	values := make([]string, 0, 7)
	if item.Post != nil {
		values = append(values, item.Post.Title, item.Post.Summary)
		if item.Post.Channel != nil {
			values = append(values, item.Post.Channel.Name, item.Post.Channel.Slug)
		}
	}
	if item.FeedItem != nil {
		values = append(values, item.FeedItem.Title, item.FeedItem.Summary, item.FeedItem.ReaderHTML, item.FeedItem.FullTextHTML)
		if item.FeedItem.FeedSource != nil {
			values = append(values, item.FeedItem.FeedSource.Title, item.FeedItem.FeedSource.RssURL)
		}
	}
	if item.PodcastEpisode != nil && item.PodcastEpisode.Post != nil {
		values = append(values, item.PodcastEpisode.Post.Title, item.PodcastEpisode.Post.Summary)
		if item.PodcastEpisode.Channel != nil {
			values = append(values, item.PodcastEpisode.Channel.Name, item.PodcastEpisode.Channel.Slug)
		}
	}
	if item.Video != nil {
		values = append(values, item.Video.Title, item.Video.Description)
		if item.Video.Channel != nil {
			values = append(values, item.Video.Channel.Name, item.Video.Channel.Slug)
		}
	}
	return values
}

func sortTimeline(items []TimelineItemDTO) {
	sort.Slice(items, func(i, j int) bool {
		if !items[i].PublishedAt.Equal(items[j].PublishedAt) {
			return items[i].PublishedAt.After(items[j].PublishedAt)
		}
		if items[i].Type != items[j].Type {
			return timelineTypeRank(items[i].Type) < timelineTypeRank(items[j].Type)
		}
		return timelineItemID(items[i]) > timelineItemID(items[j])
	})
}

const priorityInboxLimit = 20

func isPriorityInboxSort(raw string) bool {
	return strings.EqualFold(strings.TrimSpace(raw), "priority")
}

type subscriptionPriorityIndex struct {
	feedSources map[uuid.UUID]string
	users       map[uuid.UUID]string
	channels    map[uuid.UUID]string
	collections map[uuid.UUID]string
}

func applySubscriptionPriority(items []TimelineItemDTO, priorities subscriptionPriorityIndex) {
	for index := range items {
		priority := priorities.priorityForItem(items[index])
		items[index].priorityRank = subscriptionPriorityRank(priority)
		items[index].PriorityReason = "subscription_priority_" + priority
	}
}

func newSubscriptionPriorityIndex(subscriptions []model.Subscription) subscriptionPriorityIndex {
	priorities := subscriptionPriorityIndex{
		feedSources: make(map[uuid.UUID]string, len(subscriptions)),
		users:       make(map[uuid.UUID]string),
		channels:    make(map[uuid.UUID]string),
		collections: make(map[uuid.UUID]string),
	}
	for _, subscription := range subscriptions {
		priority, ok := normalizeSubscriptionPriority(subscription.Priority)
		if !ok {
			priority = subscriptionPriorityNormal
		}
		source := subscription.FeedSource
		if source == nil {
			priorities.assign(priorities.feedSources, subscription.FeedSourceID, priority)
			continue
		}
		switch source.SourceType {
		case "internal_user":
			if source.SourceID != nil {
				priorities.assign(priorities.users, *source.SourceID, priority)
			}
		case "internal_channel":
			if source.SourceID != nil {
				priorities.assign(priorities.channels, *source.SourceID, priority)
			}
		case "internal_collection":
			if source.SourceID != nil {
				priorities.assign(priorities.collections, *source.SourceID, priority)
			}
		default:
			priorities.assign(priorities.feedSources, subscription.FeedSourceID, priority)
		}
	}
	return priorities
}

func (priorities subscriptionPriorityIndex) assign(target map[uuid.UUID]string, id uuid.UUID, priority string) {
	if id == uuid.Nil {
		return
	}
	target[id] = higherSubscriptionPriority(target[id], priority)
}

func (priorities subscriptionPriorityIndex) priorityForItem(item TimelineItemDTO) string {
	if item.FeedItem != nil {
		return highestConfiguredPriority(priorities.feedSources[item.FeedItem.FeedSourceID])
	}
	if item.Post != nil {
		return priorities.priorityForPost(&item.Post.Post)
	}
	if item.PodcastEpisode != nil {
		if item.PodcastEpisode.Post != nil {
			return priorities.priorityForPost(item.PodcastEpisode.Post)
		}
		return highestConfiguredPriority(priorities.channels[item.PodcastEpisode.ChannelID])
	}
	if item.Video != nil {
		candidates := []string{priorities.users[item.Video.UserID]}
		if item.Video.ChannelID != nil {
			candidates = append(candidates, priorities.channels[*item.Video.ChannelID])
		}
		if item.Video.CollectionID != nil {
			candidates = append(candidates, priorities.collections[*item.Video.CollectionID])
		}
		return highestConfiguredPriority(candidates...)
	}
	return subscriptionPriorityNormal
}

func (priorities subscriptionPriorityIndex) priorityForPost(post *model.Post) string {
	candidates := []string{priorities.users[post.UserID]}
	if post.ChannelID != nil {
		candidates = append(candidates, priorities.channels[*post.ChannelID])
	}
	if post.CollectionID != nil {
		candidates = append(candidates, priorities.collections[*post.CollectionID])
	}
	return highestConfiguredPriority(candidates...)
}

func highestConfiguredPriority(candidates ...string) string {
	priority := ""
	for _, candidate := range candidates {
		normalized, ok := normalizeSubscriptionPriority(candidate)
		if !ok {
			continue
		}
		if priority == "" || subscriptionPriorityRank(normalized) > subscriptionPriorityRank(priority) {
			priority = normalized
		}
	}
	if priority == "" {
		return subscriptionPriorityNormal
	}
	return priority
}

func higherSubscriptionPriority(current string, candidate string) string {
	normalizedCurrent, currentOK := normalizeSubscriptionPriority(current)
	normalizedCandidate, candidateOK := normalizeSubscriptionPriority(candidate)
	if !currentOK {
		if candidateOK {
			return normalizedCandidate
		}
		return subscriptionPriorityNormal
	}
	if !candidateOK {
		return normalizedCurrent
	}
	if subscriptionPriorityRank(normalizedCandidate) > subscriptionPriorityRank(normalizedCurrent) {
		return normalizedCandidate
	}
	return normalizedCurrent
}

func subscriptionPriorityRank(priority string) int {
	switch priority {
	case subscriptionPriorityHigh:
		return 3
	case subscriptionPriorityNormal:
		return 2
	case subscriptionPriorityLow:
		return 1
	default:
		return 2
	}
}

func priorityInbox(items []TimelineItemDTO) ([]TimelineItemDTO, int64) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].priorityRank != items[j].priorityRank {
			return items[i].priorityRank > items[j].priorityRank
		}
		if !items[i].PublishedAt.Equal(items[j].PublishedAt) {
			return items[i].PublishedAt.After(items[j].PublishedAt)
		}
		if items[i].Type != items[j].Type {
			return timelineTypeRank(items[i].Type) < timelineTypeRank(items[j].Type)
		}
		return timelineItemID(items[i]) > timelineItemID(items[j])
	})
	if len(items) > priorityInboxLimit {
		items = items[:priorityInboxLimit]
	}
	return items, int64(len(items))
}

func timelineTypeRank(itemType string) int {
	switch itemType {
	case "post":
		return 0
	case "podcast_episode":
		return 1
	case "video":
		return 2
	case "feed_item":
		return 3
	default:
		return 2
	}
}

func timelineItemID(item TimelineItemDTO) string {
	if item.Post != nil {
		return item.Post.ID.String()
	}
	if item.FeedItem != nil {
		return item.FeedItem.ID.String()
	}
	if item.PodcastEpisode != nil {
		return item.PodcastEpisode.ID.String()
	}
	if item.Video != nil {
		return item.Video.ID.String()
	}
	return ""
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

func dedupeVideos(videos []model.Video) []model.Video {
	seen := make(map[uuid.UUID]struct{}, len(videos))
	result := make([]model.Video, 0, len(videos))
	for _, video := range videos {
		if video.ID == uuid.Nil {
			continue
		}
		if _, exists := seen[video.ID]; exists {
			continue
		}
		seen[video.ID] = struct{}{}
		result = append(result, video)
	}
	return result
}
