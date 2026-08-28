package books

import (
	"context"
	"errors"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) ListRelatedBookPosts(ctx context.Context, workID uuid.UUID, limit, offset int) ([]BookPublicPostDTO, int64, error) {
	if _, err := s.GetPublicWork(ctx, workID); err != nil {
		return nil, 0, err
	}
	limit, offset = normalizeCatalogPagination(limit, offset)
	query := s.db.WithContext(ctx).Table("book_post_links AS links").Joins("JOIN posts ON posts.id = links.post_id AND posts.status = ? AND posts.visibility = ?", "published", "public").Where("links.work_id = ? AND links.deleted_at IS NULL", workID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []struct {
		ID          uuid.UUID  `gorm:"column:id"`
		Title       string     `gorm:"column:title"`
		Summary     string     `gorm:"column:summary"`
		PublishedAt *time.Time `gorm:"column:published_at"`
	}
	if err := query.Select("posts.id, posts.title, posts.summary, posts.published_at").Order("posts.published_at DESC NULLS LAST").Offset(offset).Limit(limit).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	items := make([]BookPublicPostDTO, 0, len(rows))
	for _, row := range rows {
		items = append(items, BookPublicPostDTO{ID: row.ID.String(), Title: row.Title, Summary: row.Summary, PublishedAt: row.PublishedAt})
	}
	return items, total, nil
}

func (s *Service) LinkBookPost(user authctx.CurrentUser, workID, postID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if _, err := s.GetPublicWork(context.Background(), workID); err != nil {
		return err
	}
	var post model.Post
	if err := s.db.Where("id = ? AND user_id = ? AND status = ? AND visibility = ?", postID, user.ID, "published", "public").First(&post).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("books.post_not_found", "Published blog post not found")
		}
		return err
	}
	var link model.BookPostLink
	err := s.db.Where("work_id = ? AND post_id = ?", workID, postID).First(&link).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return s.db.Create(&model.BookPostLink{WorkID: workID, PostID: postID}).Error
}

func (s *Service) UnlinkBookPost(user authctx.CurrentUser, workID, postID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	result := s.db.Unscoped().Where("work_id = ? AND post_id = ? AND EXISTS (SELECT 1 FROM posts WHERE posts.id = book_post_links.post_id AND posts.user_id = ?)", workID, postID, user.ID).Delete(&model.BookPostLink{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return apperr.NotFound("books.post_link_not_found", "Book post link not found")
	}
	return nil
}
