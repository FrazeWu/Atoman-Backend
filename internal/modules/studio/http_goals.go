package studio

import (
	"net/http"

	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

// listGoals godoc
// @Summary 获取频道经营目标
// @Tags studio
// @Produce json
// @Param channel_id query string false "频道 UUID"
// @Success 200 {object} StudioGoalsResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Router /api/v1/studio/goals [get]
func (h *Handler) listGoals(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	channelID, ok := optionalUUIDQuery(c, "channel_id")
	if !ok {
		return
	}
	response, err := h.service.ListGoals(user, channelID)
	respond(c, http.StatusOK, response, err)
}

// createGoalCycle godoc
// @Summary 创建经营目标周期
// @Tags studio
// @Accept json
// @Produce json
// @Param input body CreateStudioGoalCycleInput true "经营目标周期"
// @Success 201 {object} StudioGoalCycle
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 409 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Router /api/v1/studio/goals/cycles [post]
func (h *Handler) createGoalCycle(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	var input CreateStudioGoalCycleInput
	if !bindJSON(c, &input) {
		return
	}
	response, err := h.service.CreateGoalCycle(user, input)
	respond(c, http.StatusCreated, response, err)
}

// createGoal godoc
// @Summary 创建经营目标
// @Tags studio
// @Accept json
// @Produce json
// @Param id path string true "目标周期 UUID"
// @Param input body CreateStudioGoalInput true "经营目标"
// @Success 201 {object} StudioGoal
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 409 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Router /api/v1/studio/goals/cycles/{id}/goals [post]
func (h *Handler) createGoal(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	cycleID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var input CreateStudioGoalInput
	if !bindJSON(c, &input) {
		return
	}
	response, err := h.service.CreateGoal(user, cycleID, input)
	respond(c, http.StatusCreated, response, err)
}

// updateGoal godoc
// @Summary 更新经营目标
// @Tags studio
// @Accept json
// @Produce json
// @Param id path string true "目标 UUID"
// @Param input body UpdateStudioGoalInput true "经营目标"
// @Success 200 {object} StudioGoal
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 409 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Router /api/v1/studio/goals/{id} [patch]
func (h *Handler) updateGoal(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	goalID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var input UpdateStudioGoalInput
	if !bindJSON(c, &input) {
		return
	}
	response, err := h.service.UpdateGoal(user, goalID, input)
	respond(c, http.StatusOK, response, err)
}

// createGoalAction godoc
// @Summary 创建目标行动
// @Tags studio
// @Accept json
// @Produce json
// @Param id path string true "目标 UUID"
// @Param input body CreateStudioGoalActionInput true "目标行动"
// @Success 201 {object} StudioGoalAction
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 409 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Router /api/v1/studio/goals/{id}/actions [post]
func (h *Handler) createGoalAction(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	goalID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var input CreateStudioGoalActionInput
	if !bindJSON(c, &input) {
		return
	}
	response, err := h.service.CreateGoalAction(user, goalID, input)
	respond(c, http.StatusCreated, response, err)
}

// updateGoalAction godoc
// @Summary 更新目标行动
// @Tags studio
// @Accept json
// @Produce json
// @Param id path string true "行动 UUID"
// @Param input body UpdateStudioGoalActionInput true "目标行动"
// @Success 200 {object} StudioGoalAction
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 409 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Router /api/v1/studio/goals/actions/{id} [patch]
func (h *Handler) updateGoalAction(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	actionID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var input UpdateStudioGoalActionInput
	if !bindJSON(c, &input) {
		return
	}
	response, err := h.service.UpdateGoalAction(user, actionID, input)
	respond(c, http.StatusOK, response, err)
}

// deleteGoalAction godoc
// @Summary 删除目标行动
// @Tags studio
// @Produce json
// @Param id path string true "行动 UUID"
// @Success 200 {object} map[string]bool
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 409 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Router /api/v1/studio/goals/actions/{id} [delete]
func (h *Handler) deleteGoalAction(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	actionID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteGoalAction(user, actionID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}

// reviewGoalCycle godoc
// @Summary 提交经营目标周期复盘
// @Tags studio
// @Accept json
// @Produce json
// @Param id path string true "目标周期 UUID"
// @Param input body CreateStudioGoalReviewInput true "周期复盘"
// @Success 201 {object} StudioGoalCycle
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 409 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Router /api/v1/studio/goals/cycles/{id}/review [post]
func (h *Handler) reviewGoalCycle(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	cycleID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var input CreateStudioGoalReviewInput
	if !bindJSON(c, &input) {
		return
	}
	response, err := h.service.ReviewGoalCycle(user, cycleID, input)
	respond(c, http.StatusCreated, response, err)
}
