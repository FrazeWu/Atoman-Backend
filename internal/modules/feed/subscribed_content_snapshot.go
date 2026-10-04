package feed

import (
	"atoman/internal/model"
	"github.com/google/uuid"
)

func buildSubscribedTimelineItems(
	posts []model.Post,
	videos []model.Video,
	feedItems []model.FeedItem,
	shortNotes []model.ShortNote,
	shortNoteRead map[uuid.UUID]bool,
	engagementByPostID map[uuid.UUID]PostEngagementCount,
	episodeByPostID map[uuid.UUID]model.PodcastEpisode,
	readMap map[uuid.UUID]bool,
) []TimelineItemDTO {
	items := make([]TimelineItemDTO, 0, len(posts)+len(videos)+len(feedItems)+len(shortNotes))
	for i := range shortNotes {
		items = append(items, TimelineItemDTO{Type: "short_note", ShortNote: &shortNotes[i], PublishedAt: shortNotes[i].CreatedAt, IsRead: shortNoteRead[shortNotes[i].ID]})
	}
	for i := range posts {
		if episode, ok := episodeByPostID[posts[i].ID]; ok {
			episode.Post = &posts[i]
			items = append(items, TimelineItemDTO{Type: "podcast_episode", PodcastEpisode: &episode, PublishedAt: postTimelinePublishedAt(posts[i])})
			continue
		}
		items = append(items, TimelineItemDTO{Type: "post", Post: timelinePostDTO(posts[i], engagementByPostID[posts[i].ID]), PublishedAt: postTimelinePublishedAt(posts[i])})
	}
	for i := range videos {
		items = append(items, TimelineItemDTO{Type: "video", Video: &videos[i], PublishedAt: videos[i].CreatedAt})
	}
	for i := range feedItems {
		items = append(items, TimelineItemDTO{Type: timelineFeedItemType(&feedItems[i]), FeedItem: &feedItems[i], PublishedAt: feedItems[i].PublishedAt, IsRead: feedItemClusterRead(feedItems[i], readMap)})
	}
	return items
}
