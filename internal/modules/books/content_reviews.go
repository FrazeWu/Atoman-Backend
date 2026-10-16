package books

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type LinkBookContentReviewInput struct {
	Spoiler bool `json:"spoiler"`
}

type BookContentReviewDTO struct {
	ContentID    uuid.UUID  `json:"content_id"`
	WorkID       uuid.UUID  `json:"work_id"`
	AuthorID     uuid.UUID  `json:"author_id"`
	Title        string     `json:"title"`
	Summary      string     `json:"summary"`
	Spoiler      bool       `json:"spoiler"`
	PublishedAt  *time.Time `json:"published_at,omitempty"`
	TargetPath   string     `json:"target_path"`
	TargetKind   string     `json:"target_kind"`
	CommentsPath string     `json:"comments_path"`
}

type BookContentReviewList struct {
	Items  []BookContentReviewDTO `json:"items"`
	Total  int64                  `json:"total"`
	Limit  int                    `json:"limit"`
	Offset int                    `json:"offset"`
}

// Reuse the canonical blog create/edit/draft APIs. Linking a draft does not
// publish it, and withdrawing/deleting content immediately removes it from
// the public work list. There is no legacy Post or BookReview body authority.
func (s *Service) LinkBookContentReview(ctx context.Context, user authctx.CurrentUser, workID, contentID uuid.UUID, input LinkBookContentReviewInput) (model.BookContentReview, error) {
	if user.ID == uuid.Nil {
		return model.BookContentReview{}, apperr.Unauthorized("Login required")
	}
	if contentID == uuid.Nil {
		return model.BookContentReview{}, apperr.BadRequest("books.invalid_content_review", "A canonical blog content UUID is required")
	}
	var result model.BookContentReview
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		service := *s
		service.db = tx
		work, err := service.GetBookWork(ctx, workID)
		if err != nil {
			return err
		}
		// Work -> content -> relation is also the order used by book publication.
		var entry model.ContentEntry
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ? AND kind = 'blog' AND author_id = ?", contentID, user.ID).First(&entry).Error; err != nil {
			return bookContentReviewNotFound(err)
		}
		var body model.ContentBlogExtension
		if err := tx.Select("content_id").First(&body, "content_id = ?", contentID).Error; err != nil {
			return bookContentReviewNotFound(err)
		}
		candidate := model.BookContentReview{ContentID: contentID, WorkID: workID, CreatedBy: user.ID, Spoiler: input.Spoiler}
		inserted := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
		if inserted.Error != nil {
			return inserted.Error
		}
		if inserted.RowsAffected == 1 {
			result = candidate
			return nil
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&result, "content_id = ?", contentID).Error; err != nil {
			return err
		}
		aliases, err := bookNoteWorkAliases(tx, uuid.MustParse(work.ID))
		if err != nil {
			return err
		}
		belongs := false
		for _, id := range aliases {
			belongs = belongs || id == result.WorkID
		}
		if !belongs || result.CreatedBy != user.ID {
			return apperr.Conflict("books.review_work_mismatch", "This review is already linked to another work")
		}
		if err := tx.Model(&result).Update("spoiler", input.Spoiler).Error; err != nil {
			return err
		}
		return tx.First(&result, "content_id = ?", contentID).Error
	})
	return result, err
}

func bookContentReviewNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.NotFound("books.content_review_not_found", "Owned canonical blog content or review not found")
	}
	return err
}

func (s *Service) UnlinkBookContentReview(ctx context.Context, user authctx.CurrentUser, workID, contentID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		service := *s
		service.db = tx
		work, err := service.GetBookWork(ctx, workID)
		if err != nil {
			return err
		}
		aliases, err := bookNoteWorkAliases(tx, uuid.MustParse(work.ID))
		if err != nil {
			return err
		}
		// The creator may remove an orphan relation after the original blog was
		// deleted. Removing the relation never deletes the blog or its comments.
		return tx.Where("content_id = ? AND created_by = ? AND work_id IN ?", contentID, user.ID, aliases).Delete(&model.BookContentReview{}).Error
	})
}

func (s *Service) ListBookContentReviews(ctx context.Context, workID uuid.UUID, limit, offset int) (BookContentReviewList, error) {
	limit, offset = normalizeCatalogPagination(limit, offset)
	result := BookContentReviewList{Items: []BookContentReviewDTO{}, Limit: limit, Offset: offset}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		service := *s
		service.db = tx
		work, err := service.GetBookWork(ctx, workID)
		if err != nil {
			return err
		}
		aliases, err := bookNoteWorkAliases(tx, uuid.MustParse(work.ID))
		if err != nil {
			return err
		}
		query := tx.Table("book_content_reviews AS reviews").
			Joins("JOIN content_entries c ON c.id = reviews.content_id AND c.deleted_at IS NULL AND c.kind = 'blog' AND c.status = 'published' AND c.visibility = 'public'").
			Joins("JOIN content_blog_extensions body ON body.content_id = c.id").
			Where("reviews.work_id IN ?", aliases)
		if err := query.Count(&result.Total).Error; err != nil {
			return err
		}
		if err := query.Select("reviews.content_id, reviews.work_id, c.author_id, c.title, c.summary, reviews.spoiler, c.published_at").
			Order("c.published_at DESC NULLS LAST, c.id ASC").Limit(limit).Offset(offset).Scan(&result.Items).Error; err != nil {
			return err
		}
		for i := range result.Items {
			item := &result.Items[i]
			item.TargetPath = "/post/" + item.ContentID.String()
			item.TargetKind = "blog_post"
			item.CommentsPath = "/api/v1/discussions/blog_post/" + item.ContentID.String() + "/comments"
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	return result, err
}
