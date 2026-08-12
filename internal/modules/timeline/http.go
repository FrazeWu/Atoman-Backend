package timeline

import (
	"atoman/internal/handlers"
	"atoman/internal/middleware"
	"atoman/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func RegisterRoutes(router *gin.Engine, db *gorm.DB) {
	tl := router.Group("/api/v1/timeline")
	proposalService := service.NewTimelineRevisionProposalService(db)

	tl.GET("/events", handlers.GetTimelineEvents(db))
	tl.GET("/events/:id", handlers.GetTimelineEvent(db))
	tl.GET("/persons", handlers.GetTimelinePersons(db))
	tl.GET("/persons/:id", handlers.GetTimelinePerson(db))
	tl.GET("/persons/:id/locations", handlers.GetPersonLocations(db))
	tl.GET("/events/:id/revision-proposals", middleware.OptionalAuthMiddleware(), handlers.ListTimelineEventProposals(proposalService))
	tl.GET("/persons/:id/revision-proposals", middleware.OptionalAuthMiddleware(), handlers.ListTimelinePersonProposals(proposalService))

	protected := tl.Group("")
	protected.Use(middleware.AuthMiddleware())
	protected.POST("/events", handlers.CreateTimelineEvent(db))
	protected.POST("/events/:id/revision-proposals", handlers.CreateTimelineEventProposal(proposalService))
	protected.PUT("/events/:id", handlers.UpdateTimelineEvent(db))
	protected.DELETE("/events/:id", handlers.DeleteTimelineEvent(db))
	protected.GET("/events/:id/history", handlers.GetTimelineEventHistory(db))
	protected.POST("/events/:id/revert/:revision_id", handlers.RevertTimelineEvent(db))
	protected.POST("/persons", handlers.CreateTimelinePerson(db))
	protected.POST("/persons/:id/revision-proposals", handlers.CreateTimelinePersonProposal(proposalService))
	protected.PUT("/persons/:id", handlers.UpdateTimelinePerson(db))
	protected.DELETE("/persons/:id", handlers.DeleteTimelinePerson(db))
	protected.PUT("/revision-proposals/:comment_id/decision", handlers.DecideTimelineRevisionProposal(proposalService))
	protected.POST("/persons/:id/locations", handlers.AddPersonLocation(db))
	protected.PUT("/locations/:id", handlers.UpdatePersonLocation(db))
	protected.DELETE("/locations/:id", handlers.DeletePersonLocation(db))
}
