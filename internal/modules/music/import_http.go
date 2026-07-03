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

func albumImportMultipartRouteParams(c *gin.Context) (uuid.UUID, int, bool) {
	sessionID, err := parseMusicID(c.Param("sessionId"), "sessionId")
	if err != nil {
		httpx.Error(c, err)
		return uuid.Nil, 0, false
	}
	partNumber, err := strconv.Atoi(c.Param("partNumber"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "part number is invalid"))
		return uuid.Nil, 0, false
	}
	return sessionID, partNumber, true
}

func (h *Handler) getAlbumImportSession(c *gin.Context) {
	sessionID, err := parseMusicID(c.Param("sessionId"), "sessionId")
	if err != nil {
		httpx.Error(c, err)
		return
	}

	session, err := h.service.GetAlbumImportSession(sessionID)
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
