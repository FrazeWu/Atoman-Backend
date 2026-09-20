package music

import (
	"net/http"
	"strings"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type musicTagInput struct {
	Kind     string     `json:"kind"`
	Name     string     `json:"name"`
	ParentID *uuid.UUID `json:"parent_id"`
}

type musicTagVoteInput struct {
	Vote string `json:"vote"`
}

// searchMusicTags godoc
// @Summary 浏览或搜索公共音乐标签
// @Description 按标签类别、父级和关键词浏览公共标签目录。
// @Tags music
// @Produce json
// @Param kind query string false "标签类别" Enums(mood,type,scene,theme,instrument)
// @Param q query string false "搜索关键词"
// @Param parent_id query string false "父级标签 ID"
// @Param root query bool false "是否只返回根标签"
// @Success 200 {array} MusicTagOptionDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Router /api/v1/music/tags [get]
func (h *Handler) searchMusicTags(c *gin.Context) {
	kind := strings.TrimSpace(c.Query("kind"))
	parentID, err := parseOptionalMusicTagParentID(c.Query("parent_id"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	tags, err := h.service.ListMusicTagOptions(kind, strings.TrimSpace(c.Query("q")), parentID, c.Query("root") == "true")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, tags)
}

// createMusicTag godoc
// @Summary 创建公共音乐标签
// @Description 在指定标签维度和父级下创建标签；同名标签会复用已有标签。
// @Tags music
// @Accept json
// @Produce json
// @Param input body musicTagInput true "标签"
// @Success 200 {object} MusicTagOptionDTO
// @Success 201 {object} MusicTagOptionDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Router /api/v1/music/tags [post]
func (h *Handler) createMusicTag(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var input musicTagInput
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	tag, created, err := h.service.CreateMusicTag(user, input.Kind, input.Name, input.ParentID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.OK(c, status, tag)
}

// getMusicTag godoc
// @Summary 获取公共音乐标签详情
// @Description 根据标签 ID 获取标签名称和标签类别。
// @Tags music
// @Produce json
// @Param tagId path string true "标签 ID"
// @Success 200 {object} MusicTagOptionDTO
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/music/tags/{tagId} [get]
func (h *Handler) getMusicTag(c *gin.Context) {
	tagID, err := parseMusicID(c.Param("tagId"), "tagId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	tag, err := h.service.GetMusicTag(tagID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, tag)
}

// listSongTags godoc
// @Summary 获取歌曲标签
// @Tags music
// @Produce json
// @Param songId path string true "歌曲 ID"
// @Success 200 {array} MusicTagDTO
// @Router /api/v1/music/songs/{songId}/tags [get]
func (h *Handler) listSongTags(c *gin.Context) {
	h.listMusicTags(c, musicTagEntitySong)
}

// listAlbumTags godoc
// @Summary 获取专辑标签
// @Tags music
// @Produce json
// @Param albumId path string true "专辑 ID"
// @Success 200 {array} MusicTagDTO
// @Router /api/v1/music/albums/{albumId}/tags [get]
func (h *Handler) listAlbumTags(c *gin.Context) {
	h.listMusicTags(c, musicTagEntityAlbum)
}

func (h *Handler) listMusicTags(c *gin.Context, entityType string) {
	entityID, err := parseMusicID(c.Param(entityIDParam(entityType)), "entityId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	user, ok := currentMusicUser(c)
	var viewer *authctx.CurrentUser
	if ok {
		viewer = &user
	}
	tags, err := h.service.ListMusicTags(viewer, entityType, entityID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, tags)
}

// addSongTag godoc
// @Summary 添加歌曲标签
// @Tags music
// @Accept json
// @Produce json
// @Param songId path string true "歌曲 ID"
// @Param input body musicTagInput true "标签"
// @Success 201 {object} MusicTagDTO
// @Router /api/v1/music/songs/{songId}/tags [post]
func (h *Handler) addSongTag(c *gin.Context) {
	h.addMusicTag(c, musicTagEntitySong)
}

// addAlbumTag godoc
// @Summary 添加专辑标签
// @Tags music
// @Accept json
// @Produce json
// @Param albumId path string true "专辑 ID"
// @Param input body musicTagInput true "标签"
// @Success 201 {object} MusicTagDTO
// @Router /api/v1/music/albums/{albumId}/tags [post]
func (h *Handler) addAlbumTag(c *gin.Context) {
	h.addMusicTag(c, musicTagEntityAlbum)
}

func (h *Handler) addMusicTag(c *gin.Context, entityType string) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	entityID, err := parseMusicID(c.Param(entityIDParam(entityType)), "entityId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var input musicTagInput
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	tag, err := h.service.AddMusicTag(user, entityType, entityID, input.Kind, input.Name, input.ParentID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, tag)
}

// deleteSongTag godoc
// @Summary 删除歌曲标签
// @Tags music
// @Produce json
// @Param songId path string true "歌曲 ID"
// @Param tagId path string true "标签 ID"
// @Success 200 {object} map[string]bool
// @Router /api/v1/music/songs/{songId}/tags/{tagId} [delete]
func (h *Handler) deleteSongTag(c *gin.Context) {
	h.deleteMusicTag(c, musicTagEntitySong)
}

// deleteAlbumTag godoc
// @Summary 删除专辑标签
// @Tags music
// @Produce json
// @Param albumId path string true "专辑 ID"
// @Param tagId path string true "标签 ID"
// @Success 200 {object} map[string]bool
// @Router /api/v1/music/albums/{albumId}/tags/{tagId} [delete]
func (h *Handler) deleteAlbumTag(c *gin.Context) {
	h.deleteMusicTag(c, musicTagEntityAlbum)
}

func (h *Handler) deleteMusicTag(c *gin.Context, entityType string) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	entityID, err := parseMusicID(c.Param(entityIDParam(entityType)), "entityId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	tagID, err := parseMusicID(c.Param("tagId"), "tagId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.DeleteMusicTag(user, entityType, entityID, tagID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}

// voteSongTag godoc
// @Summary 评价歌曲标签
// @Tags music
// @Accept json
// @Produce json
// @Param songId path string true "歌曲 ID"
// @Param tagId path string true "标签 ID"
// @Param input body musicTagVoteInput true "投票，可选 up、down、none"
// @Success 200 {object} MusicTagDTO
// @Router /api/v1/music/songs/{songId}/tags/{tagId}/vote [put]
func (h *Handler) voteSongTag(c *gin.Context) {
	h.voteMusicTag(c, musicTagEntitySong)
}

// voteAlbumTag godoc
// @Summary 评价专辑标签
// @Tags music
// @Accept json
// @Produce json
// @Param albumId path string true "专辑 ID"
// @Param tagId path string true "标签 ID"
// @Param input body musicTagVoteInput true "投票，可选 up、down、none"
// @Success 200 {object} MusicTagDTO
// @Router /api/v1/music/albums/{albumId}/tags/{tagId}/vote [put]
func (h *Handler) voteAlbumTag(c *gin.Context) {
	h.voteMusicTag(c, musicTagEntityAlbum)
}

func (h *Handler) voteMusicTag(c *gin.Context, entityType string) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	entityID, err := parseMusicID(c.Param(entityIDParam(entityType)), "entityId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	tagID, err := parseMusicID(c.Param("tagId"), "tagId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var input musicTagVoteInput
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	tag, err := h.service.VoteMusicTag(user, entityType, entityID, tagID, input.Vote)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, tag)
}

func entityIDParam(entityType string) string {
	if entityType == musicTagEntityAlbum {
		return "albumId"
	}
	return "songId"
}

func parseOptionalMusicTagParentID(raw string) (*uuid.UUID, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, nil
	}
	parentID, err := parseMusicID(value, "parent_id")
	if err != nil {
		return nil, err
	}
	return &parentID, nil
}
