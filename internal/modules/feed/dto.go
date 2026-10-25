package feed

import (
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/httpx"

	"github.com/google/uuid"
)

type FeedQuery struct {
	Page               int       `json:"page" form:"page"`
	PageSize           int       `json:"page_size" form:"page_size"`
	ContentType        string    `json:"content_type" form:"content_type"`
	Category           string    `json:"category" form:"category"`
	LanguageCode       string    `json:"language" form:"language"`
	SourceType         string    `json:"source_type" form:"source_type"`
	SourceID           uuid.UUID `json:"source_id" form:"source_id"`
	GroupID            uuid.UUID `json:"group_id" form:"group_id"`
	IsRead             *bool     `json:"is_read" form:"is_read"`
	HideDuplicates     bool      `json:"hide_duplicates" form:"hide_duplicates"`
	Sort               string    `json:"sort" form:"sort"`
	Search             string    `json:"q" form:"q"`
	viewerID           uuid.UUID
	sourceVisibleAfter map[uuid.UUID]time.Time
}

type TimelineItemDTO struct {
	Type           string                     `json:"type"`
	Post           *TimelinePostDTO           `json:"post,omitempty"`
	ShortNote      *TimelineShortNoteDTO      `json:"short_note,omitempty"`
	PodcastEpisode *TimelinePodcastEpisodeDTO `json:"podcast_episode,omitempty"`
	Video          *TimelineVideoDTO          `json:"video,omitempty"`
	FeedItem       *model.FeedItem            `json:"feed_item,omitempty"`
	PublishedAt    time.Time                  `json:"published_at"`
	IsRead         bool                       `json:"is_read"`
	PriorityReason string                     `json:"priority_reason,omitempty"`
	priorityRank   int
}

type TimelinePostDTO struct {
	model.Post
	User           *model.PublicUserDTO    `json:"user,omitempty"`
	Channel        *TimelineChannelDTO     `json:"channel,omitempty"`
	Collection     *TimelineCollectionDTO  `json:"collection,omitempty"`
	Collections    []TimelineCollectionDTO `json:"collections,omitempty"`
	LikesCount     int64                   `json:"likes_count,omitempty"`
	CommentsCount  int64                   `json:"comments_count,omitempty"`
	BookmarksCount int64                   `json:"bookmarks_count,omitempty"`
}

type TimelineChannelDTO struct {
	model.Channel
	User *model.PublicUserDTO `json:"user,omitempty"`
}

type TimelineCollectionDTO struct {
	model.Collection
	Channel *TimelineChannelDTO `json:"channel,omitempty"`
}

type TimelineShortNoteDTO struct {
	model.ShortNote
	User *model.PublicUserDTO `json:"user,omitempty"`
}

type TimelinePodcastEpisodeDTO struct {
	model.PodcastEpisode
	Post    *TimelinePostDTO    `json:"post,omitempty"`
	Channel *TimelineChannelDTO `json:"channel,omitempty"`
}

type TimelineVideoDTO struct {
	model.Video
	User        *model.PublicUserDTO    `json:"user,omitempty"`
	Channel     *TimelineChannelDTO     `json:"channel,omitempty"`
	Collection  *TimelineCollectionDTO  `json:"collection,omitempty"`
	Collections []TimelineCollectionDTO `json:"collections,omitempty"`
}

func timelineChannelDTO(channel *model.Channel) *TimelineChannelDTO {
	if channel == nil {
		return nil
	}
	copy := *channel
	copy.User = nil
	return &TimelineChannelDTO{Channel: copy, User: model.PublicUserFromUser(channel.User)}
}

func timelineCollectionDTO(collection *model.Collection) *TimelineCollectionDTO {
	if collection == nil {
		return nil
	}
	copy := *collection
	copy.Channel = nil
	return &TimelineCollectionDTO{Collection: copy, Channel: timelineChannelDTO(collection.Channel)}
}

func timelineCollectionsDTO(collections []model.Collection) []TimelineCollectionDTO {
	if len(collections) == 0 {
		return nil
	}
	result := make([]TimelineCollectionDTO, 0, len(collections))
	for index := range collections {
		if collection := timelineCollectionDTO(&collections[index]); collection != nil {
			result = append(result, *collection)
		}
	}
	return result
}

