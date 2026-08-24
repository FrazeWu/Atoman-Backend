package blog

import (
	"errors"
	"math"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) CountPostLikes(postID uuid.UUID) (int64, error) {
	if postID == uuid.Nil {
		return 0, apperr.BadRequest("validation.invalid_request", "post_id is required")
	}
	return s.repo.CountPostLikes(postID)
}

type PostRatingSummary struct {
	RatingScore  float64 `json:"rating_score"`
	RatingCount  int64   `json:"rating_count"`
	ViewerRating *int    `json:"viewer_rating,omitempty"`
}

func (s *Service) SetPostRating(user authctx.CurrentUser, postID uuid.UUID, score int) (PostRatingSummary, error) {
	if user.ID == uuid.Nil {
		return PostRatingSummary{}, apperr.Unauthorized("Login required")
	}
	if score < 1 || score > 10 {
		return PostRatingSummary{}, apperr.BadRequest("validation.invalid_request", "score must be between 1 and 10")
	}
	if err := s.ensurePostRatingAccess(user.ID, postID); err != nil {
		return PostRatingSummary{}, err
	}

	var rating model.PostRating
	err := s.db.Where("user_id = ? AND content_id = ?", user.ID, postID).First(&rating).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		rating = model.PostRating{UserID: user.ID, ContentID: postID, Score: score}
		if err := s.db.Create(&rating).Error; err != nil {
			return PostRatingSummary{}, err
		}
	case err != nil:
		return PostRatingSummary{}, err
	default:
		if err := s.db.Model(&rating).Update("score", score).Error; err != nil {
			return PostRatingSummary{}, err
		}
	}
	return s.PostRatingSummary(postID, &user.ID)
}

func (s *Service) DeletePostRating(user authctx.CurrentUser, postID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if err := s.ensurePostRatingAccess(user.ID, postID); err != nil {
		return err
	}
	return s.db.Where("user_id = ? AND content_id = ?", user.ID, postID).Delete(&model.PostRating{}).Error
}

func (s *Service) PostRatingSummary(postID uuid.UUID, viewerID *uuid.UUID) (PostRatingSummary, error) {
	if postID == uuid.Nil {
		return PostRatingSummary{}, apperr.BadRequest("validation.invalid_request", "post_id is required")
	}
	if !s.db.Migrator().HasTable(&model.PostRating{}) {
		return PostRatingSummary{}, nil
	}
	var aggregate struct {
		RatingScore float64 `gorm:"column:rating_score"`
		RatingCount int64   `gorm:"column:rating_count"`
	}
	if err := s.db.Model(&model.PostRating{}).
		Select("COALESCE(AVG(score), 0) AS rating_score, COUNT(*) AS rating_count").
		Where("content_id = ?", postID).
		Scan(&aggregate).Error; err != nil {
		return PostRatingSummary{}, err
	}
	summary := PostRatingSummary{
		RatingScore: math.Round(aggregate.RatingScore*10) / 10,
		RatingCount: aggregate.RatingCount,
	}
	if viewerID != nil {
		var rating model.PostRating
		if err := s.db.Where("user_id = ? AND content_id = ?", *viewerID, postID).First(&rating).Error; err == nil {
			score := rating.Score
			summary.ViewerRating = &score
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return PostRatingSummary{}, err
		}
	}
	return summary, nil
}

func (s *Service) ensurePostRatingAccess(userID, postID uuid.UUID) error {
	if postID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "post_id is required")
	}
	post, err := s.repo.GetPost(postID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("blog.post_not_found", "Post not found")
		}
		return err
	}
	if post.Status != "published" {
		if post.UserID != userID {
			return apperr.Forbidden("blog.post_forbidden", "You don't have permission to interact with this unpublished post")
		}
		return nil
	}
	allowed, err := CanViewPublishedPost(s.db, &userID, post)
	if err != nil {
		return err
	}
	if !allowed {
		return apperr.Forbidden("blog.post_forbidden", "You don't have permission to interact with this post")
	}
	return nil
}

func (s *Service) ListBookmarks(user authctx.CurrentUser, folderID *uuid.UUID, sort string) ([]model.Bookmark, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	return s.repo.ListBookmarks(user.ID, folderID, sort)
}

