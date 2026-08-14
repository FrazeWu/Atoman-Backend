package model

import (
	"time"

	"github.com/google/uuid"
)

const (
	DMPartyUser    = "user"
	DMPartyChannel = "channel"

	DMPermissionOneBeforeReply = "one_before_reply"
	DMPermissionFollowingOnly  = "following_only"
	DMPermissionAnyone         = "anyone"
	DMPermissionClosed         = "closed"

	DMReportPending   = "pending"
	DMReportResolved  = "resolved"
	DMReportDismissed = "dismissed"
)

type DMConversation struct {
	Base
	ParticipantAType   string     `json:"-" gorm:"type:varchar(16);not null;default:'user'"`
	ParticipantA       uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	ParticipantBType   string     `json:"-" gorm:"type:varchar(16);not null;default:'user'"`
	ParticipantB       uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	LastMessageAt      *time.Time `json:"-"`
	LastMessagePreview string     `json:"-" gorm:"size:100"`
}

func (DMConversation) TableName() string { return "dm_conversations" }

type DMMessage struct {
	Base
	ConversationID uuid.UUID `json:"conversation_id" gorm:"type:uuid;not null;index"`
	SenderType     string    `json:"-" gorm:"type:varchar(16);not null;default:'user'"`
	SenderID       uuid.UUID `json:"sender_id" gorm:"type:uuid;not null;index"`
	// Deprecated: retained only while the legacy DM handler is still compiled.
	Sender          *User      `json:"sender,omitempty" gorm:"-"`
	ActorUserID     uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	ClientMessageID uuid.UUID  `json:"-" gorm:"type:uuid;not null"`
	Content         string     `json:"content" gorm:"type:text"`
	ImageID         *uuid.UUID `json:"-" gorm:"type:uuid"`
	// Deprecated: legacy data remains in this column until image migration runs.
	ImageURL string     `json:"image_url" gorm:"column:image_url"`
	ReadAt   *time.Time `json:"read_at"`
}

func (DMMessage) TableName() string { return "dm_messages" }

type DMImage struct {
	Base
	UploadedByUserID uuid.UUID `json:"-" gorm:"type:uuid;not null;index"`
	ObjectKey        string    `json:"-" gorm:"type:text;not null"`
	ContentType      string    `json:"-" gorm:"type:varchar(64);not null"`
	SizeBytes        int64     `json:"-" gorm:"not null"`
}

func (DMImage) TableName() string { return "dm_images" }

type DMChannelSettings struct {
	ChannelID  uuid.UUID `json:"-" gorm:"type:uuid;primaryKey"`
	Permission string    `json:"-" gorm:"type:varchar(32);not null;default:'one_before_reply'"`
}

func (DMChannelSettings) TableName() string { return "dm_channel_settings" }

type DMMessageReport struct {
	Base
	MessageID           uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	ReporterUserID      uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	ReportedActorUserID uuid.UUID  `json:"-" gorm:"type:uuid;not null;index"`
	Reason              string     `json:"-" gorm:"type:varchar(64);not null"`
	Detail              string     `json:"-" gorm:"type:text"`
	SnapshotContent     string     `json:"-" gorm:"type:text"`
	SnapshotImageKey    string     `json:"-" gorm:"type:text"`
	Status              string     `json:"-" gorm:"type:varchar(16);not null;default:'pending'"`
	ReviewedByUserID    *uuid.UUID `json:"-" gorm:"type:uuid"`
	ReviewedAt          *time.Time `json:"-"`
}

func (DMMessageReport) TableName() string { return "dm_message_reports" }
