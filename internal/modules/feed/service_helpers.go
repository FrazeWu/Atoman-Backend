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
