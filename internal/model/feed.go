package model

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type NullableUserUUID struct {
	uuid.UUID
	Valid bool
}

func NewNullableUserUUID(id uuid.UUID) NullableUserUUID {
	return NullableUserUUID{UUID: id, Valid: true}
}

func (n NullableUserUUID) MarshalJSON() ([]byte, error) {
	if !n.Valid {
		return []byte("null"), nil
	}
	return json.Marshal(n.UUID.String())
}

func (n *NullableUserUUID) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		n.UUID = uuid.Nil
		n.Valid = false
		return nil
	}

	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return err
	}
	n.UUID = id
	n.Valid = true
	return nil
}

func (n NullableUserUUID) Value() (driver.Value, error) {
	if !n.Valid {
		return nil, nil
	}
	return n.UUID.String(), nil
}

func (n *NullableUserUUID) Scan(value interface{}) error {
	if value == nil {
		n.UUID = uuid.Nil
		n.Valid = false
		return nil
	}

	switch v := value.(type) {
	case []byte:
		id, err := uuid.ParseBytes(v)
		if err != nil {
			return err
		}
		n.UUID = id
	case string:
		id, err := uuid.Parse(v)
		if err != nil {
			return err
		}
		n.UUID = id
	default:
		return fmt.Errorf("unsupported uuid scan type %T", value)
	}

	n.Valid = true
	return nil
}

type Channel struct {
	Base
	UserID      *uuid.UUID `json:"user_id,omitempty" gorm:"type:uuid;index"`
	User        *User      `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	Name        string     `json:"name" gorm:"not null;uniqueIndex:idx_channels_name"`
	Slug        string     `json:"slug" gorm:"uniqueIndex"`
	Description string     `json:"description" gorm:"type:text"`
	CoverURL    string     `json:"cover_url" gorm:"type:text"`
	IsAnonymous bool       `json:"is_anonymous" gorm:"default:false;index"`
	BanUntil    *time.Time `json:"ban_until,omitempty"`
	BanReason   string     `json:"ban_reason" gorm:"type:text"`
}

func (Channel) TableName() string { return "channels" }

type Collection struct {
	Base
	ChannelID   uuid.UUID  `json:"channel_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_collection_channel_type_name,priority:1"`
	Channel     *Channel   `json:"channel,omitempty" gorm:"foreignKey:ChannelID"`
	ContentType string     `json:"content_type" gorm:"type:varchar(16);not null;default:'blog';index;uniqueIndex:idx_collection_channel_type_name,priority:2"`
	CreatedBy   *uuid.UUID `json:"created_by,omitempty" gorm:"type:uuid;index"`
	Name        string     `json:"name" gorm:"not null;uniqueIndex:idx_collection_channel_type_name,priority:3"`
	Description string     `json:"description" gorm:"type:text"`
	CoverURL    string     `json:"cover_url" gorm:"type:text"`
	IsDefault   bool       `json:"is_default" gorm:"default:false;index"`
}

func (Collection) TableName() string { return "collections" }

type Post struct {
	Base
	UserID             uuid.UUID    `json:"user_id" gorm:"type:uuid;not null;index"`
	User               *User        `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	ChannelID          *uuid.UUID   `json:"channel_id,omitempty" gorm:"type:uuid;index"`
	Channel            *Channel     `json:"channel,omitempty" gorm:"foreignKey:ChannelID"`
	CollectionID       *uuid.UUID   `json:"collection_id,omitempty" gorm:"type:uuid;index"`
	Collection         *Collection  `json:"collection,omitempty" gorm:"foreignKey:CollectionID"`
	Collections        []Collection `json:"collections,omitempty" gorm:"many2many:post_collections;"`
	CollectionPosition int          `json:"collection_position" gorm:"not null;default:0"`
	Title              string       `json:"title" gorm:"not null"`
	Content            string       `json:"content" gorm:"type:text;not null"`
	Summary            string       `json:"summary" gorm:"type:text"`
	CoverURL           string       `json:"cover_url" gorm:"type:text"`
	Status             string       `json:"status" gorm:"default:'draft'"` // draft / published
	Visibility         string       `json:"visibility" gorm:"not null;default:'public'"`
	Pinned             bool         `json:"pinned" gorm:"default:false"`
	ScheduledAt        *time.Time   `json:"scheduled_at,omitempty" gorm:"index"`
	PublishedAt        *time.Time   `json:"published_at,omitempty" gorm:"index"`
	ViewCount          int64        `json:"view_count" gorm:"not null;default:0"`
}

