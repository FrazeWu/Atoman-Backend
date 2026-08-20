package handlers

import (
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"atoman/internal/middleware"
)

func SetupAdminRoutes(router *gin.Engine, db *gorm.DB, s3Client *s3.S3) {
	router.GET("/api/v1/site/access", GetPublicSiteAccessHandler(db))
	router.GET("/api/v1/settings/public/site-access", GetPublicSiteAccessHandler(db))

	settings := router.Group("/api/v1/settings")
	settings.Use(middleware.AuthMiddleware())
	settings.Use(middleware.AdminMiddleware(db))
	{
		settings.GET("/site-access", GetSiteAccessHandler(db))
		settings.PUT("/site-access", UpdateSiteAccessHandler(db))
	}

	admin := router.Group("/api/v1/admin")
	admin.Use(middleware.AuthMiddleware())
	admin.Use(middleware.AdminMiddleware(db))
	{
		admin.PUT("/site-access", UpdateLegacySiteAccessHandler(db))

		feedFullText := admin.Group("/feed/fulltext")
		{
			feedFullText.GET("/settings", GetAdminFeedFullTextSettings(db))
			feedFullText.PUT("/settings", UpdateAdminFeedFullTextSettings(db))
			feedFullText.GET("/health", GetAdminFeedFullTextHealth(db))
			feedFullText.POST("/crawl", RunAdminFeedFullTextCrawl(db))
			feedFullText.GET("/sources", GetAdminFeedFullTextSources(db))
			feedFullText.POST("/sources", CreateAdminFeedSource(db))
			feedFullText.PUT("/sources/:source_id", UpdateAdminFeedSource(db))
			feedFullText.POST("/sources/:source_id/sync", SyncAdminFeedSource(db))
			feedFullText.GET("/items", GetAdminFeedFullTextItems(db))
			feedFullText.PUT("/sources/:source_id/settings", UpdateAdminFeedFullTextSourceSettings(db))
			feedFullText.POST("/items/:item_id/retry", RetryAdminFeedFullTextItem(db))
		}

		admin.GET("/feed/sources", AdminListFeedSources(db))
		admin.GET("/feed/sources/:id/impact", GetAdminFeedSourceImpact(db))
		admin.GET("/feed/sources/:id/diagnostics", GetAdminFeedSourceDiagnostics(db))
		admin.PATCH("/feed/sources/:id", AdminUpdateFeedSourceRow(db))
		admin.DELETE("/feed/sources/:id", AdminDeleteFeedSourceRow(db))
		admin.GET("/feed/onboarding/recommendations", ListAdminOnboardingFeedRecommendations(db))
		admin.POST("/feed/onboarding/recommendations", CreateAdminOnboardingFeedRecommendation(db))
		admin.PATCH("/feed/onboarding/recommendations/:id", UpdateAdminOnboardingFeedRecommendation(db))
		admin.DELETE("/feed/onboarding/recommendations/:id", DeleteAdminOnboardingFeedRecommendation(db))

	}
}
