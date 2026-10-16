package handlers

import (
	"errors"
	"time"

	"atoman/internal/model"
	ratingpolicy "atoman/internal/rating"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type contentRatingSummary struct {
	RatingScore  float64 `json:"rating_score"`
	RatingCount  int64   `json:"rating_count"`
	ViewerRating *int    `json:"viewer_rating"`
}

func contentRatingUpsertConflict(score int) clause.OnConflict {
	return clause.OnConflict{
		Columns:     []clause.Column{{Name: "user_id"}, {Name: "content_id"}},
		TargetWhere: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "deleted_at IS NULL"}}},
		DoUpdates:   clause.Assignments(map[string]any{"score": score, "updated_at": time.Now()}),
	}
}

func contentRatingSummaryForContent(db *gorm.DB, contentID uuid.UUID, viewerID *uuid.UUID) (contentRatingSummary, error) {
	if !db.Migrator().HasTable(&model.PostRating{}) {
		return contentRatingSummary{}, nil
	}
	var summary contentRatingSummary
	if err := db.Model(&model.PostRating{}).
		Select("COALESCE(AVG(score), 0) AS rating_score, COUNT(*) AS rating_count").
		Where("content_id = ?", contentID).Scan(&summary).Error; err != nil {
		return contentRatingSummary{}, err
	}
	summary.RatingScore = ratingpolicy.RoundAverage(summary.RatingScore)
	if viewerID != nil {
		var rating model.PostRating
		if err := db.Where("user_id = ? AND content_id = ?", *viewerID, contentID).First(&rating).Error; err == nil {
			summary.ViewerRating = &rating.Score
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return contentRatingSummary{}, err
		}
	}
	return summary, nil
}
