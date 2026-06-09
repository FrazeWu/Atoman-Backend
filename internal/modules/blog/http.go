package blog

import (
	"errors"
	"io"
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Handler struct {
	service *Service
}

type setRatingRequest struct {
	Score int `json:"score"`
}

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service}
	group.POST("/posts", h.createPost)
	group.PUT("/posts/:postId/rating", h.setRating)
}

func (h *Handler) createPost(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	var req CreatePostRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}

	post, err := h.service.CreatePost(user, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, post)
}

func (h *Handler) setRating(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	postID, err := parsePostID(c.Param("postId"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var req setRatingRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}

	summary, err := h.service.SetRating(user, postID, req.Score)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, summary)
}

func parsePostID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, apperr.BadRequest("validation.invalid_request", "postId must be a valid UUID")
	}
	return id, nil
}

func bindJSON(c *gin.Context, dst any) error {
	if err := c.ShouldBindJSON(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return apperr.BadRequest("validation.invalid_request", "request body must not be empty")
		}
		return apperr.BadRequest("validation.invalid_request", "request body must be valid JSON")
	}
	return nil
}
