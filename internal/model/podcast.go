package model

import "github.com/google/uuid"

// PodcastEpisode extends Post with audio-specific fields.
// Show (节目) = Channel; Episode (单集) = Post + PodcastEpisode.
// The relationship: PodcastEpisode.PostID -> Post.ID (one-to-one).
type PodcastEpisode struct {
	Base
	PostID    uuid.UUID `json:"post_id" gorm:"type:uuid;not null;uniqueIndex"`
	Post      *Post     `json:"post,omitempty" gorm:"foreignKey:PostID"`
	ChannelID uuid.UUID `json:"channel_id" gorm:"type:uuid;not null;index"`
	Channel   *Channel  `json:"channel,omitempty" gorm:"foreignKey:ChannelID"`
	// Audio file: always local upload (S3/MinIO)
	AudioURL    string `json:"audio_url" gorm:"type:text;not null"`
	DurationSec int    `json:"duration_sec" gorm:"default:0"`
	// Episode cover: optional; falls back to channel cover in RSS
	EpisodeCoverURL string `json:"episode_cover_url" gorm:"type:text"`
	// Episode ordering
	SeasonNumber  int `json:"season_number" gorm:"default:1"`
	EpisodeNumber int `json:"episode_number" gorm:"default:0"`
}

func (PodcastEpisode) TableName() string { return "podcast_episodes" }

type PodcastEpisodeBookmark struct {
	Base
	UserID    uuid.UUID       `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_podcast_episode_bookmarks_user_episode,priority:1,where:deleted_at IS NULL"`
	User      *User           `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	EpisodeID uuid.UUID       `json:"episode_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_podcast_episode_bookmarks_user_episode,priority:2,where:deleted_at IS NULL"`
	Episode   *PodcastEpisode `json:"episode,omitempty" gorm:"foreignKey:EpisodeID"`
}

func (PodcastEpisodeBookmark) TableName() string { return "podcast_episode_bookmarks" }
