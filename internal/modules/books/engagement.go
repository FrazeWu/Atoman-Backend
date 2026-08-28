package books

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const bookReviewMaxLength = 5000

type BookRatingSummary struct {
	RatingScore  float64 `json:"rating_score"`
	RatingCount  int64   `json:"rating_count"`
	ViewerRating *int    `json:"viewer_rating,omitempty"`
}

type BookReviewDTO struct {
	ID        string    `json:"id"`
	AuthorID  string    `json:"author_id"`
	WorkID    string    `json:"work_id"`
	Content   string    `json:"content"`
	Spoiler   bool      `json:"spoiler"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SaveBookReviewInput struct {
	Content    string `json:"content"`
	Spoiler    bool   `json:"spoiler"`
	Visibility string `json:"visibility"`
}

func (s *Service) SetBookRating(user authctx.CurrentUser, workID uuid.UUID, score int) (BookRatingSummary, error) {
	if user.ID == uuid.Nil {
		return BookRatingSummary{}, apperr.Unauthorized("Login required")
	}
	if score < 1 || score > 5 {
		return BookRatingSummary{}, apperr.BadRequest("validation.invalid_request", "score must be between 1 and 5")
	}
	if err := s.requirePublicWork(context.Background(), workID); err != nil {
		return BookRatingSummary{}, err
	}
	rating := model.BookRating{UserID: user.ID, WorkID: workID, Score: score}
	if err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "work_id"}},
		DoUpdates: clause.Assignments(map[string]any{"score": score, "updated_at": time.Now().UTC()}),
	}).Create(&rating).Error; err != nil {
		return BookRatingSummary{}, err
	}
	return s.BookRatingSummary(context.Background(), workID, &user.ID)
}

func (s *Service) BookRatingSummary(ctx context.Context, workID uuid.UUID, viewerID *uuid.UUID) (BookRatingSummary, error) {
	if err := s.requirePublicWork(ctx, workID); err != nil {
		return BookRatingSummary{}, err
	}
	var aggregate struct {
		RatingScore float64 `gorm:"column:rating_score"`
		RatingCount int64   `gorm:"column:rating_count"`
	}
	if err := s.db.WithContext(ctx).Model(&model.BookRating{}).
		Select("COALESCE(AVG(score), 0) AS rating_score, COUNT(*) AS rating_count").
		Where("work_id = ?", workID).Scan(&aggregate).Error; err != nil {
		return BookRatingSummary{}, err
	}
	summary := BookRatingSummary{RatingScore: math.Round(aggregate.RatingScore*10) / 10, RatingCount: aggregate.RatingCount}
	if viewerID != nil {
		var rating model.BookRating
		if err := s.db.WithContext(ctx).Where("user_id = ? AND work_id = ?", *viewerID, workID).First(&rating).Error; err == nil {
			score := rating.Score
			summary.ViewerRating = &score
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return BookRatingSummary{}, err
		}
	}
	return summary, nil
}

func (s *Service) SaveBookReview(user authctx.CurrentUser, workID uuid.UUID, input SaveBookReviewInput) (BookReviewDTO, error) {
	if user.ID == uuid.Nil {
		return BookReviewDTO{}, apperr.Unauthorized("Login required")
	}
	if err := validateBookReviewInput(input); err != nil {
		return BookReviewDTO{}, err
	}
	if err := s.requirePublicWork(context.Background(), workID); err != nil {
		return BookReviewDTO{}, err
	}
	var review model.BookReview
	err := s.db.Where("user_id = ? AND work_id = ?", user.ID, workID).First(&review).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		review = model.BookReview{UserID: user.ID, WorkID: workID}
	} else if err != nil {
		return BookReviewDTO{}, err
	}
	review.Content = strings.TrimSpace(input.Content)
	review.Spoiler = input.Spoiler
	review.Visibility = input.Visibility
	if review.ID == uuid.Nil {
		if err := s.db.Create(&review).Error; err != nil {
			return BookReviewDTO{}, err
		}
	} else if err := s.db.Model(&review).Select("content", "spoiler", "visibility", "updated_at").Updates(&review).Error; err != nil {
		return BookReviewDTO{}, err
	}
	return buildBookReviewDTO(review), nil
}

func (s *Service) ListPublicBookReviews(ctx context.Context, workID uuid.UUID, limit, offset int) ([]BookReviewDTO, int64, error) {
	if err := s.requirePublicWork(ctx, workID); err != nil {
		return nil, 0, err
	}
	limit, offset = normalizeCatalogPagination(limit, offset)
	query := s.db.WithContext(ctx).Model(&model.BookReview{}).Where("work_id = ? AND visibility = ?", workID, model.BookReviewVisibilityPublic)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var reviews []model.BookReview
	if err := query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&reviews).Error; err != nil {
		return nil, 0, err
	}
	result := make([]BookReviewDTO, 0, len(reviews))
	for _, review := range reviews {
		result = append(result, buildBookReviewDTO(review))
	}
	return result, total, nil
}

func (s *Service) requirePublicWork(ctx context.Context, workID uuid.UUID) error {
	if workID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "work_id is required")
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&model.BookWork{}).
		Where("id = ? AND lifecycle_status = ? AND edit_status <> ?", workID, model.BookLifecycleStatusActive, model.BookEditStatusClosed).
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return apperr.NotFound("books.catalog_not_found", "Public book not found")
	}
	return nil
}

func validateBookReviewInput(input SaveBookReviewInput) error {
	content := strings.TrimSpace(input.Content)
	if content == "" || len(content) > bookReviewMaxLength {
		return apperr.BadRequest("validation.invalid_request", "review content is invalid")
	}
	if input.Visibility != model.BookReviewVisibilityPublic && input.Visibility != model.BookReviewVisibilityPrivate {
		return apperr.BadRequest("validation.invalid_request", "review visibility is invalid")
	}
	return nil
}

func buildBookReviewDTO(review model.BookReview) BookReviewDTO {
	return BookReviewDTO{ID: review.ID.String(), AuthorID: review.UserID.String(), WorkID: review.WorkID.String(), Content: review.Content, Spoiler: review.Spoiler, CreatedAt: review.CreatedAt, UpdatedAt: review.UpdatedAt}
}
