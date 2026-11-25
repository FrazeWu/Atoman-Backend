package papers

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type FileVersionSummary struct {
	Revision  int64     `json:"revision"`
	Path      string    `json:"path"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Service) ListFileVersions(user authctx.CurrentUser, projectID, fileID uuid.UUID, before int64) ([]FileVersionSummary, error) {
	file, err := s.GetFile(user, projectID, fileID)
	if err != nil {
		return nil, err
	}
	if file.Kind != "text" {
		return nil, apperr.BadRequest("papers.binary_read_only", "History is available for text files")
	}
	rows := make([]FileVersionSummary, 0)
	q := s.db.Model(&model.PaperFileVersion{}).Select("revision", "path", "sha256", "created_at").Where("file_id = ?", fileID)
	if before > 0 {
		q = q.Where("revision < ?", before)
	}
	err = q.Order("revision DESC").Limit(50).Scan(&rows).Error
	return rows, err
}

func (s *Service) GetFileVersion(user authctx.CurrentUser, projectID, fileID uuid.UUID, revision int64) (model.PaperFileVersion, error) {
	var result model.PaperFileVersion
	file, err := s.GetFile(user, projectID, fileID)
	if err != nil {
		return result, err
	}
	if file.Kind != "text" {
		return result, apperr.BadRequest("papers.binary_read_only", "History is available for text files")
	}
	err = s.db.Where("file_id = ? AND revision = ?", fileID, revision).First(&result).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return result, apperr.NotFound("papers.version_not_found", "Version not found")
	}
	return result, err
}

// @Summary 查询文本文件版本历史
// @Tags papers
// @Security BearerAuth
// @Produce json
// @Param id path string true "Project UUID"
// @Param fileId path string true "File UUID"
// @Param before query integer false "Exclusive revision cursor, 50 entries per page"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/files/{fileId}/versions [get]
func (h *handler) fileVersions(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	pid, ok := projectID(c)
	if !ok {
		return
	}
	fid, ok := httpx.UUIDParam(c, "fileId")
	if !ok {
		return
	}
	var before int64
	if raw := c.Query("before"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1 {
			httpx.Error(c, apperr.BadRequest("papers.invalid_revision", "Invalid revision cursor"))
			return
		}
		before = n
	}
	result, err := h.service.ListFileVersions(user, pid, fid, before)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// @Summary 读取指定历史源码版本
// @Tags papers
// @Security BearerAuth
// @Produce json
// @Param id path string true "Project UUID"
// @Param fileId path string true "File UUID"
// @Param revision path integer true "File revision"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/files/{fileId}/versions/{revision} [get]
func (h *handler) fileVersion(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	pid, ok := projectID(c)
	if !ok {
		return
	}
	fid, ok := httpx.UUIDParam(c, "fileId")
	if !ok {
		return
	}
	revision, err := strconv.ParseInt(c.Param("revision"), 10, 64)
	if err != nil || revision < 1 {
		httpx.Error(c, apperr.BadRequest("papers.invalid_revision", "Invalid file revision"))
		return
	}
	result, err := h.service.GetFileVersion(user, pid, fid, revision)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}
