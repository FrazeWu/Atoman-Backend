package adapters

import (
	"context"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/search"
	"atoman/internal/testdb"
	"github.com/stretchr/testify/require"
)

func TestUserSearchAccessRankingAndPrivacy(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.UserSettings{})
	names := []string{"cols", "colslwys", "display-exact", "display-prefix", "contains-cols", "bio-only", "private-cols", "inactive-cols", "deleted-cols", "literal_name", "literalXname", "percent%name", "percentXname", `slash\name`, "slashXname"}
	for index, name := range names {
		user := model.User{Username: name, Email: name + "@example.test", Password: "hash", IsActive: true, CreatedAt: time.Now().Add(time.Duration(index) * time.Hour)}
		switch name {
		case "cols":
			user.DisplayName = "完全不同的显示名"
		case "display-exact":
			user.DisplayName = "cols"
		case "display-prefix":
			user.DisplayName = "Cols Display"
		case "bio-only":
			user.Bio = "cols"
		}
		require.NoError(t, db.Create(&user).Error)
		switch name {
		case "private-cols":
			require.NoError(t, db.Create(&model.UserSettings{UserID: user.UUID, PrivateProfile: true}).Error)
		case "inactive-cols":
			require.NoError(t, db.Model(&user).Update("is_active", false).Error)
		case "deleted-cols":
			require.NoError(t, db.Delete(&user).Error)
		}
	}
	provider := NewPublicProvider(db, "users", "用户", PublicType("user", "用户", "username", "display_name", "bio"))
	enabled := true
	registry := search.NewRegistry(func(context.Context) (map[string]bool, error) { return map[string]bool{"blog": enabled}, nil })
	require.NoError(t, registry.Register(provider))
	service := search.NewService(registry)
	req := search.SearchRequest{Query: "COLS", Module: "users", Types: []string{"user"}, Scope: search.ScopePublic, View: search.ViewList, PageSize: 2, Page: 1}
	result, err := service.Search(context.Background(), search.Viewer{}, req)
	require.NoError(t, err)
	require.EqualValues(t, 6, result.Pagination.Total)
	require.Equal(t, []string{"/users/cols", "/users/colslwys"}, []string{result.Items[0].Href, result.Items[1].Href})
	req.Page = 2
	result, err = service.Search(context.Background(), search.Viewer{}, req)
	require.NoError(t, err)
	require.Equal(t, []string{"/users/display-exact", "/users/display-prefix"}, []string{result.Items[0].Href, result.Items[1].Href})
	req.View = search.ViewPreview
	req.Page, req.PageSize = 0, 0
	preview, err := service.Search(context.Background(), search.Viewer{}, req)
	require.NoError(t, err)
	require.Equal(t, "/users/cols", preview.Groups[0].Items[0].Href)
	for _, query := range []string{"literal_", "percent%", `slash\`} {
		rows, err := provider.Search(context.Background(), search.Viewer{}, search.ProviderSearchRequest{Scope: search.ScopePublic, Type: "user", Query: query, Sort: "relevance", Limit: 20, ExactTotal: true})
		require.NoError(t, err)
		require.Len(t, rows.Items, 1, query)
		require.EqualValues(t, 1, *rows.Total)
	}
	enabled = false
	_, err = service.Search(context.Background(), search.Viewer{}, req)
	require.ErrorContains(t, err, "site.module_disabled")
}
