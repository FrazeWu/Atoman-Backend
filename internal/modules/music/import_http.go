package music

import (
	"net/http"
	"strconv"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (h *Handler) createAlbumImportSession(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	var req CreateAlbumImportSessionInput
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}

	session, err := h.service.CreateAlbumImportSession(user, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, buildAlbumImportDTO(session))
}

func (h *Handler) listAlbumImportSessions(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	sessions, err := h.service.ListAlbumImportSessionsForUser(user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	data := make([]AlbumImportDTO, 0, len(sessions))
	for _, session := range sessions {
		data = append(data, buildAlbumImportDTO(session))
	}
	httpx.OK(c, http.StatusOK, data)
}

func (h *Handler) uploadAlbumImportArchive(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	sessionID, err := parseMusicID(c.Param("sessionId"), "sessionId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	limits := albumImportUploadLimitsFromEnv()
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limits.MaxFileBytes+1024*1024)

	file, header, err := c.Request.FormFile("archive")
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "archive file is required"))
		return
	}
	defer func() {
		_ = file.Close()
	}()

	session, err := h.service.UploadAlbumImportArchive(user, sessionID, header.Filename, file)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, buildAlbumImportDTO(session))
}

func (h *Handler) startAlbumImportMultipart(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	sessionID, err := parseMusicID(c.Param("sessionId"), "sessionId")
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var req StartAlbumImportMultipartInput
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}

	multipart, err := h.service.StartAlbumImportMultipart(user, sessionID, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, multipart)
}

func (h *Handler) createAlbumImportMultipartPartUpload(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	sessionID, partNumber, ok := albumImportMultipartRouteParams(c)
	if !ok {
		return
	}

	var req CreateAlbumImportMultipartPartInput
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}

	upload, err := h.service.CreateAlbumImportMultipartPartUpload(user, sessionID, partNumber, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, upload)
}

func (h *Handler) completeAlbumImportMultipartPart(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	sessionID, partNumber, ok := albumImportMultipartRouteParams(c)
	if !ok {
		return
	}

	var req CompleteAlbumImportMultipartPartInput
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}

	multipart, err := h.service.CompleteAlbumImportMultipartPart(user, sessionID, partNumber, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, multipart)
}

func (h *Handler) completeAlbumImportMultipart(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	sessionID, err := parseMusicID(c.Param("sessionId"), "sessionId")
	if err != nil {
		httpx.Error(c, err)
		return
	}

	session, err := h.service.CompleteAlbumImportMultipart(user, sessionID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, buildAlbumImportDTO(session))
}

// registerAlbumImportFiles godoc
// @Summary 登记专辑导入文件
// @Tags music-imports
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param sessionId path string true "导入会话 UUID"
// @Param input body RegisterAlbumImportFilesInput true "文件列表"
// @Success 200 {object} AlbumImportResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 422 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Router /api/v1/music/imports/albums/{sessionId}/files [post]
func (h *Handler) registerAlbumImportFiles(c *gin.Context) {
	user, sessionID, ok := albumImportSessionRouteUser(c)
	if !ok {
		return
	}
	var req RegisterAlbumImportFilesInput
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	session, err := h.service.RegisterAlbumImportFiles(user, sessionID, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, buildAlbumImportDTO(session))
}

// createAlbumImportFilePartUpload godoc
// @Summary 获取专辑导入文件分片上传地址
// @Tags music-imports
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param sessionId path string true "导入会话 UUID"
// @Param fileId path string true "导入文件 UUID"
// @Param partNumber path int true "分片序号"
// @Success 200 {object} AlbumImportMultipartPartUploadResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 422 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Router /api/v1/music/imports/albums/{sessionId}/files/{fileId}/parts/{partNumber} [post]
func (h *Handler) createAlbumImportFilePartUpload(c *gin.Context) {
	user, sessionID, fileID, partNumber, ok := albumImportFilePartRouteParams(c)
	if !ok {
		return
	}
	upload, err := h.service.CreateAlbumImportFilePartUpload(user, sessionID, fileID, partNumber, CreateAlbumImportMultipartPartInput{})
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, upload)
}

