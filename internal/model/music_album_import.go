package model

import (
	"time"

	"github.com/google/uuid"
)

type AlbumImportSession struct {
	Base
	UserID          *uuid.UUID        `json:"user_id,omitempty" gorm:"type:uuid;index"`
	InputMode       string            `json:"input_mode" gorm:"not null;default:'auto'"`
	Status          string            `json:"status" gorm:"not null;default:'pending_upload'"`
	Stage           string            `json:"stage" gorm:"not null;default:'upload'"`
	ProgressCurrent int64             `json:"progress_current" gorm:"not null;default:0"`
	ProgressTotal   int64             `json:"progress_total" gorm:"not null;default:0"`
	PayloadJSON     string            `json:"payload" gorm:"type:text;not null"`
	ErrorMessage    string            `json:"error_message" gorm:"type:text;not null;default:''"`
	ExpiresAt       *time.Time        `json:"expires_at" gorm:"index"`
	CommittedAt     *time.Time        `json:"committed_at"`
	CommittedBy     *uuid.UUID        `json:"committed_by" gorm:"type:uuid"`
	Files           []AlbumImportFile `json:"files,omitempty" gorm:"foreignKey:ImportID"`
	Job             *AlbumImportJob   `json:"job,omitempty" gorm:"foreignKey:ImportID"`
}

func (AlbumImportSession) TableName() string {
	return "music_album_import_sessions"
}

type AlbumImportFile struct {
	Base
	ImportID         uuid.UUID `json:"import_id" gorm:"type:uuid;not null;index"`
	RelativePath     string    `json:"relative_path" gorm:"not null;default:''"`
	FileName         string    `json:"file_name" gorm:"not null"`
	Role             string    `json:"role" gorm:"not null;default:'unknown'"`
	DetectedFormat   string    `json:"detected_format"`
	ContentType      string    `json:"content_type"`
	Size             int64     `json:"size" gorm:"not null;default:0"`
	SourceKey        string    `json:"source_key" gorm:"type:text"`
	PlaybackKey      string    `json:"playback_key" gorm:"type:text"`
	UploadStatus     string    `json:"upload_status" gorm:"not null;default:'pending'"`
	ProcessingStatus string    `json:"processing_status" gorm:"not null;default:'pending'"`
	DiscNumber       int       `json:"disc_number" gorm:"not null;default:0"`
	TrackNumber      int       `json:"track_number" gorm:"not null;default:0"`
	Title            string    `json:"title"`
	DurationSeconds  float64   `json:"duration_seconds" gorm:"not null;default:0"`
	MetadataJSON     string    `json:"metadata" gorm:"type:text;not null;default:'{}'"`
	ErrorMessage     string    `json:"error_message" gorm:"type:text;not null;default:''"`
}

func (AlbumImportFile) TableName() string {
	return "music_album_import_files"
}

type AlbumImportJob struct {
	Base
	ImportID    uuid.UUID  `json:"import_id" gorm:"type:uuid;not null;uniqueIndex:idx_music_album_import_jobs_import"`
	Status      string     `json:"status" gorm:"not null;default:'queued'"`
	Stage       string     `json:"stage" gorm:"not null;default:'queued'"`
	Attempts    int        `json:"attempts" gorm:"not null;default:0"`
	MaxAttempts int        `json:"max_attempts" gorm:"not null;default:3"`
	LockedBy    string     `json:"locked_by"`
	LockedAt    *time.Time `json:"locked_at"`
	HeartbeatAt *time.Time `json:"heartbeat_at"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	LastError   string     `json:"last_error" gorm:"type:text;not null;default:''"`
}

func (AlbumImportJob) TableName() string {
	return "music_album_import_jobs"
}
