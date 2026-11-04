package mail

import (
	"net/http"
	"os"
	"strings"

	"atoman/internal/middleware"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type AccountHandler struct {
	service *AccountService
}

// RegisterAccountRoutes adds site-session-authenticated invitation and
// reservation endpoints. No endpoint provisions a real Stalwart mailbox.
func RegisterAccountRoutes(group *gin.RouterGroup, db *gorm.DB) {
	enabled := strings.TrimSpace(os.Getenv("ATOMAN_MAIL_INVITES_ENABLED")) == "1"
	RegisterAccountRoutesWithService(group, NewAccountService(db, enabled))
}

func RegisterAccountRoutesWithService(group *gin.RouterGroup, service *AccountService) {
	h := &AccountHandler{service: service}
	group.Use(middleware.AuthMiddleware())
	group.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "private, no-store")
		c.Next()
	})
	group.GET("/me", h.me)
	group.POST("/activate", h.activate)
	group.POST("/admin/invites", h.issueInvite)
}

// me godoc
// @Summary Current Atoman mail reservation (not mail service readiness)
// @Tags mail
// @Produce json
// @Success 200 {object} AccountInfo
// @Failure 401 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/mail/me [get]
func (h *AccountHandler) me(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	info, err := h.service.Me(user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, info)
}

// activate godoc
// @Summary Redeem invitation, reserve address only (does not provision mail)
// @Tags mail
// @Accept json
// @Produce json
// @Param input body ReserveInput true "Single-use invite token"
// @Success 201 {object} AccountInfo
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 409 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/mail/activate [post]
func (h *AccountHandler) activate(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var input ReserveInput
	if !httpx.BindRequiredJSON(c, &input) {
		return
	}
	info, err := h.service.RedeemInvite(user, input.Token)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, info)
}

// issueInvite godoc
// @Summary Admin creates a user-bound, one-time mail address invitation
// @Tags mail
// @Accept json
// @Produce json
// @Param input body IssueInviteInput true "Target Atoman user and proposed mail local part"
// @Success 201 {object} IssuedInvite
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 409 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/mail/admin/invites [post]
func (h *AccountHandler) issueInvite(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var input IssueInviteInput
	if !httpx.BindRequiredJSON(c, &input) {
		return
	}
	issued, err := h.service.IssueInvite(user, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, issued)
}