// completeAlbumImportFilePart godoc
// @Summary 完成专辑导入文件分片上传
// @Tags music-imports
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param sessionId path string true "导入会话 UUID"
// @Param fileId path string true "导入文件 UUID"
// @Param partNumber path int true "分片序号"
// @Param input body CompleteAlbumImportMultipartPartInput true "分片上传结果"
// @Success 200 {object} AlbumImportFileResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 422 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Router /api/v1/music/imports/albums/{sessionId}/files/{fileId}/parts/{partNumber}/complete [post]
func (h *Handler) completeAlbumImportFilePart(c *gin.Context) {
	user, sessionID, fileID, partNumber, ok := albumImportFilePartRouteParams(c)
	if !ok {
		return
	}
	var req CompleteAlbumImportMultipartPartInput
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	file, err := h.service.CompleteAlbumImportFilePart(user, sessionID, fileID, partNumber, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, file)
}

// completeAlbumImportFile godoc
// @Summary 完成专辑导入文件上传
// @Tags music-imports
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param sessionId path string true "导入会话 UUID"
// @Param fileId path string true "导入文件 UUID"
// @Success 200 {object} AlbumImportFileResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 422 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Router /api/v1/music/imports/albums/{sessionId}/files/{fileId}/complete [post]
func (h *Handler) completeAlbumImportFile(c *gin.Context) {
	user, sessionID, fileID, ok := albumImportFileRouteParams(c)
	if !ok {
		return
	}
	file, err := h.service.CompleteAlbumImportFile(user, sessionID, fileID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, file)
}

// completeAlbumImportSession godoc
// @Summary 完成专辑导入会话上传
// @Tags music-imports
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param sessionId path string true "导入会话 UUID"
// @Success 200 {object} AlbumImportResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 422 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Router /api/v1/music/imports/albums/{sessionId}/complete [post]
func (h *Handler) completeAlbumImportSession(c *gin.Context) {
	user, sessionID, ok := albumImportSessionRouteUser(c)
	if !ok {
		return
	}
	session, err := h.service.CompleteAlbumImportSession(user, sessionID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, buildAlbumImportDTO(session))
}

// cancelAlbumImportSession godoc
// @Summary 取消专辑导入会话
// @Tags music-imports
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param sessionId path string true "导入会话 UUID"
// @Success 200 {object} AlbumImportResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 422 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Router /api/v1/music/imports/albums/{sessionId} [delete]
func (h *Handler) cancelAlbumImportSession(c *gin.Context) {
	user, sessionID, ok := albumImportSessionRouteUser(c)
	if !ok {
		return
	}
	session, err := h.service.CancelAlbumImportSession(user, sessionID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, buildAlbumImportDTO(session))
}

// deleteAlbumImportFile godoc
// @Summary 删除专辑导入文件
// @Tags music-imports
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param sessionId path string true "导入会话 UUID"
// @Param fileId path string true "导入文件 UUID"
// @Success 204
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 422 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Router /api/v1/music/imports/albums/{sessionId}/files/{fileId} [delete]
func (h *Handler) deleteAlbumImportFile(c *gin.Context) {
	user, sessionID, fileID, ok := albumImportFileRouteParams(c)
	if !ok {
		return
	}
	if err := h.service.DeleteAlbumImportFile(user, sessionID, fileID); err != nil {
		httpx.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// retryAlbumImportFile godoc
// @Summary 重试专辑导入文件
// @Tags music-imports
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param sessionId path string true "导入会话 UUID"
// @Param fileId path string true "导入文件 UUID"
// @Success 200 {object} AlbumImportResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 422 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Router /api/v1/music/imports/albums/{sessionId}/files/{fileId}/retry [post]
func (h *Handler) retryAlbumImportFile(c *gin.Context) {
	user, sessionID, fileID, ok := albumImportFileRouteParams(c)
	if !ok {
		return
	}
	session, err := h.service.RetryAlbumImportFile(user, sessionID, fileID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, buildAlbumImportDTO(session))
}

// replaceAlbumImportFile godoc
// @Summary 替换专辑导入文件
// @Tags music-imports
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param sessionId path string true "导入会话 UUID"
// @Param fileId path string true "导入文件 UUID"
// @Param input body AlbumImportFileInput true "替换文件"
// @Success 200 {object} AlbumImportFileResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 422 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Router /api/v1/music/imports/albums/{sessionId}/files/{fileId}/replace [post]
func (h *Handler) replaceAlbumImportFile(c *gin.Context) {
	user, sessionID, fileID, ok := albumImportFileRouteParams(c)
	if !ok {
		return
	}
	var req AlbumImportFileInput
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	file, err := h.service.ReplaceAlbumImportFile(user, sessionID, fileID, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, file)
}

func albumImportSessionRouteUser(c *gin.Context) (authctx.CurrentUser, uuid.UUID, bool) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return authctx.CurrentUser{}, uuid.Nil, false
	}
	sessionID, err := parseMusicID(c.Param("sessionId"), "sessionId")
	if err != nil {
		httpx.Error(c, err)
		return authctx.CurrentUser{}, uuid.Nil, false
	}
	return user, sessionID, true
}

