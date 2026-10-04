package model

import (
	"time"

	"github.com/google/uuid"
)

type BlogPostReport struct {
	Base
	ContentID  uuid.UUID  `json:"content_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_blog_post_report_user,priority:1"`
	ReporterID uuid.UUID  `json:"reporter_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_blog_post_report_user,priority:2"`
	Reason     string     `json:"reason" gorm:"type:varchar(32);not null"`
	Note       string     `json:"note" gorm:"type:text"`
	Status     string     `json:"status" gorm:"type:varchar(16);not null;default:'pending';index"`
	ReviewerID *uuid.UUID `json:"reviewer_id,omitempty" gorm:"type:uuid;index"`
	ReviewedAt *time.Time `json:"reviewed_at,omitempty"`
}

func (BlogPostReport) TableName() string { return "blog_post_reports" }

type BlogPostAppeal struct {
	Base
	ContentID  uuid.UUID  `json:"content_id" gorm:"type:uuid;not null;uniqueIndex:idx_blog_post_appeal_author"`
	AuthorID   uuid.UUID  `json:"author_id" gorm:"type:uuid;not null;index"`
	Reason     string     `json:"reason" gorm:"type:text;not null"`
	Status     string     `json:"status" gorm:"type:varchar(16);not null;default:'pending';index"`
	ReviewerID *uuid.UUID `json:"reviewer_id,omitempty" gorm:"type:uuid;index"`
	ReviewedAt *time.Time `json:"reviewed_at,omitempty"`
}

func (BlogPostAppeal) TableName() string { return "blog_post_appeals" }
