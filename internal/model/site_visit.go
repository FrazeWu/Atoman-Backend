package model

// SiteVisitDaily stores aggregated public page views for one UTC calendar day.
type SiteVisitDaily struct {
	Date      string `json:"date" gorm:"primaryKey;size:10"`
	ViewCount int64  `json:"view_count" gorm:"not null;default:0"`
}

func (SiteVisitDaily) TableName() string { return "site_visit_daily" }
