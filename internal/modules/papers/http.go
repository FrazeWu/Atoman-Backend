package papers

import (
	"net/http"
	"time"

	"atoman/internal/middleware"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type handler struct{ service *Service }

type projectInput struct {
	Name         string `json:"name"`
	BaseRevision int64  `json:"base_revision"`
}
type fileInput struct {
	Path         string `json:"path"`
	Content      string `json:"content"`
	BaseRevision int64  `json:"base_revision"`
}
type grantInput struct {
	UserID    uuid.UUID  `json:"user_id"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// RegisterRoutes mounts all Papers routes behind Atoman session authentication.
// Admin invitation management is separately role-gated, while every project
// endpoint checks the per-user module grant and project ownership or explicit project membership server-side.
func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &handler{service: service}
	group.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "private, no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Next()
	})
	group.Use(middleware.AuthMiddleware())
	group.GET("/access", h.access)
	group.GET("/application", h.application)
	group.POST("/application", h.apply)
	admin := group.Group("/admin")
	admin.GET("/applications", h.applications)
	admin.POST("/applications/:id/review", h.reviewApplication)
	admin.POST("/grants", h.grant)
	admin.DELETE("/grants/:userId", h.revoke)

	private := group.Group("")
	private.Use(h.requireInvite)
	private.GET("/projects", h.projects)
	private.GET("/projects/trash", h.trashedProjects)
	private.POST("/projects/:id/restore", h.restoreProject)
	private.POST("/projects", h.createProject)
	private.POST("/projects/import", h.importProject)
	private.GET("/projects/:id/export", h.exportProject)
	private.GET("/projects/:id", h.project)
	private.GET("/projects/:id/members", h.members)
	private.PUT("/projects/:id/members/:userId", h.setMember)
	private.DELETE("/projects/:id/members/:userId", h.removeMember)
	private.PATCH("/projects/:id", h.renameProject)
	private.DELETE("/projects/:id", h.deleteProject)
	private.GET("/projects/:id/files", h.files)
	private.POST("/projects/:id/files", h.createFile)
	private.POST("/projects/:id/files/manage", h.manageFiles)
	private.GET("/projects/:id/files/:fileId", h.file)
	private.PUT("/projects/:id/files/:fileId", h.saveFile)
	private.GET("/projects/:id/files/:fileId/collaboration", h.collaborationCapability)
	private.GET("/projects/:id/files/:fileId/collaboration/ws", h.collaborationSocket)
	private.GET("/projects/:id/files/:fileId/versions", h.fileVersions)
	private.GET("/projects/:id/files/:fileId/versions/:revision", h.fileVersion)
	private.GET("/projects/:id/snapshots", h.projectSnapshots)
	private.POST("/projects/:id/snapshots", h.createSnapshot)
	private.GET("/projects/:id/snapshots/:snapshotId", h.snapshotDetail)
	private.DELETE("/projects/:id/snapshots/:snapshotId", h.deleteSnapshot)
	private.POST("/projects/:id/snapshots/:snapshotId/restore", h.restoreSnapshot)
	// These routes are deliberately fail-closed in production: NewService(db)
	// does NOT provide the trusted publisher, signed environment or queue.
	private.GET("/projects/:id/compile-capability", h.compileCapability)
	private.POST("/projects/:id/compiles", h.submitCompile)
	private.GET("/projects/:id/compiles", h.compileHistory)
	private.GET("/projects/:id/compiles/:jobId", h.compileStatus)
	private.DELETE("/projects/:id/compiles/:jobId", h.cancelCompile)
	private.GET("/projects/:id/compiles/:jobId/pdf", h.compilePDF)
	private.GET("/projects/:id/compiles/:jobId/source-map", h.compileSourceMap)
}

func current(c *gin.Context) (authctx.CurrentUser, bool) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return user, false
	}
	return user, true
}

func (h *handler) requireInvite(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		c.Abort()
		return
	}
	if err := h.service.RequireAccess(user); err != nil {
		httpx.Error(c, err)
		c.Abort()
		return
	}
	c.Next()
}

// @Summary 查询 Papers 访问资格
// @Tags papers
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 401 {object} map[string]interface{}
// @Failure 403 {object} map[string]interface{}
// @Router /api/v1/papers/access [get]
func (h *handler) access(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	allowed, err := h.service.AccessAllowed(user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"allowed": allowed})
}

// @Summary 管理员授予 Papers 资格
// @Tags papers
// @Security BearerAuth
// @Produce json
// @Accept json
// @Param input body grantInput true "Invite"
// @Success 200 {object} map[string]interface{}
// @Failure 401 {object} map[string]interface{}
// @Failure 403 {object} map[string]interface{}
// @Router /api/v1/papers/admin/grants [post]
func (h *handler) grant(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	var input grantInput
	if !httpx.BindRequiredJSON(c, &input) {
		return
	}
	if err := h.service.GrantAccess(user, input.UserID, input.ExpiresAt); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"granted": true})
}

// @Summary 管理员撤销 Papers 资格
// @Tags papers
// @Security BearerAuth
// @Produce json
// @Param userId path string true "User UUID"
// @Success 204
// @Failure 401 {object} map[string]interface{}
// @Failure 403 {object} map[string]interface{}
// @Router /api/v1/papers/admin/grants/{userId} [delete]
func (h *handler) revoke(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	target, ok := httpx.UUIDParam(c, "userId")
	if !ok {
		return
	}
	if err := h.service.RevokeAccess(user, target); err != nil {
		httpx.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func projectID(c *gin.Context) (uuid.UUID, bool) { return httpx.UUIDParam(c, "id") }

// @Summary 列出当前用户私有论文项目
// @Tags papers
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 401 {object} map[string]interface{}
// @Failure 403 {object} map[string]interface{}
// @Router /api/v1/papers/projects [get]
func (h *handler) projects(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	projects, err := h.service.ListProjects(user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, projects)
}

// @Summary 创建私有论文项目
// @Tags papers
// @Security BearerAuth
// @Produce json
// @Accept json
// @Param input body projectInput true "Project"
// @Success 201 {object} map[string]interface{}
// @Failure 401 {object} map[string]interface{}
// @Failure 403 {object} map[string]interface{}
// @Router /api/v1/papers/projects [post]
func (h *handler) createProject(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	var input projectInput
	if !httpx.BindRequiredJSON(c, &input) {
		return
	}
	p, err := h.service.CreateProject(user, input.Name)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, p)
}

// @Summary 读取私有论文项目
// @Tags papers
// @Security BearerAuth
// @Produce json
// @Param id path string true "Project UUID"
// @Success 200 {object} map[string]interface{}
// @Failure 401 {object} map[string]interface{}
// @Failure 403 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id} [get]
func (h *handler) project(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	id, ok := projectID(c)
	if !ok {
		return
	}
	p, err := h.service.ReadProject(user, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, p)
}

// @Summary 更新论文项目名称
// @Tags papers
// @Security BearerAuth
// @Produce json
// @Accept json
// @Param id path string true "Project UUID"
// @Param input body projectInput true "Project and base revision"
// @Success 200 {object} map[string]interface{}
// @Failure 401 {object} map[string]interface{}
// @Failure 403 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id} [patch]
func (h *handler) renameProject(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	id, ok := projectID(c)
	if !ok {
		return
	}
	var input projectInput
	if !httpx.BindRequiredJSON(c, &input) {
		return
	}
	p, err := h.service.RenameProject(user, id, input.Name, input.BaseRevision)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, p)
}

// @Summary 删除私有论文项目
// @Tags papers
// @Security BearerAuth
// @Produce json
// @Param id path string true "Project UUID"
// @Success 204
// @Failure 401 {object} map[string]interface{}
// @Failure 403 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id} [delete]
func (h *handler) deleteProject(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	id, ok := projectID(c)
	if !ok {
		return
	}
	if err := h.service.DeleteProject(user, id); err != nil {
		httpx.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// @Summary 列出私有项目文本文件
// @Tags papers
// @Security BearerAuth
// @Produce json
// @Param id path string true "Project UUID"
// @Success 200 {object} map[string]interface{}
// @Failure 401 {object} map[string]interface{}
// @Failure 403 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/files [get]
func (h *handler) files(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	id, ok := projectID(c)
	if !ok {
		return
	}
	files, err := h.service.ListFiles(user, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, files)
}

// @Summary 创建 LaTeX 文本文件
// @Tags papers
// @Security BearerAuth
// @Produce json
// @Accept json
// @Param id path string true "Project UUID"
// @Param input body fileInput true "Text file"
// @Success 201 {object} map[string]interface{}
// @Failure 401 {object} map[string]interface{}
// @Failure 403 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/files [post]
func (h *handler) createFile(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	id, ok := projectID(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 7<<20)
	var input fileInput
	if !httpx.BindRequiredJSON(c, &input) {
		return
	}
	file, err := h.service.CreateFile(user, id, input.Path, input.Content)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, file)
}

// @Summary 读取私有项目文本内容
// @Tags papers
// @Security BearerAuth
// @Produce json
// @Param id path string true "Project UUID"
// @Param fileId path string true "File UUID"
// @Success 200 {object} map[string]interface{}
// @Failure 401 {object} map[string]interface{}
// @Failure 403 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/files/{fileId} [get]
func (h *handler) file(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	id, ok := projectID(c)
	if !ok {
		return
	}
	fileID, ok := httpx.UUIDParam(c, "fileId")
	if !ok {
		return
	}
	file, err := h.service.GetFile(user, id, fileID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, file)
}

// @Summary 乐观锁保存 LaTeX 文本内容
// @Tags papers
// @Security BearerAuth
// @Produce json
// @Accept json
// @Param id path string true "Project UUID"
// @Param fileId path string true "File UUID"
// @Param input body fileInput true "Text and base revision"
// @Success 200 {object} map[string]interface{}
// @Failure 401 {object} map[string]interface{}
// @Failure 403 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/files/{fileId} [put]
func (h *handler) saveFile(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	id, ok := projectID(c)
	if !ok {
		return
	}
	fileID, ok := httpx.UUIDParam(c, "fileId")
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 7<<20)
	var input fileInput
	if !httpx.BindRequiredJSON(c, &input) {
		return
	}
	file, err := h.service.SaveFile(user, id, fileID, input.Content, input.BaseRevision)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, file)
}

// @Summary 原子上传或管理论文项目文件
// @Tags papers
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param id path string true "Project UUID"
// @Param input body FileChange true "Project revision and file operation"
// @Success 200 {object} map[string]interface{}
// @Failure 409 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/files/manage [post]
func (h *handler) manageFiles(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	id, ok := projectID(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<20)
	var input FileChange
	if !httpx.BindRequiredJSON(c, &input) {
		return
	}
	result, err := h.service.ManageFiles(user, id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// @Summary 查询我的论文回收站
// @Tags papers
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/papers/projects/trash [get]
func (h *handler) trashedProjects(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	result, err := h.service.ListTrashedProjects(user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// @Summary 恢复已删除的私有论文
// @Tags papers
// @Security BearerAuth
// @Produce json
// @Param id path string true "Project UUID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/restore [post]
func (h *handler) restoreProject(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	id, ok := projectID(c)
	if !ok {
		return
	}
	result, err := h.service.RestoreProject(user, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}
