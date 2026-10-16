package books

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/reputation"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	ratingpolicy "atoman/internal/rating"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Use the existing reputation system's validity threshold and published weights.
// Creating a wiki entry does not make its editor the book's author: editors may rate it.
const bookMinimumRatingCount = reputation.BlogMinimumRatingCount

type BookWorkRatingDTO struct {
	WorkID              uuid.UUID `json:"work_id"`
	RatingScore         float64   `json:"rating_score"`
	RatingCount         int64     `json:"rating_count"`
	WeightedRatingScore *float64  `json:"weighted_rating_score"`
	RatingActive        bool      `json:"rating_active"`
	MinimumRatingCount  int       `json:"minimum_rating_count"`
	ViewerRating        *int      `json:"viewer_rating,omitempty"`
}

type BookRatingRankingItem struct {
	BookWorkRatingDTO
	Title string `json:"title"`
}

type BookRatingRankingResult struct {
	Items  []BookRatingRankingItem `json:"items"`
	Total  int64                   `json:"total"`
	Limit  int                     `json:"limit"`
	Offset int                     `json:"offset"`
}

// Aggregate in SQL before paginating. An inactive/deleted account's vote is
// retained privately but excluded from both the denominator and the ranking.
func bookWeightedRatingQuery(db *gorm.DB) *gorm.DB {
	votes := db.Table("book_ratings AS r").
		Joins(`JOIN "Users" AS u ON u.uuid = r.user_id AND u.is_active = true AND u.deleted_at IS NULL`).
		Where("r.deleted_at IS NULL AND r.score BETWEEN ? AND ?", ratingpolicy.MinimumScore, ratingpolicy.MaximumScore)
	weight := "CAST(? AS double precision)"
	args := []any{reputation.InitialQuality / 100}
	// This also allows isolated catalog deployments/tests before reputation is enabled.
	// Avoid querying absent tables inside a transaction: PostgreSQL would abort it.
	if db.Migrator().HasTable(&model.ReputationRun{}) && db.Migrator().HasTable(&model.UserReputationSnapshot{}) {
		latest := db.Model(&model.ReputationRun{}).Select("id").
			Where("status = ? AND published_at IS NOT NULL", model.ReputationRunPublished).
			Order("published_at DESC, id DESC").Limit(1)
		votes = votes.Joins("LEFT JOIN user_reputation_snapshots AS reputation ON reputation.user_id = r.user_id AND reputation.run_id = (?) AND reputation.deleted_at IS NULL", latest)
		weight = "LEAST(1.0, GREATEST(0.0, COALESCE(reputation.evaluation_weight, ?)))"
	}
	votes = votes.Select("r.work_id, r.score, "+weight+" AS evaluation_weight", args...)
	return db.Table("(?) AS votes", votes).Select(`votes.work_id,
AVG(votes.score) AS rating_score, COUNT(*) AS rating_count,
SUM(votes.score * votes.evaluation_weight) / NULLIF(SUM(votes.evaluation_weight), 0) AS weighted_rating_score`).
		Group("votes.work_id")
}

func finishBookWorkRating(dto *BookWorkRatingDTO) {
	dto.MinimumRatingCount = bookMinimumRatingCount
	dto.RatingActive = dto.RatingCount >= bookMinimumRatingCount && dto.WeightedRatingScore != nil
	dto.RatingScore = ratingpolicy.RoundAverage(dto.RatingScore)
	if dto.WeightedRatingScore != nil {
		score := ratingpolicy.RoundAverage(*dto.WeightedRatingScore)
		dto.WeightedRatingScore = &score
	}
}

func loadBookWorkRating(db *gorm.DB, workID uuid.UUID, viewerID *uuid.UUID) (BookWorkRatingDTO, error) {
	dto := BookWorkRatingDTO{WorkID: workID}
	if err := db.Table("(?) AS ratings", bookWeightedRatingQuery(db)).Where("ratings.work_id = ?", workID).Scan(&dto).Error; err != nil {
		return dto, err
	}
	finishBookWorkRating(&dto)
	if viewerID != nil && *viewerID != uuid.Nil {
		var vote model.BookRating
		err := db.Where("work_id = ? AND user_id = ?", workID, *viewerID).First(&vote).Error
		if err == nil {
			dto.ViewerRating = &vote.Score
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return dto, err
		}
	}
	return dto, nil
}

