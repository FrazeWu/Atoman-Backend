package music

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type SaveLyricsRequest struct {
	Content               string                      `json:"content"`
	Translation           string                      `json:"translation"`
	Format                string                      `json:"format"`
	EditSummary           string                      `json:"edit_summary"`
	AnnotationResolutions []AnnotationResolutionInput `json:"annotation_resolutions"`
}

type CreateLyricAnnotationRequest struct {
	LineID       uuid.UUID `json:"line_id"`
	LineKey      string    `json:"line_key"`
	SelectedText string    `json:"selected_text"`
	StartOffset  int       `json:"start_offset"`
	EndOffset    int       `json:"end_offset"`
	Body         string    `json:"body"`
}

type UpdateLyricAnnotationRequest struct {
	Body string `json:"body"`
}

type LyricAnnotationVoteRequest struct {
	Vote string `json:"vote"`
}

type MusicLyricsResponse struct {
	Data MusicLyricsDTO `json:"data"`
}

type MusicLyricAnnotationResponse struct {
	Data MusicLyricAnnotationDTO `json:"data"`
}

type DeleteLyricAnnotationResponse struct {
	Data struct {
		Deleted bool `json:"deleted"`
	} `json:"data"`
}

type MusicLyricsErrorResponse struct {
	Error MusicLyricsErrorBody `json:"error"`
}

type MusicLyricsErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

// getSongLyrics godoc
// @Summary 获取歌曲歌词
// @Description 匿名用户可读取当前歌词、逐行翻译和公开注释。
// @Tags music-lyrics
// @Produce json
// @Param songId path string true "歌曲 UUID"
// @Success 200 {object} MusicLyricsResponse
// @Failure 400 {object} MusicLyricsErrorResponse
// @Failure 404 {object} MusicLyricsErrorResponse
// @Failure 500 {object} MusicLyricsErrorResponse
// @Router /api/v1/music/songs/{songId}/lyrics [get]
func (h *Handler) getSongLyrics(c *gin.Context) {
	songID, err := parseMusicID(c.Param("songId"), "songId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	user, _ := currentMusicUser(c)
	lyrics, err := h.service.GetSongLyrics(user, songID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, lyrics)
}

// saveSongLyrics godoc
// @Summary 保存歌曲歌词
// @Description 保存 LRC 或纯文本歌词、逐行翻译，并处理失效注释锚点。
// @Tags music-lyrics
// @Accept json
// @Produce json
// @Param songId path string true "歌曲 UUID"
// @Param input body SaveLyricsRequest true "歌词内容"
// @Success 200 {object} MusicLyricsResponse
// @Failure 400 {object} MusicLyricsErrorResponse
// @Failure 401 {object} MusicLyricsErrorResponse
// @Failure 404 {object} MusicLyricsErrorResponse
// @Failure 409 {object} MusicLyricsErrorResponse
// @Failure 500 {object} MusicLyricsErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/music/songs/{songId}/lyrics [put]
func (h *Handler) saveSongLyrics(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	songID, err := parseMusicID(c.Param("songId"), "songId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var req SaveLyricsRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	lyrics, err := h.service.SaveSongLyrics(user, songID, SaveLyricsInput(req))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, lyrics)
}

// createLyricAnnotation godoc
// @Summary 创建歌词注释
// @Description 使用当前歌词行的 line_key 或可选 line_id 创建文本锚点注释。
// @Tags music-lyrics
// @Accept json
// @Produce json
// @Param songId path string true "歌曲 UUID"
// @Param input body CreateLyricAnnotationRequest true "歌词注释"
// @Success 201 {object} MusicLyricAnnotationResponse
// @Failure 400 {object} MusicLyricsErrorResponse
// @Failure 401 {object} MusicLyricsErrorResponse
// @Failure 404 {object} MusicLyricsErrorResponse
// @Failure 500 {object} MusicLyricsErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/music/songs/{songId}/lyrics/annotations [post]
func (h *Handler) createLyricAnnotation(c *gin.Context) {
	user, songID, ok := musicLyricsWriteContext(c)
	if !ok {
		return
	}
	var req CreateLyricAnnotationRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	annotation, err := h.service.CreateLyricAnnotation(user, songID, CreateAnnotationInput(req))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, annotation)
}

// updateLyricAnnotation godoc
// @Summary 修改歌词注释
// @Tags music-lyrics
// @Accept json
// @Produce json
// @Param songId path string true "歌曲 UUID"
// @Param annotationId path string true "注释 UUID"
// @Param input body UpdateLyricAnnotationRequest true "注释正文"
// @Success 200 {object} MusicLyricAnnotationResponse
// @Failure 400 {object} MusicLyricsErrorResponse
// @Failure 401 {object} MusicLyricsErrorResponse
// @Failure 403 {object} MusicLyricsErrorResponse
// @Failure 404 {object} MusicLyricsErrorResponse
// @Failure 500 {object} MusicLyricsErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/music/songs/{songId}/lyrics/annotations/{annotationId} [patch]
func (h *Handler) updateLyricAnnotation(c *gin.Context) {
	user, songID, annotationID, ok := musicLyricsAnnotationWriteContext(c)
	if !ok {
		return
	}
	var req UpdateLyricAnnotationRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	annotation, err := h.service.UpdateLyricAnnotation(user, songID, annotationID, req.Body)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, annotation)
}

