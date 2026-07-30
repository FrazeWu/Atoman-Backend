package blog

import (
	"errors"
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
		if bookmark.Post == nil {
			continue
		}
		if _, exists := seen[bookmark.PostID]; exists {
			continue
		}
		seen[bookmark.PostID] = struct{}{}
		postIDs = append(postIDs, bookmark.PostID)
	}

	type engagementCount struct {
		PostID        uuid.UUID `gorm:"column:post_id"`
		LikesCount    int64     `gorm:"column:likes_count"`
		CommentsCount int64     `gorm:"column:comments_count"`
	}
	countsByPostID := make(map[uuid.UUID]engagementCount, len(postIDs))
	if len(postIDs) > 0 {
		var counts []engagementCount
		if err := s.db.Model(&model.Post{}).Select(`posts.id AS post_id,
			(SELECT COUNT(*) FROM likes WHERE likes.target_type = 'post' AND likes.target_id = posts.id AND likes.deleted_at IS NULL) AS likes_count,
			COALESCE((SELECT targets.comment_count FROM discussion_targets AS targets WHERE targets.kind = 'blog_post' AND targets.resource_id = posts.id AND targets.deleted_at IS NULL LIMIT 1), 0) AS comments_count`).
			Where("posts.id IN ?", postIDs).
			Scan(&counts).Error; err != nil {
			return nil, err
		}
		for _, count := range counts {
			countsByPostID[count.PostID] = count
		}
	}
	posts := make([]model.Post, 0, len(bookmarks))
	for _, bookmark := range bookmarks {
		if bookmark.Post != nil {
			posts = append(posts, *bookmark.Post)
		}
	}
	postDTOs, err := s.postDTOs(s.db, posts, &user.ID)
	if err != nil {
		return nil, err
	}
	postDTOByID := make(map[uuid.UUID]PostDTO, len(postDTOs))
	for _, postDTO := range postDTOs {
		postDTOByID[postDTO.ID] = postDTO
	}

	items := make([]BookmarkListItemDTO, 0, len(bookmarks))
	for _, bookmark := range bookmarks {
		item := BookmarkListItemDTO{Bookmark: bookmark}
		if bookmark.Post != nil {
			count := countsByPostID[bookmark.PostID]
			item.Bookmark.Post = nil
			item.Post = &BookmarkPostDTO{
				PostDTO:       postDTOByID[bookmark.PostID],
				LikesCount:    count.LikesCount,
				CommentsCount: count.CommentsCount,
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
	if post.Status == "draft" {
		if post.UserID != user.ID {
			return model.Bookmark{}, apperr.Forbidden("blog.post_forbidden", "You don't have permission to interact with this post")
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
	err = s.db.Where("user_id = ? AND post_id = ?", user.ID, postID).First(&bookmark).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		bookmark = model.Bookmark{UserID: user.ID, PostID: postID, BookmarkFolderID: folderID}
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
		if post.Status == "draft" {
			if post.UserID != user.ID {
				return apperr.Forbidden("blog.post_forbidden", "You don't have permission to interact with this post")
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