func timelinePostDTOFromModel(post model.Post) *TimelinePostDTO {
	copy := post
	copy.User, copy.Channel, copy.Collection, copy.Collections = nil, nil, nil, nil
	return &TimelinePostDTO{
		Post: copy, User: model.PublicUserFromUser(post.User),
		Channel: timelineChannelDTO(post.Channel), Collection: timelineCollectionDTO(post.Collection),
		Collections: timelineCollectionsDTO(post.Collections),
	}
}

func timelineShortNoteDTO(note model.ShortNote) *TimelineShortNoteDTO {
	copy := note
	copy.User = nil
	return &TimelineShortNoteDTO{ShortNote: copy, User: model.PublicUserFromUser(note.User)}
}

func timelinePodcastEpisodeDTO(episode model.PodcastEpisode) *TimelinePodcastEpisodeDTO {
	copy := episode
	copy.Post, copy.Channel = nil, nil
	var post *TimelinePostDTO
	if episode.Post != nil {
		post = timelinePostDTOFromModel(*episode.Post)
	}
	return &TimelinePodcastEpisodeDTO{
		PodcastEpisode: copy, Post: post, Channel: timelineChannelDTO(episode.Channel),
	}
}

func timelineVideoDTO(video model.Video) *TimelineVideoDTO {
	copy := video
	copy.User, copy.Channel, copy.Collection, copy.Collections = nil, nil, nil, nil
	collections := make([]model.Collection, 0, len(video.Collections))
	collections = append(collections, video.Collections...)
	return &TimelineVideoDTO{
		Video: copy, User: model.PublicUserFromUser(video.User), Channel: timelineChannelDTO(video.Channel),
		Collection: timelineCollectionDTO(video.Collection), Collections: timelineCollectionsDTO(collections),
	}
}

type TimelineListResponseDTO struct {
	Data []TimelineItemDTO `json:"data"`
	Meta TimelineMeta      `json:"meta"`
}

type TimelineMeta struct {
	httpx.PageMeta
	CheckedAt time.Time `json:"checked_at"`
}

type TimelineUpdatesResponse struct {
	HasUpdates bool      `json:"has_updates"`
	CheckedAt  time.Time `json:"checked_at"`
}

type ToggleStateDTO struct {
	Active bool `json:"active"`
}

type RecommendationItemDTO struct {
	ID                   string                     `json:"id"`
	Title                string                     `json:"title"`
	Summary              string                     `json:"summary"`
	Description          string                     `json:"description"`
	ContentType          string                     `json:"content_type"`
	LanguageCode         string                     `json:"language_code,omitempty"`
	ImageURL             string                     `json:"image_url"`
	TargetPath           string                     `json:"target_path"`
	ScoreLabel           string                     `json:"score_label"`
	PlayCount            int64                      `json:"play_count"`
	BookmarkCount        int64                      `json:"bookmark_count"`
	BirthYear            int                        `json:"birth_year,omitempty"`
	BirthDate            *time.Time                 `json:"birth_date,omitempty"`
	ReadCount            int64                      `json:"read_count"`
	ViewCount            int64                      `json:"view_count"`
	RatingScore          float64                    `json:"rating_score"`
	RatingCount          int64                      `json:"rating_count"`
	UpdateFrequencyLabel string                     `json:"update_frequency_label"`
	LastPublishedAt      *time.Time                 `json:"last_published_at,omitempty"`
	SourceID             string                     `json:"source_id,omitempty"`
	SourceTitle          string                     `json:"source_title,omitempty"`
	SourceImageURL       string                     `json:"source_image_url,omitempty"`
	SourceType           string                     `json:"source_type,omitempty"`
	SourceCategory       string                     `json:"source_category,omitempty"`
	Platform             string                     `json:"platform,omitempty"`
	SourceContentType    string                     `json:"source_content_type,omitempty"`
	SourcePath           string                     `json:"source_path,omitempty"`
	RssURL               string                     `json:"rss_url,omitempty"`
	RecentItems          []RecommendationPreviewDTO `json:"recent_items,omitempty"`
}

type RecommendationThemeDTO struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

type RecommendationPreviewDTO struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
