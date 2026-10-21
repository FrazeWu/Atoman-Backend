package app

import (
	"context"

	"atoman/internal/modules/search"
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
	// P1 仅装配核心。P2 起在此注册真实领域 Provider，不使用占位结果开放能力。
	search.RegisterRoutes(group, search.NewService(registry))
}
