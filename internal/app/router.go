package app

import (
	"atoman/internal/modules/blog"
	"atoman/internal/modules/debate"
	"atoman/internal/modules/debate_voting"
	"atoman/internal/modules/feed"
	"atoman/internal/modules/forum"
	"atoman/internal/modules/forum_engagement"
	"atoman/internal/modules/forum_moderation"
	"atoman/internal/modules/music"
	"atoman/internal/modules/notification"
	"atoman/internal/modules/subscription"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func RegisterV1Routes(r *gin.Engine, db *gorm.DB) {
	group := r.Group("/api/v1")
	blog.RegisterRoutes(group.Group("/blog"), blog.NewService(db))
	subscription.RegisterRoutes(group, subscription.NewService(db))
	feed.RegisterRoutes(group.Group("/feed"), feed.NewService(db))
	notification.RegisterRoutes(group, notification.NewService(db))
	forum.RegisterRoutes(group.Group("/forum"), forum.NewService(db))
	forum_engagement.RegisterRoutes(group.Group("/forum"), forum_engagement.NewService(db))
	forum_moderation.RegisterRoutes(group.Group("/forum/moderation"), forum_moderation.NewService(db))
	debate.RegisterRoutes(group, debate.NewService(db))
	debate_voting.RegisterRoutes(group, debate_voting.NewService(db))
	music.RegisterRoutes(group.Group("/music"), music.NewService(db))
}
