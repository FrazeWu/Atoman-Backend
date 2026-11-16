package model

import "time"

// ClientErrorDaily counts sanitized browser errors for one UTC day,
// release, route, kind, and fingerprint. It is the source for error counts
// by release version and route.
type ClientErrorDaily struct {
	ID              uint      `json:"id" gorm:"primaryKey"`
	Day             string    `json:"day" gorm:"size:10;not null;uniqueIndex:idx_client_error_daily,priority:1;index"`
	Release         string    `json:"release" gorm:"size:64;not null;uniqueIndex:idx_client_error_daily,priority:2;index"`
	Route           string    `json:"route" gorm:"size:256;not null;uniqueIndex:idx_client_error_daily,priority:3;index"`
	Kind            string    `json:"kind" gorm:"size:32;not null;uniqueIndex:idx_client_error_daily,priority:4"`
	Fingerprint     string    `json:"fingerprint" gorm:"size:64;not null;uniqueIndex:idx_client_error_daily,priority:5"`
	Count           int64     `json:"count" gorm:"not null;default:0"`
	SampleName      string    `json:"sample_name" gorm:"size:128;not null;default:''"`
	SampleMessage   string    `json:"sample_message" gorm:"size:500;not null;default:''"`
	SampleStack     string    `json:"sample_stack" gorm:"size:2000;not null;default:''"`
	SampleContext   string    `json:"sample_context" gorm:"size:200;not null;default:''"`
	SampleUserAgent string    `json:"sample_user_agent" gorm:"size:256;not null;default:''"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (ClientErrorDaily) TableName() string { return "client_error_daily" }
