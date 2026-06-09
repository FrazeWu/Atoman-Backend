package model

import (
	"time"

	"github.com/google/uuid"
)

// TimelineEvent represents a historical event on the timeline
type TimelineEvent struct {
	Base
	UserID      uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;index"`
	User        *User      `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	Title       string     `json:"title" gorm:"not null"`
	Description string     `json:"description" gorm:"type:text"`
	Content     string     `json:"content" gorm:"type:text"`
	EventDate   time.Time  `json:"event_date" gorm:"not null;index"`
	EndDate     *time.Time `json:"end_date,omitempty"`
	Location    string     `json:"location" gorm:"not null;default:''"` // 事件发生地点描述（必填）
	Latitude    *float64   `json:"latitude,omitempty" gorm:"default:null"`  // 地理坐标（可选）
	Longitude   *float64   `json:"longitude,omitempty" gorm:"default:null"`
	Source      string     `json:"source" gorm:"not null;default:''"` // 来源/参考资料（必填）
	Category    string     `json:"category" gorm:"index"`
	Tags        []string   `json:"tags" gorm:"type:text[]"`
	IsPublic    bool       `json:"is_public" gorm:"default:true"`
}

func (TimelineEvent) TableName() string { return "timeline_events" }

// TimelinePerson represents a historical person with a geographic trajectory
type TimelinePerson struct {
	Base
	UserID    uuid.UUID        `json:"user_id" gorm:"type:uuid;not null;index"`
	User      *User            `json:"user,omitempty" gorm:"foreignKey:UserID;references:UUID"`
	Name      string           `json:"name" gorm:"not null"`
	Bio       string           `json:"bio" gorm:"type:text"`
	BirthDate *time.Time       `json:"birth_date,omitempty"`
	DeathDate *time.Time       `json:"death_date,omitempty"`
	Tags      []string         `json:"tags" gorm:"type:text[]"`
	IsPublic  bool             `json:"is_public" gorm:"default:true"`
	Locations []PersonLocation `json:"locations,omitempty" gorm:"-"`
}

func (TimelinePerson) TableName() string { return "timeline_persons" }

// PersonLocation records a historical person's location at a specific time
type PersonLocation struct {
	Base
	PersonID  uuid.UUID       `json:"person_id" gorm:"type:uuid;not null;index"`
	Person    *TimelinePerson `json:"person,omitempty" gorm:"foreignKey:PersonID"`
	Date      time.Time       `json:"date" gorm:"not null;index"`
	EndDate   *time.Time      `json:"end_date,omitempty"`
	PlaceName string          `json:"place_name" gorm:"not null"`
	Latitude  float64         `json:"latitude" gorm:"not null"`
	Longitude float64         `json:"longitude" gorm:"not null"`
	Source    string          `json:"source" gorm:"not null;default:''"` // 来源/参考资料（必填）
	Note      string          `json:"note" gorm:"type:text"`
}

func (PersonLocation) TableName() string { return "person_locations" }

// TimelineRevision stores a snapshot of a TimelineEvent at a given point in time.
type TimelineRevision struct {
	Base
	EventID     uuid.UUID `json:"event_id" gorm:"type:uuid;not null;index"`
	EditorID    uuid.UUID `json:"editor_id" gorm:"type:uuid;not null"`
	Editor      *User     `json:"editor,omitempty" gorm:"foreignKey:EditorID;references:UUID"`
	Title       string    `json:"title"`
	Description string    `json:"description" gorm:"type:text"`
	Content     string    `json:"content" gorm:"type:text"`
	EventDate   string    `json:"event_date"`
	EndDate     string    `json:"end_date"`
	Location    string    `json:"location"`
	Source      string    `json:"source"`
	Category    string    `json:"category"`
	IsPublic    bool      `json:"is_public"`
}

func (TimelineRevision) TableName() string { return "timeline_revisions" }
