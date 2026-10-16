package books

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// getBookWorkRating godoc
// @Summary 获取作品有效评分和信誉加权评分
// @Description 仅统计活跃未删除账号。至少六名有效评分者且权重和大于零才进入正式高分榜；未达门槛仍展示参考分。合并别名解析到规范作品。本人评分仅登录时返回。
// @Tags books-ratings
// @Produce json
// @Param workId path string true "作品 UUID"
// @Success 200 {object} BookWorkRatingDTO
// @Router /api/v1/books/works/{workId}/rating [get]
func (h *Handler) getBookWorkRating(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	id, ok := parseCatalogID(c, "workId")
	if !ok {
		return
	}
	var viewerID *uuid.UUID
	if user, exists := authctx.Current(c); exists && user.ID != uuid.Nil {
		viewerID = &user.ID
	}
	result, err := h.service.GetBookWorkRating(c.Request.Context(), id, viewerID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// setBookWorkRating godoc
// @Summary 设置本人作品评分，1–10 分，重复提交不增加票数
// @Tags books-ratings
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param workId path string true "作品 UUID"
// @Param input body bookRatingInput true "评分"
// @Success 200 {object} BookWorkRatingDTO
// @Router /api/v1/books/works/{workId}/rating [put]
func (h *Handler) setBookWorkRating(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "workId")
	if !ok {
		return
	}
	var input bookRatingInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid book rating"))
		return
	}
	result, err := h.service.SetBookWorkRating(c.Request.Context(), user, id, input.Score)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// deleteBookWorkRating godoc
// @Summary 撤销本人作品评分
// @Tags books-ratings
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param workId path string true "作品 UUID"
// @Success 200 {object} BookWorkRatingDTO
// @Router /api/v1/books/works/{workId}/rating [delete]
func (h *Handler) deleteBookWorkRating(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "workId")
	if !ok {
		return
	}
	result, err := h.service.DeleteBookWorkRating(c.Request.Context(), user, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// listHighRatedBookWorks godoc
// @Summary 获取至少六名有效评分者的正式图书高分榜
// @Description 使用最新已发布信誉批次的评价权重，无快照账号使用初始权重 0.2。按未四舍五入加权分、人数、作品 UUID 排序。未达六人、零总权重、已关闭或已合并作品不入榜。
// @Tags books-ratings
// @Produce json
// @Param limit query int false "每页数量，最多 100"
// @Param offset query int false "偏移量"
// @Success 200 {object} BookRatingRankingResult
// @Router /api/v1/books/rankings/high-rated [get]
func (h *Handler) listHighRatedBookWorks(c *gin.Context) {
	limit, offset, ok := catalogPaginationParams(c)
	if !ok {
		return
	}
	result, err := h.service.ListHighRatedBookWorks(c.Request.Context(), limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}