// deleteLyricAnnotation godoc
// @Summary 删除歌词注释
// @Tags music-lyrics
// @Produce json
// @Param songId path string true "歌曲 UUID"
// @Param annotationId path string true "注释 UUID"
// @Success 200 {object} DeleteLyricAnnotationResponse
// @Failure 400 {object} MusicLyricsErrorResponse
// @Failure 401 {object} MusicLyricsErrorResponse
// @Failure 403 {object} MusicLyricsErrorResponse
// @Failure 404 {object} MusicLyricsErrorResponse
// @Failure 500 {object} MusicLyricsErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/music/songs/{songId}/lyrics/annotations/{annotationId} [delete]
func (h *Handler) deleteLyricAnnotation(c *gin.Context) {
	user, songID, annotationID, ok := musicLyricsAnnotationWriteContext(c)
	if !ok {
		return
	}
	if err := h.service.DeleteLyricAnnotation(user, songID, annotationID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}

// voteLyricAnnotation godoc
// @Summary 设置歌词注释投票
// @Description vote 可为 up、down 或 none；none 表示撤销投票。
// @Tags music-lyrics
// @Accept json
// @Produce json
// @Param songId path string true "歌曲 UUID"
// @Param annotationId path string true "注释 UUID"
// @Param input body LyricAnnotationVoteRequest true "投票"
// @Success 200 {object} MusicLyricAnnotationResponse
// @Failure 400 {object} MusicLyricsErrorResponse
// @Failure 401 {object} MusicLyricsErrorResponse
// @Failure 404 {object} MusicLyricsErrorResponse
// @Failure 500 {object} MusicLyricsErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/music/songs/{songId}/lyrics/annotations/{annotationId}/votes [post]
func (h *Handler) voteLyricAnnotation(c *gin.Context) {
	user, songID, annotationID, ok := musicLyricsAnnotationWriteContext(c)
	if !ok {
		return
	}
	var req LyricAnnotationVoteRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	annotation, err := h.service.SetLyricAnnotationVote(user, songID, annotationID, req.Vote)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, annotation)
}

func musicLyricsWriteContext(c *gin.Context) (authctx.CurrentUser, uuid.UUID, bool) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return authctx.CurrentUser{}, uuid.Nil, false
	}
	songID, err := parseMusicID(c.Param("songId"), "songId")
	if err != nil {
		httpx.Error(c, err)
		return authctx.CurrentUser{}, uuid.Nil, false
	}
	return user, songID, true
}

func musicLyricsAnnotationWriteContext(c *gin.Context) (authctx.CurrentUser, uuid.UUID, uuid.UUID, bool) {
	user, songID, ok := musicLyricsWriteContext(c)
	if !ok {
		return authctx.CurrentUser{}, uuid.Nil, uuid.Nil, false
	}
	annotationID, err := parseMusicID(c.Param("annotationId"), "annotationId")
	if err != nil {
		httpx.Error(c, err)
		return authctx.CurrentUser{}, uuid.Nil, uuid.Nil, false
	}
	return user, songID, annotationID, true
}