func (Post) TableName() string { return "posts" }

type BlogPostVersion struct {
	Base
	PostID       uuid.UUID  `json:"post_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_blog_post_version,priority:1"`
	Version      int        `json:"version" gorm:"not null;uniqueIndex:idx_blog_post_version,priority:2"`
	EditorID     uuid.UUID  `json:"editor_id" gorm:"type:uuid;not null;index"`
	Title        string     `json:"title" gorm:"not null"`
	Content      string     `json:"content" gorm:"type:text;not null"`
	Summary      string     `json:"summary" gorm:"type:text"`
	CoverURL     string     `json:"cover_url" gorm:"type:text"`
	Visibility   string     `json:"visibility" gorm:"not null"`
	CollectionID uuid.UUID  `json:"collection_id" gorm:"type:uuid;not null;index"`
	PublishedAt  *time.Time `json:"published_at,omitempty"`
}

func (BlogPostVersion) TableName() string { return "blog_post_versions" }

type BlogDraft struct {
	Base
	UserID        uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_blog_drafts_user_context,priority:1"`
	ContextKey    string     `json:"context_key" gorm:"not null;uniqueIndex:idx_blog_drafts_user_context,priority:2"`
	SourcePostID  *uuid.UUID `json:"source_post_id,omitempty" gorm:"type:uuid;index"`
	Title         string     `json:"title"`
	Content       string     `json:"content" gorm:"type:text"`
	Summary       string     `json:"summary" gorm:"type:text"`
	CoverURL      string     `json:"cover_url" gorm:"type:text"`
	Visibility    string     `json:"visibility" gorm:"not null;default:'public'"`
	ChannelID     *uuid.UUID `json:"channel_id,omitempty" gorm:"type:uuid;index"`
	CollectionID  *uuid.UUID `json:"collection_id,omitempty" gorm:"type:uuid;index"`
	CollectionIDs string     `json:"-" gorm:"type:text"`
}

func (BlogDraft) TableName() string { return "blog_drafts" }

type PostCollection struct {
	PostID       uuid.UUID `json:"post_id" gorm:"type:uuid;primaryKey"`
	CollectionID uuid.UUID `json:"collection_id" gorm:"type:uuid;primaryKey"`
	Position     int       `json:"position" gorm:"not null;default:0"`
}

func (PostCollection) TableName() string { return "post_collections" }

