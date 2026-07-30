package handlers

import (
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	proposalservice "atoman/internal/service"
	timelinecore "atoman/internal/timeline"
)

// parseDateTime 尝试多种格式解析时间，支持精确到小时分钟，支持负年份（BCE）
// 负年份格式示例："-0500-01-01"（公元前500年）
func parseDateTime(s string) (time.Time, error) {
	return timelinecore.ParseDateTime(s)
}

func SetupTimelineRoutes(router *gin.Engine, db *gorm.DB) {
	tl := router.Group("/api/v1/timeline")
	proposalService := proposalservice.NewTimelineRevisionProposalService(db)
	{
		// Public routes
		tl.GET("/events", GetTimelineEvents(db))
		tl.GET("/events/:id", GetTimelineEvent(db))
		tl.GET("/persons", GetTimelinePersons(db))
		tl.GET("/persons/:id", GetTimelinePerson(db))
		tl.GET("/persons/:id/locations", GetPersonLocations(db))
		tl.GET("/events/:id/revision-proposals", middleware.OptionalAuthMiddleware(), ListTimelineEventProposals(proposalService))
		tl.GET("/persons/:id/revision-proposals", middleware.OptionalAuthMiddleware(), ListTimelinePersonProposals(proposalService))

		// Protected routes
		protected := tl.Group("")
		protected.Use(middleware.AuthMiddleware())
		{
			protected.POST("/events", CreateTimelineEvent(db))
			protected.POST("/events/:id/revision-proposals", CreateTimelineEventProposal(proposalService))
			protected.PUT("/events/:id", UpdateTimelineEvent(db))
			protected.DELETE("/events/:id", DeleteTimelineEvent(db))
			protected.GET("/events/:id/history", GetTimelineEventHistory(db))
			protected.POST("/events/:id/revert/:revision_id", RevertTimelineEvent(db))

			protected.POST("/persons", CreateTimelinePerson(db))
			protected.POST("/persons/:id/revision-proposals", CreateTimelinePersonProposal(proposalService))
			protected.PUT("/persons/:id", UpdateTimelinePerson(db))
			protected.DELETE("/persons/:id", DeleteTimelinePerson(db))
			protected.PUT("/revision-proposals/:comment_id/decision", DecideTimelineRevisionProposal(proposalService))

			protected.POST("/persons/:id/locations", AddPersonLocation(db))
			protected.PUT("/locations/:id", UpdatePersonLocation(db))
			protected.DELETE("/locations/:id", DeletePersonLocation(db))
		}
	}
}
