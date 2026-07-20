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
