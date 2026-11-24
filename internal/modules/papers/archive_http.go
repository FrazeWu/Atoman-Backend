package papers

import (
	"io"
	"mime"
	"net/http"
	"os"
	"strings"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
)

func archiveBusy(c *gin.Context) bool {
	select {
	case archiveSlots <- struct{}{}:
		return false
	default:
		httpx.Error(c, apperr.New(http.StatusTooManyRequests, "papers.archive_busy", "Project archive service is busy", nil))
		return true
	}
}

// @Summary 从 ZIP 导入论文项目
// @Tags papers
// @Security BearerAuth
// @Accept application/zip
// @Param name query string true "Project name"
// @Param main_file query string false "Root main TeX filename"
// @Param exclude_confirmation query string false "Exact archive digest returned by exclusion confirmation response"
// @Failure 409 {object} map[string]interface{}
// @Success 201 {object} map[string]interface{}
// @Router /api/v1/papers/projects/import [post]
func (h *handler) importProject(c *gin.Context) {
	u, ok := current(c)
	if !ok {
		return
	}
	if archiveBusy(c) {
		return
	}
	defer func() { <-archiveSlots }()
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxImportZIPBytes)
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		httpx.Error(c, importError("ZIP upload exceeds 5 MiB or could not be read"))
		return
	}
	p, err := h.service.ImportProject(c.Request.Context(), u, c.Query("name"), c.Query("main_file"), raw, c.Query("exclude_confirmation"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, p)
}

// @Summary 下载当前论文项目的 ZIP 源码
// @Tags papers
// @Security BearerAuth
// @Produce application/zip
// @Param id path string true "Project UUID"
// @Success 200 {file} binary
// @Router /api/v1/papers/projects/{id}/export [get]
func (h *handler) exportProject(c *gin.Context) {
	u, ok := current(c)
	if !ok {
		return
	}
	id, ok := projectID(c)
	if !ok {
		return
	}
	if archiveBusy(c) {
		return
	}
	defer func() { <-archiveSlots }()
	// A private temporary spool avoids holding a legacy 100 MiB text project
	// in heap memory. It is removed on every success and failure path.
	f, err := os.CreateTemp("", "papers-export-*.zip")
	if err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	defer f.Close()
	defer os.Remove(f.Name())
	name, err := h.service.ExportProject(c.Request.Context(), u, id, f)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	st, err := f.Stat()
	if err != nil || st.Size() > 110<<20 {
		httpx.Error(c, importError("Export exceeds the download limit"))
		return
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	name = strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(name) + ".zip"
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	http.ServeContent(c.Writer, c.Request, "project.zip", st.ModTime(), f)
}