func (s *Service) ListBookmarkItems(user authctx.CurrentUser, folderID *uuid.UUID, sort string) ([]BookmarkListItemDTO, error) {
	bookmarks, err := s.ListBookmarks(user, folderID, sort)
	if err != nil {
		return nil, err
	}

	postIDs := make([]uuid.UUID, 0, len(bookmarks))
	seen := make(map[uuid.UUID]struct{}, len(bookmarks))
	for _, bookmark := range bookmarks {
		if bookmark.ContentID == uuid.Nil {
			continue
		}
		if _, exists := seen[bookmark.ContentID]; !exists {
			seen[bookmark.ContentID] = struct{}{}
			postIDs = append(postIDs, bookmark.ContentID)
		}
	}
	posts, err := loadCanonicalBlogPosts(s.db, canonicalBlogPostsQuery(s.db).Where("posts.id IN ?", postIDs))
	if err != nil {
		return nil, err
	}
	visibleContentIDs := make(map[uuid.UUID]struct{}, len(posts))
	for _, post := range posts {
		visible := post.Status != "published" && post.UserID == user.ID
		if post.Status == "published" {
			visible, err = CanViewPublishedPost(s.db, &user.ID, post)
			if err != nil {
				return nil, err
			}
		}
		if visible {
			visibleContentIDs[post.ID] = struct{}{}
		}
	}

	type engagementCount struct {
		ContentID     uuid.UUID `gorm:"column:content_id"`
		LikesCount    int64     `gorm:"column:likes_count"`
		CommentsCount int64     `gorm:"column:comments_count"`
	}
	countsByContentID := make(map[uuid.UUID]engagementCount, len(postIDs))
	if len(postIDs) > 0 {
		var counts []engagementCount
		if err := s.db.Table("content_entries AS content").Select(`content.id AS content_id,
			(SELECT COUNT(*) FROM likes WHERE likes.target_type = 'post' AND likes.target_id = content.id AND likes.deleted_at IS NULL) AS likes_count,
			COALESCE((SELECT targets.comment_count FROM discussion_targets AS targets WHERE targets.kind = 'blog_post' AND targets.resource_id = content.id AND targets.deleted_at IS NULL LIMIT 1), 0) AS comments_count`).
			Where("content.id IN ?", postIDs).
			Scan(&counts).Error; err != nil {
			return nil, err
		}
		for _, count := range counts {
			countsByContentID[count.ContentID] = count
		}
	}
	postDTOs, err := s.postDTOs(s.db, posts, &user.ID)
	if err != nil {
		return nil, err
	}
	postDTOByID := make(map[uuid.UUID]BlogContentDTO, len(postDTOs))
	for _, postDTO := range postDTOs {
		postDTOByID[postDTO.ID] = postDTO
	}

	items := make([]BookmarkListItemDTO, 0, len(bookmarks))
	for _, bookmark := range bookmarks {
		item := BookmarkListItemDTO{BlogBookmarkDTO: BlogBookmarkDTO{
			ID: bookmark.ID, CreatedAt: bookmark.CreatedAt, UpdatedAt: bookmark.UpdatedAt,
			UserID: bookmark.UserID, ContentID: bookmark.ContentID, BookmarkFolderID: bookmark.BookmarkFolderID,
		}}
		if _, visible := visibleContentIDs[bookmark.ContentID]; visible {
			count := countsByContentID[bookmark.ContentID]
			item.Content = &BookmarkBlogContentDTO{
				BlogContentDTO: postDTOByID[bookmark.ContentID],
				LikesCount:     count.LikesCount,
				CommentsCount:  count.CommentsCount,
			}
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Service) CreateBookmark(user authctx.CurrentUser, postID uuid.UUID, folderID *uuid.UUID) (model.Bookmark, error) {
	if user.ID == uuid.Nil {
		return model.Bookmark{}, apperr.Unauthorized("Login required")
	}
	if folderID == nil || *folderID == uuid.Nil {
		return model.Bookmark{}, apperr.BadRequest("validation.invalid_request", "bookmark_folder_id is required")
	}
	post, err := s.repo.GetPost(postID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Bookmark{}, apperr.NotFound("blog.post_not_found", "Post not found")
		}
		return model.Bookmark{}, err
	}
	if post.Status != "published" {
		if post.UserID != user.ID {
			return model.Bookmark{}, apperr.Forbidden("blog.post_forbidden", "You don't have permission to interact with this unpublished post")
		}
	} else {
		allowed, err := CanViewPublishedPost(s.db, &user.ID, post)
		if err != nil {
			return model.Bookmark{}, err
		}
		if !allowed {
			return model.Bookmark{}, apperr.Forbidden("blog.post_forbidden", "You don't have permission to interact with this post")
		}
	}
	var folder model.BookmarkFolder
	if err := s.db.Where("id = ? AND user_id = ?", *folderID, user.ID).First(&folder).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Bookmark{}, apperr.NotFound("blog.bookmark_folder_not_found", "Bookmark folder not found")
		}
		return model.Bookmark{}, err
	}
	var bookmark model.Bookmark
	err = s.db.Where("user_id = ? AND content_id = ?", user.ID, postID).First(&bookmark).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		bookmark = model.Bookmark{UserID: user.ID, ContentID: postID, BookmarkFolderID: folderID}
		if err := s.db.Create(&bookmark).Error; err != nil {
			return model.Bookmark{}, err
		}
		return bookmark, nil
	}
	if err != nil {
		return model.Bookmark{}, err
	}
	if bookmark.BookmarkFolderID == nil || *bookmark.BookmarkFolderID != *folderID {
		if err := s.db.Model(&bookmark).Update("bookmark_folder_id", *folderID).Error; err != nil {
			return model.Bookmark{}, err
		}
		bookmark.BookmarkFolderID = folderID
	}
	return bookmark, nil
}

