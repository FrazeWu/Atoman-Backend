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

// createMusicAssetUpload godoc
// @Summary 创建可恢复的音乐音频上传
// @Tags music-uploads
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param input body CreateMusicAssetUploadInput true "音频文件信息"
// @Success 201 {object} MusicAssetUploadSessionDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Router /api/v1/music/uploads [post]
func (h *Handler) createMusicAssetUpload(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var input CreateMusicAssetUploadInput
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	session, err := h.service.CreateMusicAssetUpload(user, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, session)
}

func musicAssetUploadRouteUser(c *gin.Context) (authctx.CurrentUser, uuid.UUID, bool) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return authctx.CurrentUser{}, uuid.Nil, false
	}
	id, err := parseMusicID(c.Param("uploadId"), "uploadId")
	if err != nil {
		httpx.Error(c, err)
		return authctx.CurrentUser{}, uuid.Nil, false
	}
	return user, id, true
}

// getMusicAssetUpload godoc
// @Summary 获取可恢复音乐音频上传状态
// @Tags music-uploads
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param uploadId path string true "上传会话 UUID"
// @Success 200 {object} MusicAssetUploadSessionDTO
// @Router /api/v1/music/uploads/{uploadId} [get]
func (h *Handler) getMusicAssetUpload(c *gin.Context) {
	user, id, ok := musicAssetUploadRouteUser(c)
	if !ok {
		return
	}
	session, err := h.service.GetMusicAssetUpload(user, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, session)
}

// createMusicAssetUploadPart godoc
// @Summary 获取音乐音频分片上传地址
// @Tags music-uploads
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param uploadId path string true "上传会话 UUID"
// @Param partNumber path int true "分片序号"
// @Success 200 {object} MusicAssetUploadPartURL
// @Router /api/v1/music/uploads/{uploadId}/parts/{partNumber} [post]
func (h *Handler) createMusicAssetUploadPart(c *gin.Context) {
	user, id, ok := musicAssetUploadRouteUser(c)
	if !ok {
		return
	}
	partNumber, err := strconv.Atoi(c.Param("partNumber"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "partNumber must be an integer"))
		return
	}
	part, err := h.service.CreateMusicAssetUploadPart(user, id, partNumber)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, part)
}

// completeMusicAssetUploadPart godoc
// @Summary 记录音乐音频分片上传结果
// @Tags music-uploads
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param uploadId path string true "上传会话 UUID"
// @Param partNumber path int true "分片序号"
// @Param input body MusicAssetUploadPart true "分片 ETag 与大小"
// @Success 200 {object} MusicAssetUploadSessionDTO
// @Router /api/v1/music/uploads/{uploadId}/parts/{partNumber}/complete [post]
func (h *Handler) completeMusicAssetUploadPart(c *gin.Context) {
	user, id, ok := musicAssetUploadRouteUser(c)
	if !ok {
		return
	}
	partNumber, err := strconv.Atoi(c.Param("partNumber"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "partNumber must be an integer"))
		return
	}
	var input MusicAssetUploadPart
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	session, err := h.service.CompleteMusicAssetUploadPart(user, id, partNumber, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, session)
}

// completeMusicAssetUpload godoc
// @Summary 完成可恢复音乐音频上传
// @Tags music-uploads
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param uploadId path string true "上传会话 UUID"
// @Success 200 {object} model.MediaAsset
// @Router /api/v1/music/uploads/{uploadId}/complete [post]
func (h *Handler) completeMusicAssetUpload(c *gin.Context) {
	user, id, ok := musicAssetUploadRouteUser(c)
	if !ok {
		return
	}
	asset, err := h.service.CompleteMusicAssetUpload(user, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, asset)
}

// cancelMusicAssetUpload godoc
// @Summary 取消可恢复音乐音频上传
// @Tags music-uploads
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param uploadId path string true "上传会话 UUID"
// @Success 200 {object} map[string]boolean
// @Router /api/v1/music/uploads/{uploadId} [delete]
func (h *Handler) cancelMusicAssetUpload(c *gin.Context) {
	user, id, ok := musicAssetUploadRouteUser(c)
	if !ok {
		return
	}
	if err := h.service.CancelMusicAssetUpload(user, id); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"canceled": true})
}
