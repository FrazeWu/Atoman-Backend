package music

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

type musicTagInput struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type musicTagVoteInput struct {
	Vote string `json:"vote"`
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
	tag, err := h.service.AddMusicTag(user, entityType, entityID, input.Kind, input.Name)
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
