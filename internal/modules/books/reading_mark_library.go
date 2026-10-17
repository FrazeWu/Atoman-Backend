package books

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"
	"context"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"net/http"
	"strings"
)

type BookPrivateMarkItem struct {
	BookReadingMarkDTO
	WorkID    uuid.UUID `json:"work_id"`
	AssetID   uuid.UUID `json:"asset_id"`
	WorkTitle string    `json:"work_title"`
}

func (s *Service) ListBookPrivateHighlights(ctx context.Context, user authctx.CurrentUser, query, color string, limit, offset int) ([]BookPrivateMarkItem, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	limit, offset = normalizeCatalogPagination(limit, offset)
	db := s.db.WithContext(ctx).Model(&model.BookReadingMark{}).
		Joins("JOIN book_asset_revisions r ON r.id = book_reading_marks.revision_id").
		Joins("JOIN book_assets a ON a.id = r.asset_id").Joins("JOIN book_works w ON w.id = a.work_id").
		Where("book_reading_marks.user_id = ? AND book_reading_marks.kind = 'highlight'", user.ID)
	if q := strings.TrimSpace(query); q != "" {
		pattern := "%" + escapeBookCatalogQuery(q) + "%"
		db = db.Where("(book_reading_marks.quote ILIKE ? OR book_reading_marks.note ILIKE ? OR w.title ILIKE ?)", pattern, pattern, pattern)
	}
	if color != "" {
		db = db.Where("book_reading_marks.color = ?", color)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []struct {
		model.BookReadingMark
		WorkID    uuid.UUID
		AssetID   uuid.UUID
		WorkTitle string
	}
	if err := db.Select("book_reading_marks.*, a.work_id, a.id AS asset_id, w.title AS work_title").Order("book_reading_marks.updated_at DESC, book_reading_marks.id ASC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	result := make([]BookPrivateMarkItem, 0, len(rows))
	for _, row := range rows {
		mark, err := bookReadingMarkDTO(row.BookReadingMark)
		if err != nil {
			return nil, 0, err
		}
		result = append(result, BookPrivateMarkItem{BookReadingMarkDTO: mark, WorkID: row.WorkID, AssetID: row.AssetID, WorkTitle: row.WorkTitle})
	}
	return result, total, nil
}

// listBookPrivateHighlights godoc
// @Summary 跨作品查询本人划线与批注，文件下架不删除私人数据
// @Tags books-notes
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param q query string false "原文、批注或书名搜索"
// @Param color query string false "高亮颜色"
// @Param limit query int false "分页大小"
// @Param offset query int false "分页偏移"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/books/me/reading-marks [get]
func (h *Handler) listBookPrivateHighlights(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	limit, offset, ok := catalogPaginationParams(c)
	if !ok {
		return
	}
	items, total, err := h.service.ListBookPrivateHighlights(c.Request.Context(), user, c.Query("q"), c.Query("color"), limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"items": items, "total": total})
}