func (s *Service) GetBookWorkRating(ctx context.Context, workID uuid.UUID, viewerID *uuid.UUID) (BookWorkRatingDTO, error) {
	var result BookWorkRatingDTO
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		service := *s
		service.db = tx
		// Resolves merge aliases and holds the work lock until the aggregate is read.
		work, err := service.GetBookWork(ctx, workID)
		if err != nil {
			return err
		}
		result, err = loadBookWorkRating(tx, uuid.MustParse(work.ID), viewerID)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	return result, err
}

// PUT is a current-value replacement, not an additive vote. The existing
// (user_id, work_id) unique index makes concurrent writes and retries one vote.
func (s *Service) SetBookWorkRating(ctx context.Context, user authctx.CurrentUser, workID uuid.UUID, score int) (BookWorkRatingDTO, error) {
	if !ratingpolicy.ValidScore(score) {
		return BookWorkRatingDTO{}, apperr.BadRequest("validation.invalid_request", ratingpolicy.ScoreRangeMessage)
	}
	return s.writeBookWorkRating(ctx, user, workID, &score)
}

func (s *Service) DeleteBookWorkRating(ctx context.Context, user authctx.CurrentUser, workID uuid.UUID) (BookWorkRatingDTO, error) {
	return s.writeBookWorkRating(ctx, user, workID, nil)
}

func (s *Service) writeBookWorkRating(ctx context.Context, user authctx.CurrentUser, workID uuid.UUID, score *int) (BookWorkRatingDTO, error) {
	if user.ID == uuid.Nil {
		return BookWorkRatingDTO{}, apperr.Unauthorized("Login required")
	}
	var result BookWorkRatingDTO
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		service := *s
		service.db = tx
		work, err := service.GetBookWork(ctx, workID)
		if err != nil {
			return err
		}
		var author model.User
		if err := tx.Select("uuid").Where("uuid = ? AND is_active = true", user.ID).First(&author).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.Forbidden("books.rating_account_inactive", "An active account is required")
			}
			return err
		}
		canonicalID := uuid.MustParse(work.ID)
		if score == nil {
			if err := tx.Where("user_id = ? AND work_id = ?", user.ID, canonicalID).Delete(&model.BookRating{}).Error; err != nil {
				return err
			}
		} else {
			vote := model.BookRating{UserID: user.ID, WorkID: canonicalID, Score: *score}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "user_id"}, {Name: "work_id"}},
				DoUpdates: clause.Assignments(map[string]any{"score": *score, "updated_at": time.Now().UTC(), "deleted_at": nil}),
			}).Create(&vote).Error; err != nil {
				return err
			}
		}
		result, err = loadBookWorkRating(tx, canonicalID, &user.ID)
		return err
	})
	return result, err
}

func (s *Service) ListHighRatedBookWorks(ctx context.Context, limit, offset int) (BookRatingRankingResult, error) {
	limit, offset = normalizeCatalogPagination(limit, offset)
	result := BookRatingRankingResult{Items: []BookRatingRankingItem{}, Limit: limit, Offset: offset}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Table("(?) AS ratings", bookWeightedRatingQuery(tx)).
			Joins("JOIN book_works AS w ON w.id = ratings.work_id").
			Where("w.deleted_at IS NULL AND w.lifecycle_status = ? AND w.edit_status <> ?", model.BookLifecycleStatusActive, model.BookEditStatusClosed).
			Where("EXISTS (SELECT 1 FROM revisions WHERE content_type = 'book_work' AND content_id = w.id AND is_current = true AND deleted_at IS NULL)").
			Where("ratings.rating_count >= ? AND ratings.weighted_rating_score IS NOT NULL", bookMinimumRatingCount)
		if err := query.Count(&result.Total).Error; err != nil {
			return err
		}
		// Rank by unrounded scores; rounded display values must not change order.
		if err := query.Select("ratings.*, w.title").Order("ratings.weighted_rating_score DESC, ratings.rating_count DESC, ratings.work_id ASC").
			Limit(limit).Offset(offset).Scan(&result.Items).Error; err != nil {
			return err
		}
		for i := range result.Items {
			finishBookWorkRating(&result.Items[i].BookWorkRatingDTO)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return result, err
}
