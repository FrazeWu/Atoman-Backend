package service

import (
	"atoman/internal/platform/sitehandle"

	"gorm.io/gorm"
)

var (
	ErrSiteHandleInvalid  = sitehandle.ErrInvalid
	ErrSiteHandleReserved = sitehandle.ErrReserved
	ErrSiteHandleTaken    = sitehandle.ErrTaken
)

type SiteResolution = sitehandle.Resolution
type SiteNamespaceService = sitehandle.Service

func NewSiteNamespaceService(db *gorm.DB) *SiteNamespaceService {
	return sitehandle.NewService(db)
}