type Like struct {
	Base
	UserID     uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_likes_user_target,priority:1,where:deleted_at IS NULL"`
	User       *User     `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	TargetType string    `json:"target_type" gorm:"not null;uniqueIndex:idx_likes_user_target,priority:2,where:deleted_at IS NULL"` // post / comment
	TargetID   uuid.UUID `json:"target_id" gorm:"type:uuid;not null;uniqueIndex:idx_likes_user_target,priority:3,where:deleted_at IS NULL"`
}

func (Like) TableName() string { return "likes" }

type BookmarkFolder struct {
	Base
	UserID uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index"`
	User   *User     `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	Name   string    `json:"name" gorm:"not null"`
}

func (BookmarkFolder) TableName() string { return "bookmark_folders" }

type Bookmark struct {
	Base
	UserID           uuid.UUID       `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_bookmarks_user_post,priority:1,where:deleted_at IS NULL"`
	User             *User           `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	PostID           uuid.UUID       `json:"post_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_bookmarks_user_post,priority:2,where:deleted_at IS NULL"`
	Post             *Post           `json:"post,omitempty" gorm:"foreignKey:PostID"`
	BookmarkFolderID *uuid.UUID      `json:"bookmark_folder_id" gorm:"type:uuid;index"`
	BookmarkFolder   *BookmarkFolder `json:"bookmark_folder,omitempty" gorm:"foreignKey:BookmarkFolderID"`
}

type ChannelBookmark struct {
	Base
	UserID    uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_channel_bookmarks_user_channel_kind,priority:1,where:deleted_at IS NULL"`
	User      *User     `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	ChannelID uuid.UUID `json:"channel_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_channel_bookmarks_user_channel_kind,priority:2,where:deleted_at IS NULL"`
	Channel   *Channel  `json:"channel,omitempty" gorm:"foreignKey:ChannelID"`
	Kind      string    `json:"kind" gorm:"not null;default:'video_channel';index;uniqueIndex:idx_channel_bookmarks_user_channel_kind,priority:3,where:deleted_at IS NULL"`
}

func (ChannelBookmark) TableName() string { return "channel_bookmarks" }

// FeedSource 存储全局唯一的订阅源元数据
type FeedSource struct {
	Base
	SourceType            string     `json:"source_type" gorm:"not null;index:idx_feed_sources_type_enabled,priority:1"` // internal_user | internal_channel | internal_collection | external_rss
	SourceID              *uuid.UUID `json:"source_id" gorm:"type:uuid"`                                                 // 站内资源 ID（外部 RSS 时为 null）
	Provider              string     `json:"provider" gorm:"not null;default:'rss';index"`
	Category              string     `json:"category" gorm:"not null;default:'blog';index"`
	RssURL                string     `json:"rss_url" gorm:"type:text"`
	CanonicalURL          string     `json:"canonical_url" gorm:"type:text;index"`
	SiteURL               string     `json:"site_url" gorm:"type:text"`
	Hash                  string     `json:"hash" gorm:"type:varchar(64);uniqueIndex"` // 唯一哈希
	Title                 string     `json:"title"`                                    // 全局默认标题
	CoverURL              string     `json:"cover_url" gorm:"type:text"`
	Hidden                bool       `json:"hidden" gorm:"not null;default:false;index"`
	HealthStatus          string     `json:"health_status" gorm:"not null;default:'healthy';index"`
	LastError             string     `json:"last_error" gorm:"type:text"`
	LastFetchedAt         *time.Time `json:"last_fetched_at"`
	FullTextEnabled       bool       `json:"full_text_enabled" gorm:"not null;default:false;index:idx_feed_sources_type_enabled,priority:2"`
	FullTextSuccessCount  int        `json:"full_text_success_count" gorm:"not null;default:0"`
	FullTextFailureCount  int        `json:"full_text_failure_count" gorm:"not null;default:0"`
	FullTextLastSuccessAt *time.Time `json:"full_text_last_success_at"`
	FullTextLastFailureAt *time.Time `json:"full_text_last_failure_at"`
	FullTextLastErrorCode string     `json:"full_text_last_error_code" gorm:"type:varchar(64)"`
	FullTextLastError     string     `json:"full_text_last_error" gorm:"type:text"`
}

func (FeedSource) TableName() string { return "feed_sources" }

// Subscription 存储用户与订阅源的多对多关系
type Subscription struct {
	Base
	UserID              uuid.UUID          `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_subscriptions_user_source,priority:1,where:deleted_at IS NULL"`
	User                *User              `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	FeedSourceID        uuid.UUID          `json:"feed_source_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_subscriptions_user_source,priority:2,where:deleted_at IS NULL"`
	FeedSource          *FeedSource        `json:"feed_source,omitempty" gorm:"foreignKey:FeedSourceID"`
	Title               string             `json:"title"`
	SubscriptionGroupID *uuid.UUID         `json:"subscription_group_id" gorm:"type:uuid;index"`
	SubscriptionGroup   *SubscriptionGroup `json:"subscription,omitempty" gorm:"foreignKey:SubscriptionGroupID"`
	HealthStatus        string             `json:"health_status" gorm:"default:'healthy'"` // healthy | warning | error
	ErrorMessage        string             `json:"error_message" gorm:"type:text"`
	LastChecked         *time.Time         `json:"last_checked"`
}

func (Subscription) TableName() string { return "subscriptions" }

type FeedItem struct {
	Base
	FeedSourceID          uuid.UUID   `json:"feed_source_id" gorm:"type:uuid;not null;index;index:idx_feed_items_source_status,priority:1;index:idx_feed_items_source_published,priority:1"`
	FeedSource            *FeedSource `json:"feed_source,omitempty" gorm:"foreignKey:FeedSourceID"`
	GUID                  string      `json:"guid" gorm:"not null"`
	Title                 string      `json:"title"`
	Link                  string      `json:"link" gorm:"type:text"`
	Summary               string      `json:"summary" gorm:"type:text"`
	Author                string      `json:"author"`
	PublishedAt           time.Time   `json:"published_at" gorm:"index:idx_feed_items_source_published,priority:2,sort:desc"`
	FetchedAt             time.Time   `json:"fetched_at"`
	EnclosureURL          string      `json:"enclosure_url" gorm:"type:text"`
	EnclosureType         string      `json:"enclosure_type"`
	Duration              string      `json:"duration"`
	ImageURL              string      `json:"image_url" gorm:"type:text"`
	FullTextHTML          string      `json:"full_text_html" gorm:"type:text"`
	FullTextStatus        string      `json:"full_text_status" gorm:"type:varchar(24);not null;default:'disabled';index:idx_feed_items_status_retry,priority:1;index:idx_feed_items_source_status,priority:2"`
	FullTextErrorCode     string      `json:"full_text_error_code" gorm:"type:varchar(64)"`
	FullTextError         string      `json:"full_text_error" gorm:"type:text"`
	FullTextAttemptCount  int         `json:"full_text_attempt_count" gorm:"not null;default:0"`
	LastFullTextAttemptAt *time.Time  `json:"last_full_text_attempt_at"`
	NextFullTextAttemptAt *time.Time  `json:"next_full_text_attempt_at" gorm:"index:idx_feed_items_status_retry,priority:2"`
	FullTextFetchedAt     *time.Time  `json:"full_text_fetched_at"`
	FullTextWordCount     int         `json:"full_text_word_count" gorm:"not null;default:0"`
	IsDuplicate           bool        `json:"is_duplicate" gorm:"-"`
	DuplicateCount        int         `json:"duplicate_count" gorm:"-"`
	DuplicateOfID         *uuid.UUID  `json:"duplicate_of_id,omitempty" gorm:"-"`
	DuplicateSources      []string    `json:"duplicate_sources,omitempty" gorm:"-"`
}

func (FeedItem) TableName() string { return "feed_items" }

type FeedItemRead struct {
	UserID     uuid.UUID `json:"user_id" gorm:"type:uuid;not null;primaryKey;index"`
	FeedItemID uuid.UUID `json:"feed_item_id" gorm:"type:uuid;not null;primaryKey;index"`
	ReadAt     time.Time `json:"read_at"`
}

func (FeedItemRead) TableName() string { return "feed_item_reads" }

type FeedStarGroup struct {
	Base
	UserID uuid.UUID `json:"user_id" gorm:"type:uuid;not null;index"`
	User   *User     `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	Name   string    `json:"name" gorm:"not null"`
}

