package search

import "context"

// 注册仅发生在启动时。能力声明不得执行内容搜索；Search 必须把 ctx 传至领域查询。
// 每次 Search 只处理一个类型；筛选 OR/AND、权限、去重和稳定唯一排序在 SQL 中完成。
type SearchProvider interface {
	Module() string
	Capabilities(ctx context.Context, viewer Viewer, scope Scope) ModuleCapabilities
	Search(ctx context.Context, viewer Viewer, req ProviderSearchRequest) (ProviderSearchResult, error)
}
