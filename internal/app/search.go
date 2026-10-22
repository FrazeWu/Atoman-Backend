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
	search.RegisterRoutes(group, search.NewService(registry))
}
