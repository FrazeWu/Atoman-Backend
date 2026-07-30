package handlers

import (
	"fmt"
	"math"
	"strings"

	"atoman/internal/modules/recommendation"
	"atoman/internal/platform/apperr"
)

type recommendationItemDTO struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	ContentType string `json:"content_type"`
	ImageURL    string `json:"image_url"`
	TargetPath  string `json:"target_path"`
	ScoreLabel  string `json:"score_label"`
}

func parseRecommendationMode(raw string) (recommendation.Mode, error) {
	switch recommendation.Mode(strings.TrimSpace(strings.ToLower(raw))) {
	case recommendation.ModeHot:
		return recommendation.ModeHot, nil
	case recommendation.ModeFeatured:
		return recommendation.ModeFeatured, nil
	case recommendation.ModeDiscover:
		return recommendation.ModeDiscover, nil
	default:
		return "", apperr.BadRequest("validation.invalid_request", "mode must be one of hot, featured, discover")
	}
}

func clampRecommendation(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func recommendationScoreLabel(mode recommendation.Mode, score float64) string {
	prefix := "推荐"
	switch mode {
	case recommendation.ModeHot:
		prefix = "热度"
	case recommendation.ModeFeatured:
		prefix = "精选"
	case recommendation.ModeDiscover:
		prefix = "探索"
	}
	return fmt.Sprintf("%s %.0f", prefix, math.Round(score*100))
}
