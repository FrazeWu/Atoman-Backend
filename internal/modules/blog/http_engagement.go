package blog

import (
	"errors"
	"net/http"
	"strings"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (h *Handler) getPostLikesCount(c *gin.Context) {
	postID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	post, err := h.service.repo.GetPost(postID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("blog.post_not_found", "Post not found"))
			return
		}
		httpx.Error(c, err)
		return
	}
	viewerID := currentViewerID(c)
	if post.Status != "published" {
		if viewerID == nil || post.UserID != *viewerID {
			httpx.Error(c, apperr.Forbidden("blog.post_forbidden", "You don't have permission to view this post"))
			return
		}
	} else {
		allowed, err := CanViewPublishedPost(h.service.db, viewerID, post)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		if !allowed {
			httpx.Error(c, apperr.Forbidden("blog.post_forbidden", "You don't have permission to view this post"))
			return
		}
	}
	count, err := h.service.CountPostLikes(postID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"count": count})
}

func (h *Handler) createLike(c *gin.Context) {
	h.toggleLike(c, true)
}

func (h *Handler) deleteLike(c *gin.Context) {
	h.toggleLike(c, false)
}

func (h *Handler) toggleLike(c *gin.Context, isLike bool) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req struct {
		TargetType string `json:"target_type"`
		TargetID   string `json:"target_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	targetID, err := uuid.Parse(req.TargetID)
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "target_id must be a valid uuid"))
		return
	}
	if err := h.service.ToggleLike(user, req.TargetType, targetID, isLike); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "ok"})
}

// listBookmarks godoc
// @Summary 获取文章收藏
// @Tags blog
// @Produce json
// @Param folder_id query string false "收藏夹 UUID"
// @Param sort query string false "排序" Enums(latest,popular)
// @Success 200 {object} map[string][]BookmarkListItemDTO
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/bookmarks [get]
func (h *Handler) listBookmarks(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var folderID *uuid.UUID
	if raw := strings.TrimSpace(c.Query("folder_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "folder_id must be a valid uuid"))
			return
		}
		folderID = &parsed
	}
	sort := strings.TrimSpace(c.DefaultQuery("sort", "latest"))
	bookmarks, err := h.service.ListBookmarkItems(user, folderID, sort)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, bookmarks)
}

// createBookmark godoc
// @Summary 收藏文章到指定收藏夹
// @Tags blog
// @Accept json
// @Produce json
// @Param input body bookmarkInput true "收藏输入"
// @Success 201 {object} model.Bookmark
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/bookmarks [post]
func (h *Handler) createBookmark(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req bookmarkInput
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	bookmark, err := h.service.CreateBookmark(user, req.PostID, req.BookmarkFolderID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, bookmark)
}

// deleteBookmark godoc
// @Summary 取消文章收藏
// @Tags blog
// @Param id path string true "收藏 UUID"
// @Success 200 {object} handlers.MessageResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/bookmarks/{id} [delete]
func (h *Handler) deleteBookmark(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	bookmarkID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	if err := h.service.DeleteBookmark(user, bookmarkID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "ok"})
}

// listBookmarkFolders godoc
// @Summary 获取收藏夹
// @Tags blog
// @Success 200 {array} model.BookmarkFolder
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/bookmark-folders [get]
func (h *Handler) listBookmarkFolders(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	folders, err := h.service.ListBookmarkFolders(user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, folders)
}

// createBookmarkFolder godoc
// @Summary 新建收藏夹
// @Tags blog
// @Accept json
// @Param input body bookmarkFolderInput true "收藏夹输入"
// @Success 201 {object} model.BookmarkFolder
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/bookmark-folders [post]
func (h *Handler) createBookmarkFolder(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req bookmarkFolderInput
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	folder, err := h.service.CreateBookmarkFolder(user, req.Name)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, folder)
}

// deleteBookmarkFolder godoc
// @Summary 删除收藏夹
// @Description 收藏会迁移到默认收藏夹。
// @Tags blog
// @Param id path string true "收藏夹 UUID"
// @Success 200 {object} handlers.MessageResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/bookmark-folders/{id} [delete]
func (h *Handler) deleteBookmarkFolder(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	folderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	if err := h.service.DeleteBookmarkFolder(user, folderID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "ok"})
}
