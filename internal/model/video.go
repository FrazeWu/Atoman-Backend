package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Video represents a video post published under a channel.
type Video struct {
	Base
	ChannelID   *uuid.UUID `json:"channel_id,omitempty" gorm:"type:uuid;index"`
	Channel     *Channel   `json:"channel,omitempty" gorm:"foreignKey:ChannelID"`
	UserID      uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;index"`
	User        *User      `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	Title       string     `json:"title" gorm:"not null"`
	Description string     `json:"description" gorm:"type:text"`
	// StorageType: "local" (S3/MinIO) or "external" (YouTube, Bilibili, etc.)
	StorageType       string          `json:"storage_type" gorm:"not null;default:'external'"` // local | external
	VideoURL          string          `json:"video_url" gorm:"type:text;not null"`             // S3 key or external URL
	ThumbnailURL      string          `json:"thumbnail_url" gorm:"type:text"`
	DurationSec       int             `json:"duration_sec" gorm:"default:0"`
	ProcessingStatus  string          `json:"processing_status" gorm:"not null;default:'none'"`
	ProcessingError   string          `json:"processing_error" gorm:"type:text"`
	PreviewThumbnails json.RawMessage `json:"preview_thumbnails" gorm:"type:jsonb"`
	// Visibility: public | followers | private
	Visibility  string       `json:"visibility" gorm:"not null;default:'public'"`
	Status      string       `json:"status" gorm:"not null;default:'draft'"` // draft | published
	ViewCount   int          `json:"view_count" gorm:"default:0"`
	Tags        []VideoTag   `json:"tags,omitempty" gorm:"many2many:video_tag_relations;joinForeignKey:VideoID;joinReferences:TagID"`
	Collections []Collection `json:"collections,omitempty" gorm:"many2many:video_collections;"`
}

func (Video) TableName() string { return "videos" }

type VideoProcessingJob struct {
	Base
	VideoID    uuid.UUID  `json:"video_id" gorm:"type:uuid;not null;index"`
	Video      *Video     `json:"video,omitempty" gorm:"foreignKey:VideoID"`
	Status     string     `json:"status" gorm:"not null;default:'pending';index"`
	JobType    string     `json:"job_type" gorm:"not null;default:'thumbnail_preview';index"`
	Attempts   int        `json:"attempts" gorm:"not null;default:0"`
	LastError  string     `json:"last_error" gorm:"type:text"`
	LockedAt   *time.Time `json:"locked_at"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

func (VideoProcessingJob) TableName() string { return "video_processing_jobs" }

// VideoTag is a reusable tag for video discovery.
type VideoTag struct {
	Base
	Name string `json:"name" gorm:"uniqueIndex;not null"`
}

func (VideoTag) TableName() string { return "video_tags" }

// VideoCollection is the join table between Video and Collection.
type VideoCollection struct {
	VideoID      uuid.UUID `json:"video_id" gorm:"type:uuid;primaryKey"`
	CollectionID uuid.UUID `json:"collection_id" gorm:"type:uuid;primaryKey"`
}

func (VideoCollection) TableName() string { return "video_collections" }

// VideoTagRelation is the join table between Video and VideoTag.
type VideoTagRelation struct {
	VideoID uuid.UUID `json:"video_id" gorm:"type:uuid;primaryKey"`
	TagID   uuid.UUID `json:"tag_id" gorm:"type:uuid;primaryKey"`
}

func (VideoTagRelation) TableName() string { return "video_tag_relations" }
