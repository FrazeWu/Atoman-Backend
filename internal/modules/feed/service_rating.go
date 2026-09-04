package feed

import (
	"errors"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	ratingpolicy "atoman/internal/rating"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type FeedItemRatingSummary struct {
	RatingScore  float64 `json:"rating_score"`
	RatingCount  int64   `json:"rating_count"`
	ViewerRating *int    `json:"viewer_rating,omitempty"`
}

func (s *Service) SetFeedItemRating(user authctx.CurrentUser, itemID uuid.UUID, score int) (FeedItemRatingSummary, error) {
	if user.ID == uuid.Nil {
		return FeedItemRatingSummary{}, apperr.Unauthorized("Login required")
	}
	if !ratingpolicy.ValidScore(score) {
		return FeedItemRatingSummary{}, apperr.BadRequest("validation.invalid_request", ratingpolicy.ScoreRangeMessage)
	}
	if err := s.ensureRateableFeedItem(itemID); err != nil {
		return FeedItemRatingSummary{}, err
	}
	rating := model.FeedItemRating{UserID: user.ID, FeedItemID: itemID, Score: score}
	if err := s.db.Clauses(feedItemRatingUpsertConflict(score)).Create(&rating).Error; err != nil {
		return FeedItemRatingSummary{}, err
	}
	return s.FeedItemRatingSummary(itemID, &user.ID)
}

func (s *Service) DeleteFeedItemRating(user authctx.CurrentUser, itemID uuid.UUID) (FeedItemRatingSummary, error) {
	if user.ID == uuid.Nil {
		return FeedItemRatingSummary{}, apperr.Unauthorized("Login required")
	}
	if err := s.ensureRateableFeedItem(itemID); err != nil {
		return FeedItemRatingSummary{}, err
	}
	if err := s.db.Where("user_id = ? AND feed_item_id = ?", user.ID, itemID).Delete(&model.FeedItemRating{}).Error; err != nil {
		return FeedItemRatingSummary{}, err
	}
	return s.FeedItemRatingSummary(itemID, &user.ID)
}

func (s *Service) FeedItemRatingSummary(itemID uuid.UUID, viewerID *uuid.UUID) (FeedItemRatingSummary, error) {
	var aggregate struct {
		RatingScore float64 `gorm:"column:rating_score"`
		RatingCount int64   `gorm:"column:rating_count"`
	}
	if err := s.db.Model(&model.FeedItemRating{}).
		Select("COALESCE(AVG(score), 0) AS rating_score, COUNT(*) AS rating_count").
		Where("feed_item_id = ?", itemID).
		Scan(&aggregate).Error; err != nil {
		return FeedItemRatingSummary{}, err
	}
	summary := FeedItemRatingSummary{
		RatingScore: ratingpolicy.RoundAverage(aggregate.RatingScore),
		RatingCount: aggregate.RatingCount,
	}
	if viewerID == nil {
		return summary, nil
	}
	var rating model.FeedItemRating
	if err := s.db.Where("user_id = ? AND feed_item_id = ?", *viewerID, itemID).First(&rating).Error; err == nil {
		score := rating.Score
		summary.ViewerRating = &score
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return FeedItemRatingSummary{}, err
	}
	return summary, nil
}

func (s *Service) ensureRateableFeedItem(itemID uuid.UUID) error {
	if itemID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "feed item id is required")
	}
	var count int64
	if err := s.db.Model(&model.FeedItem{}).
		Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
		Where("feed_items.id = ? AND feed_sources.hidden = ? AND feed_sources.deleted_at IS NULL", itemID, false).
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return apperr.NotFound("feed.feed_item_not_found", "Feed item not found")
	}
	return nil
}

func feedItemRatingUpsertConflict(score int) clause.OnConflict {
	return clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "feed_item_id"}},
		TargetWhere: clause.Where{Exprs: []clause.Expression{
			clause.Expr{SQL: "deleted_at IS NULL"},
		}},
		DoUpdates: clause.Assignments(map[string]any{
			"score":      score,
			"updated_at": time.Now(),
		}),
	}
}
