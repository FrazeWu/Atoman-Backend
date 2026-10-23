package music

import (
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
	"net/http"
)

// editTagWiki godoc
// @Summary 修改音乐标签 Wiki
// @Description rename、move、merge、revert；base_version 必填，merge 同时校验 target_version；preview=true 仅事务模拟返回影响范围，不写入。重叠投票采用最后修改的一票，保留来源审计。
// @Tags music
// @Accept json
// @Produce json
// @Param tagId path string true "标签 ID"
// @Param action path string true "操作" Enums(rename,move,merge,revert)
// @Param preview query bool false "仅预览影响"
// @Param input body MusicTagWikiInput true "变更及版本"
// @Success 200 {object} musicTagSnapshot
// @Failure 409 {object} handlers.ErrorResponse
// @Router /api/v1/music/tags/{tagId}/wiki/{action} [post]
func (h *Handler) editTagWiki(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	id, err := parseMusicID(c.Param("tagId"), "tagId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var input MusicTagWikiInput
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	result, err := h.service.EditMusicTag(user, id, c.Param("action"), input, c.Query("preview") == "true")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// tagWikiHistory godoc
// @Summary 查看音乐标签 Wiki 历史
// @Tags music
// @Produce json
// @Param tagId path string true "标签 ID"
// @Success 200 {object} MusicTagHistory
// @Router /api/v1/music/tags/{tagId}/history [get]
func (h *Handler) tagWikiHistory(c *gin.Context) {
	id, err := parseMusicID(c.Param("tagId"), "tagId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	result, err := h.service.MusicTagHistory(nil, "tag", id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// tagAssignmentHistory godoc
// @Summary 查看作品标签关联历史及当前版本
// @Tags music
// @Produce json
// @Param entityType path string true "song 或 album"
// @Param entityId path string true "作品 ID"
// @Success 200 {object} MusicTagHistory
// @Router /api/v1/music/entries/{entityType}/{entityId}/tags/history [get]
func (h *Handler) tagAssignmentHistory(c *gin.Context) {
	id, err := parseMusicID(c.Param("entityId"), "entityId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	user, ok := currentMusicUser(c)
	result, err := h.service.MusicTagHistory(musicViewer(user, ok), c.Param("entityType"), id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// editTagAssignments godoc
// @Summary 原子增加、解除或替换作品标签
// @Description 所有登录用户可编辑可见且允许 Wiki 编辑的作品。关联版本冲突返回 409；恢复原关联身份及投票；五类共享 12 个上限，同分支风格不能重复绑定。
// @Tags music
// @Accept json
// @Produce json
// @Param entityType path string true "song 或 album"
// @Param entityId path string true "作品 ID"
// @Param input body MusicTagAssignmentEdit true "操作与关联版本"
// @Success 200 {object} MusicTagHistory
// @Failure 409 {object} handlers.ErrorResponse
// @Router /api/v1/music/entries/{entityType}/{entityId}/tags [patch]
func (h *Handler) editTagAssignments(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	id, err := parseMusicID(c.Param("entityId"), "entityId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var input MusicTagAssignmentEdit
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	result, err := h.service.EditMusicTagAssignments(user, c.Param("entityType"), id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}
