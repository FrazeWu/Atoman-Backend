package feed

import "atoman/internal/modules/recommendation"

func (s *Service) ListRecommendationThemes(category string) []RecommendationThemeDTO {
	return listRecommendationThemes(category)
}

func (s *Service) RecommendArticlesByMode(mode recommendation.Mode, category string, theme string, page int, pageSize int) ([]RecommendationItemDTO, int64, error) {
	return s.RecommendArticles(mode, category, theme, page, pageSize)
}

func (s *Service) RecommendChannelsByMode(mode recommendation.Mode, category string, theme string, page int, pageSize int) ([]RecommendationItemDTO, int64, error) {
	return s.RecommendChannels(mode, category, theme, page, pageSize)
}