func (s *Service) DeleteBookmark(user authctx.CurrentUser, bookmarkID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.repo.DeleteBookmark(bookmarkID, user.ID)
}

func (s *Service) ListBookmarkFolders(user authctx.CurrentUser) ([]model.BookmarkFolder, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	return s.repo.ListBookmarkFolders(user.ID)
}

func (s *Service) CreateBookmarkFolder(user authctx.CurrentUser, name string) (model.BookmarkFolder, error) {
	if user.ID == uuid.Nil {
		return model.BookmarkFolder{}, apperr.Unauthorized("Login required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return model.BookmarkFolder{}, apperr.BadRequest("validation.invalid_request", "name is required")
	}
	folder := model.BookmarkFolder{UserID: user.ID, Name: name}
	if err := s.repo.CreateBookmarkFolder(&folder); err != nil {
		return model.BookmarkFolder{}, err
	}
	return folder, nil
}

func (s *Service) DeleteBookmarkFolder(user authctx.CurrentUser, folderID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.repo.DeleteBookmarkFolder(folderID, user.ID)
}

func (s *Service) ToggleLike(user authctx.CurrentUser, targetType string, targetID uuid.UUID, isLike bool) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	switch targetType {
	case "post":
		post, err := s.repo.GetPost(targetID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("blog.post_not_found", "Post not found")
			}
			return err
		}
		if post.Status != "published" {
			if post.UserID != user.ID {
				return apperr.Forbidden("blog.post_forbidden", "You don't have permission to interact with this unpublished post")
			}
		} else {
			allowed, err := CanViewPublishedPost(s.db, &user.ID, post)
			if err != nil {
				return err
			}
			if !allowed {
				return apperr.Forbidden("blog.post_forbidden", "You don't have permission to interact with this post")
			}
		}
	default:
		return apperr.BadRequest("validation.invalid_request", "target_type is invalid")
	}

	if isLike {
		like := model.Like{UserID: user.ID, TargetType: targetType, TargetID: targetID}
		return s.db.Where(model.Like{UserID: user.ID, TargetType: targetType, TargetID: targetID}).FirstOrCreate(&like).Error
	}
	return s.db.Where("user_id = ? AND target_type = ? AND target_id = ?", user.ID, targetType, targetID).Delete(&model.Like{}).Error
}
