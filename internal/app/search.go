package app

import (
	"context"

	"atoman/internal/modules/blog"
	"atoman/internal/modules/books"
	"atoman/internal/modules/forum"
	"atoman/internal/modules/music"
	"atoman/internal/modules/search"
	"atoman/internal/modules/search/adapters"
	"atoman/internal/modules/shortnote"
	"atoman/internal/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func registerSearchRoutes(group *gin.RouterGroup, db *gorm.DB) {
	registry := search.NewRegistry(func(ctx context.Context) (map[string]bool, error) {
		matrix, err := service.NewSiteAccessService(db.WithContext(ctx)).Load()
		if err != nil {
			return nil, err
		}
		modules := make(map[string]bool, len(matrix.Modules))
		for key, module := range matrix.Modules {
			modules[key] = module.Enabled != nil && *module.Enabled
		}
		return modules, nil
	})
	if err := registry.Register(adapters.NewBooksProvider(books.NewService(db))); err != nil {
		panic(err) // 固定的启动装配错误，不能静默遗漏已实现的 Provider。
	}
	if err := registry.Register(adapters.NewMusicProvider(music.NewService(db))); err != nil {
		panic(err)
	}
	if err := registry.Register(adapters.NewBlogProvider(blog.NewService(db), shortnote.NewService(db))); err != nil {
		panic(err)
	}
	if err := registry.Register(adapters.NewForumProvider(forum.NewRepo(db))); err != nil {
		panic(err)
	}
	publicProviders := []*adapters.PublicProvider{
		adapters.NewPublicProvider(db, "feed", "RSS", adapters.PublicType("source", "订阅源", "title", "url", "description"), adapters.PublicType("article", "文章", "title", "summary", "author")),
		adapters.NewPublicProvider(db, "video", "视频", adapters.PublicType("video", "视频", "title", "description")),
		adapters.NewPublicProvider(db, "podcast", "播客", adapters.PublicType("show", "节目", "name", "description"), adapters.PublicType("episode", "单集", "title", "content")),
		adapters.NewPublicProvider(db, "debate", "辩题", adapters.PublicType("debate", "辩题", "title", "description", "content")),
		adapters.NewPublicProvider(db, "timeline", "时间线", adapters.PublicType("person", "人物", "name", "bio"), adapters.PublicType("event", "事件", "title", "description", "content", "location")),
		adapters.NewPublicProvider(db, "users", "用户", adapters.PublicType("user", "用户", "username", "display_name", "bio")),
	}
	for _, provider := range publicProviders {
		if err := registry.Register(provider); err != nil {
			panic(err)
		}
	}
	search.RegisterRoutes(group, search.NewService(registry))
}
