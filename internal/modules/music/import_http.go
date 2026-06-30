package music

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
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
