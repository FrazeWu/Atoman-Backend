package mail

import (
	"net/http"
	"os"
	"strconv"
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
	read    *MailReadService
}

// RegisterAccountRoutes adds site-session-authenticated invitation and
// reservation endpoints. No endpoint provisions a real Stalwart mailbox.
func RegisterAccountRoutes(group *gin.RouterGroup, db *gorm.DB) {
	enabled := strings.TrimSpace(os.Getenv("ATOMAN_MAIL_INVITES_ENABLED")) == "1"
	var read *MailReadService
	if os.Getenv("ATOMAN_MAIL_READ_ENABLED") == "1" {
		client, clientErr := NewStalwartJMAPClient(os.Getenv("ATOMAN_MAIL_STALWART_URL"), nil)
		vault, vaultErr := NewMailCredentialVault(os.Getenv("ATOMAN_MAIL_JMAP_ENCRYPTION_KEY_B64"))
		if clientErr == nil && vaultErr == nil {
			read = NewMailReadService(db, client, vault, true)
		}
	}
	RegisterAccountRoutesWithServices(group, NewAccountService(db, enabled), read)
}

func RegisterAccountRoutesWithService(group *gin.RouterGroup, service *AccountService) {
	RegisterAccountRoutesWithServices(group, service, nil)
}

func RegisterAccountRoutesWithServices(group *gin.RouterGroup, service *AccountService, read *MailReadService) {
	h := &AccountHandler{service: service, read: read}
	group.Use(middleware.AuthMiddleware())
	group.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "private, no-store")
		c.Next()
	})
	group.GET("/me", h.me)
	group.GET("/folders", h.folders)
	group.GET("/messages", h.messages)
	group.POST("/activate", h.activate)
	group.POST("/admin/invites", h.issueInvite)
	group.POST("/admin/reserve-self", h.reserveSelf)
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
// @Summary Redeem invitation using a custom mail address (reservation only)
// @Tags mail
// @Accept json
// @Produce json
// @Param input body ReserveInput true "Single-use invite and chosen mailbox local part"
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
	info, err := h.service.RedeemInviteWithLocalPart(user, input.Token, input.LocalPart)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, info)
}

// issueInvite godoc
// @Summary Admin issues a single-use mail invite; target chooses their address
// @Tags mail
// @Accept json
// @Produce json
// @Param input body IssueInviteInput true "Target Atoman user; optional preselected mail local part"
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

// folders godoc
// @Summary List own Stalwart mail folders (requires active mailbox and encrypted user key)
// @Tags mail
// @Produce json
// @Success 200 {array} MailboxInfo
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/mail/folders [get]
func (h *AccountHandler) folders(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	if h.read == nil {
		httpx.Error(c, apperr.New(503, "mail.read_not_ready", "Mail reading is not enabled", nil))
		return
	}
	folders, err := h.read.Folders(c.Request.Context(), user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, folders)
}

// messages godoc
// @Summary List own mail messages in an accessible folder
// @Tags mail
// @Produce json
// @Param mailbox_id query string true "Mailbox ID from /mail/folders"
// @Param limit query int false "Page size (1-100)"
// @Success 200 {array} EmailSummary
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/mail/messages [get]
func (h *AccountHandler) messages(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	if h.read == nil {
		httpx.Error(c, apperr.New(503, "mail.read_not_ready", "Mail reading is not enabled", nil))
		return
	}
	limit := 25
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			httpx.Error(c, apperr.BadRequest("mail.invalid_query", "Mail limit must be between 1 and 100"))
			return
		}
		limit = parsed
	}
	messages, err := h.read.Messages(c.Request.Context(), user, c.Query("mailbox_id"), limit)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, messages)
}

// reserveSelf godoc
// @Summary Administrator reserves their own chosen mailbox address without an invitation
// @Description Only reserves the address. Does not create or activate a Stalwart mailbox.
// @Tags mail
// @Accept json
// @Produce json
// @Param input body ReserveSelfInput true "Chosen mailbox local part"
// @Success 201 {object} AccountInfo
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 409 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/mail/admin/reserve-self [post]
func (h *AccountHandler) reserveSelf(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var input ReserveSelfInput
	if !httpx.BindRequiredJSON(c, &input) {
		return
	}
	result, err := h.service.ReserveSelf(user, input.LocalPart)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, result)
}