func albumImportFileRouteParams(c *gin.Context) (authctx.CurrentUser, uuid.UUID, uuid.UUID, bool) {
	user, sessionID, ok := albumImportSessionRouteUser(c)
	if !ok {
		return authctx.CurrentUser{}, uuid.Nil, uuid.Nil, false
	}
	fileID, err := parseMusicID(c.Param("fileId"), "fileId")
	if err != nil {
		httpx.Error(c, err)
		return authctx.CurrentUser{}, uuid.Nil, uuid.Nil, false
	}
	return user, sessionID, fileID, true
}

func albumImportFilePartRouteParams(c *gin.Context) (authctx.CurrentUser, uuid.UUID, uuid.UUID, int, bool) {
	user, sessionID, fileID, ok := albumImportFileRouteParams(c)
	if !ok {
		return authctx.CurrentUser{}, uuid.Nil, uuid.Nil, 0, false
	}
	partNumber, err := strconv.Atoi(c.Param("partNumber"))
	if err != nil || partNumber <= 0 {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "part number is invalid"))
		return authctx.CurrentUser{}, uuid.Nil, uuid.Nil, 0, false
	}
	return user, sessionID, fileID, partNumber, true
}

func albumImportMultipartRouteParams(c *gin.Context) (uuid.UUID, int, bool) {
	sessionID, err := parseMusicID(c.Param("sessionId"), "sessionId")
	if err != nil {
		httpx.Error(c, err)
		return uuid.Nil, 0, false
	}
	partNumber, err := strconv.Atoi(c.Param("partNumber"))
	if err != nil || partNumber <= 0 {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "part number is invalid"))
		return uuid.Nil, 0, false
	}
	return sessionID, partNumber, true
}

func (h *Handler) getAlbumImportSession(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	sessionID, err := parseMusicID(c.Param("sessionId"), "sessionId")
	if err != nil {
		httpx.Error(c, err)
		return
	}

	session, err := h.service.GetAlbumImportSessionForUser(user, sessionID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, buildAlbumImportDTO(session))
}

// repairAlbumImportSession godoc
// @Summary 开始修复已提交的专辑导入
// @Tags music-imports
// @Produce json
// @Success 200 {object} AlbumImportResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 422 {object} handlers.ErrorResponse
// @Router /api/v1/music/imports/albums/{sessionId}/repair [post]
func (h *Handler) repairAlbumImportSession(c *gin.Context) {
	user, sessionID, ok := albumImportSessionRouteUser(c)
	if !ok {
		return
	}
	session, err := h.service.RepairAlbumImportSession(user, sessionID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, buildAlbumImportDTO(session))
}

func (h *Handler) commitAlbumImportSession(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	sessionID, err := parseMusicID(c.Param("sessionId"), "sessionId")
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var req CommitAlbumImportSessionInput
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}

	session, err := h.service.CommitAlbumImportSession(user, sessionID, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, buildAlbumImportDTO(session))
}