func (FeedStarGroup) TableName() string { return "feed_star_groups" }

type FeedItemStar struct {
	UserID     uuid.UUID      `json:"user_id" gorm:"type:uuid;not null;primaryKey;index"`
	FeedItemID uuid.UUID      `json:"feed_item_id" gorm:"type:uuid;not null;primaryKey;index"`
	FeedItem   *FeedItem      `json:"feed_item,omitempty" gorm:"foreignKey:FeedItemID"`
	GroupID    *uuid.UUID     `json:"group_id" gorm:"type:uuid;index"`
	Group      *FeedStarGroup `json:"group,omitempty" gorm:"foreignKey:GroupID"`
	StarredAt  time.Time      `json:"starred_at"`
}

func (FeedItemStar) TableName() string { return "feed_item_stars" }

type ReadingListItem struct {
	UserID     uuid.UUID `json:"user_id" gorm:"type:uuid;not null;primaryKey;index"`
	TargetType string    `json:"target_type" gorm:"type:varchar(24);not null;primaryKey"`
	TargetID   uuid.UUID `json:"target_id" gorm:"type:uuid;not null;primaryKey;index"`
	FeedItem   *FeedItem `json:"feed_item,omitempty" gorm:"foreignKey:TargetID;references:ID;-:migration"`
	Post       *Post     `json:"post,omitempty" gorm:"foreignKey:TargetID;references:ID;-:migration"`
	CreatedAt  time.Time `json:"created_at"`
}

func (ReadingListItem) TableName() string { return "reading_list_items" }

type SourceReadEvent struct {
	Base
	SourceType string `json:"source_type" gorm:"not null;index:idx_source_read_events_source,priority:1"`
	SourceID   string `json:"source_id" gorm:"not null;index:idx_source_read_events_source,priority:2"`
	EventType  string `json:"event_type" gorm:"not null;index"`
}

func (SourceReadEvent) TableName() string { return "source_read_events" }

type SubscriptionGroup struct {
	Base
	UserID uuid.UUID `json:"user_id" gorm:"type:uuid;not null;uniqueIndex:idx_subscription_groups_user_name,priority:1,where:deleted_at IS NULL"`
	Name   string    `json:"name" gorm:"not null;uniqueIndex:idx_subscription_groups_user_name,priority:2,where:deleted_at IS NULL"`
}

func (SubscriptionGroup) TableName() string { return "subscription_groups" }
