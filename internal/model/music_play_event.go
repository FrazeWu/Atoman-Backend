package model

import (
	"time"

	"github.com/google/uuid"
)

// MusicPlayEvent 只负责有效播放请求的幂等，不改变客户端五秒判定。
type MusicPlayEvent struct {
	Identity   string     `gorm:"primaryKey;type:varchar(128)"`
	EventID    uuid.UUID  `gorm:"primaryKey;type:uuid"`
	SongID     uuid.UUID  `gorm:"type:uuid;not null"`
	PlaylistID *uuid.UUID `gorm:"type:uuid"`
	CreatedAt  time.Time  `gorm:"not null;index"`
}

func (MusicPlayEvent) TableName() string { return "music_play_events" }
