package mail

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AdminMailAccountSummary struct {
	ID          uuid.UUID `json:"id"`
	UserID      uuid.UUID `json:"user_id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	UserEmail   string    `json:"user_email"`
	LocalPart   string    `json:"local_part"`
	Address     string    `json:"address"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type AdminMailInviteSummary struct {
	ID           uuid.UUID  `json:"id"`
	TargetUserID uuid.UUID  `json:"target_user_id"`
	Username     string     `json:"username"`
	DisplayName  string     `json:"display_name"`
	UserEmail    string     `json:"user_email"`
	LocalPart    string     `json:"local_part"`
	Address      string     `json:"address,omitempty"`
	State        string     `json:"state"`
	ExpiresAt    time.Time  `json:"expires_at"`
	UsedAt       *time.Time `json:"used_at,omitempty"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

func requireMailAdmin(actor authctx.CurrentUser, db *gorm.DB) error {
	if actor.ID == uuid.Nil || !authctx.RoleAtLeast(actor.Role, authctx.RoleAdmin) {
		return apperr.Forbidden("mail.admin_required", "Administrator access required")
	}
	if db == nil {
		return apperr.New(503, "mail.storage_unavailable", "Mail account storage is not available", nil)
	}
	return nil
}

func (s *AccountService) loadAdminAccountSummary(accountID uuid.UUID) (AdminMailAccountSummary, error) {
	var row AdminMailAccountSummary
	err := s.db.Table("mail_accounts AS ma").
		Select(`ma.id, ma.user_id, COALESCE(u.username, '') AS username, COALESCE(u.display_name, '') AS display_name, COALESCE(u.email, '') AS user_email, ma.local_part, ma.address, ma.status, ma.created_at, ma.updated_at`).
		Joins(`LEFT JOIN "Users" AS u ON u.uuid = ma.user_id`).
		Where("ma.id = ?", accountID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return AdminMailAccountSummary{}, apperr.NotFound("mail.account_not_found", "Mail account not found")
	}
	if err != nil {
		return AdminMailAccountSummary{}, apperr.Internal(err)
	}
	return row, nil
}

func (s *AccountService) ListAdminAccounts(actor authctx.CurrentUser, statusFilter, query string) ([]AdminMailAccountSummary, error) {
	var db *gorm.DB
	if s != nil {
		db = s.db
	}
	if err := requireMailAdmin(actor, db); err != nil {
		return nil, err
	}
	tx := s.db.Table("mail_accounts AS ma").
		Select(`ma.id, ma.user_id, COALESCE(u.username, '') AS username, COALESCE(u.display_name, '') AS display_name, COALESCE(u.email, '') AS user_email, ma.local_part, ma.address, ma.status, ma.created_at, ma.updated_at`).
		Joins(`LEFT JOIN "Users" AS u ON u.uuid = ma.user_id`)

	switch strings.TrimSpace(statusFilter) {
	case "", "all":
	case "pending":
		tx = tx.Where("ma.status IN ?", []string{"reserved", "provisioning", "failed"})
	default:
		tx = tx.Where("ma.status = ?", strings.TrimSpace(statusFilter))
	}

	if q := strings.ToLower(strings.TrimSpace(query)); q != "" {
		like := "%" + q + "%"
		tx = tx.Where(`(LOWER(ma.address) LIKE ? OR LOWER(ma.local_part) LIKE ? OR LOWER(COALESCE(u.username, '')) LIKE ? OR LOWER(COALESCE(u.display_name, '')) LIKE ? OR LOWER(COALESCE(u.email, '')) LIKE ?)`, like, like, like, like, like)
	}

	var rows []AdminMailAccountSummary
	if err := tx.Order("CASE WHEN ma.status IN ('reserved', 'provisioning', 'failed') THEN 0 ELSE 1 END, ma.updated_at DESC").
		Limit(200).
		Scan(&rows).Error; err != nil {
		return nil, apperr.Internal(err)
	}
	if rows == nil {
		rows = []AdminMailAccountSummary{}
	}
	return rows, nil
}

func (s *AccountService) ApproveAdminAccount(ctx context.Context, actor authctx.CurrentUser, accountID uuid.UUID, provision *MailProvisionService, devApprove bool) (AdminMailAccountSummary, error) {
	var db *gorm.DB
	if s != nil {
		db = s.db
	}
	if err := requireMailAdmin(actor, db); err != nil {
		return AdminMailAccountSummary{}, err
	}
	var account model.MailAccount
	if err := s.db.Where("id = ?", accountID).Take(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return AdminMailAccountSummary{}, apperr.NotFound("mail.account_not_found", "Mail account not found")
		}
		return AdminMailAccountSummary{}, apperr.Internal(err)
	}
	if account.Status == "active" {
		return s.loadAdminAccountSummary(account.ID)
	}
	if provision != nil && account.Status == "reserved" {
		if _, err := provision.ProvisionReserved(ctx, actor, account.UserID); err != nil {
			return AdminMailAccountSummary{}, err
		}
		return s.loadAdminAccountSummary(account.ID)
	}
	if !devApprove {
		return AdminMailAccountSummary{}, apperr.New(503, "mail.provisioning_not_ready", "Mail provisioning is not enabled", nil)
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var locked model.MailAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", accountID).Take(&locked).Error; err != nil {
			return err
		}
		if locked.Status == "active" {
			return nil
		}
		if locked.Status != "reserved" && locked.Status != "provisioning" && locked.Status != "failed" {
			return apperr.Conflict("mail.not_approvable", "Mail account cannot be approved in current status")
		}
		return tx.Model(&locked).Updates(map[string]any{
			"status":     "active",
			"updated_at": s.now().UTC(),
		}).Error
	})
	if err != nil {
		return AdminMailAccountSummary{}, translateDBError(err)
	}
	return s.loadAdminAccountSummary(accountID)
}

func (s *AccountService) RejectAdminAccount(actor authctx.CurrentUser, accountID uuid.UUID) error {
	var db *gorm.DB
	if s != nil {
		db = s.db
	}
	if err := requireMailAdmin(actor, db); err != nil {
		return err
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var locked model.MailAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", accountID).Take(&locked).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("mail.account_not_found", "Mail account not found")
			}
			return err
		}
		if locked.Status == "active" || locked.Status == "suspended" {
			return apperr.Conflict("mail.cannot_reject_active", "Active or suspended mail accounts cannot be rejected")
		}
		return tx.Delete(&locked).Error
	})
	if err != nil {
		return translateDBError(err)
	}
	return nil
}

func (s *AccountService) SetAdminAccountSuspended(actor authctx.CurrentUser, accountID uuid.UUID, suspended bool) (AdminMailAccountSummary, error) {
	var db *gorm.DB
	if s != nil {
		db = s.db
	}
	if err := requireMailAdmin(actor, db); err != nil {
		return AdminMailAccountSummary{}, err
	}
	targetStatus := "active"
	requiredStatus := "suspended"
	if suspended {
		targetStatus = "suspended"
		requiredStatus = "active"
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var locked model.MailAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", accountID).Take(&locked).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("mail.account_not_found", "Mail account not found")
			}
			return err
		}
		if locked.Status == targetStatus {
			return nil
		}
		if locked.Status != requiredStatus {
			return apperr.Conflict("mail.invalid_status_transition", fmt.Sprintf("Cannot change mail account from %s to %s", locked.Status, targetStatus))
		}
		return tx.Model(&locked).Updates(map[string]any{
			"status":     targetStatus,
			"updated_at": s.now().UTC(),
		}).Error
	})
	if err != nil {
		return AdminMailAccountSummary{}, translateDBError(err)
	}
	return s.loadAdminAccountSummary(accountID)
}

func computeInviteState(usedAt, revokedAt *time.Time, expiresAt, now time.Time) string {
	if usedAt != nil {
		return "used"
	}
	if revokedAt != nil {
		return "revoked"
	}
	if !expiresAt.After(now) {
		return "expired"
	}
	return "pending"
}

func (s *AccountService) ListAdminInvites(actor authctx.CurrentUser) ([]AdminMailInviteSummary, error) {
	var db *gorm.DB
	if s != nil {
		db = s.db
	}
	if err := requireMailAdmin(actor, db); err != nil {
		return nil, err
	}
	type inviteRow struct {
		ID           uuid.UUID  `gorm:"column:id"`
		TargetUserID uuid.UUID  `gorm:"column:target_user_id"`
		Username     string     `gorm:"column:username"`
		DisplayName  string     `gorm:"column:display_name"`
		UserEmail    string     `gorm:"column:user_email"`
		LocalPart    string     `gorm:"column:local_part"`
		ExpiresAt    time.Time  `gorm:"column:expires_at"`
		UsedAt       *time.Time `gorm:"column:used_at"`
		RevokedAt    *time.Time `gorm:"column:revoked_at"`
		CreatedAt    time.Time  `gorm:"column:created_at"`
	}
	var rows []inviteRow
	if err := s.db.Table("mail_invites AS mi").
		Select(`mi.id, mi.target_user_id, COALESCE(u.username, '') AS username, COALESCE(u.display_name, '') AS display_name, COALESCE(u.email, '') AS user_email, mi.local_part, mi.expires_at, mi.used_at, mi.revoked_at, mi.created_at`).
		Joins(`LEFT JOIN "Users" AS u ON u.uuid = mi.target_user_id`).
		Order("mi.created_at DESC").
		Limit(200).
		Scan(&rows).Error; err != nil {
		return nil, apperr.Internal(err)
	}
	now := s.now().UTC()
	out := make([]AdminMailInviteSummary, 0, len(rows))
	for _, r := range rows {
		addr := ""
		if strings.TrimSpace(r.LocalPart) != "" {
			addr = r.LocalPart + "@" + MailDomain
		}
		out = append(out, AdminMailInviteSummary{
			ID:           r.ID,
			TargetUserID: r.TargetUserID,
			Username:     r.Username,
			DisplayName:  r.DisplayName,
			UserEmail:    r.UserEmail,
			LocalPart:    r.LocalPart,
			Address:      addr,
			State:        computeInviteState(r.UsedAt, r.RevokedAt, r.ExpiresAt, now),
			ExpiresAt:    r.ExpiresAt,
			UsedAt:       r.UsedAt,
			RevokedAt:    r.RevokedAt,
			CreatedAt:    r.CreatedAt,
		})
	}
	return out, nil
}

func (s *AccountService) RevokeAdminInvite(actor authctx.CurrentUser, inviteID uuid.UUID) (AdminMailInviteSummary, error) {
	var db *gorm.DB
	if s != nil {
		db = s.db
	}
	if err := requireMailAdmin(actor, db); err != nil {
		return AdminMailInviteSummary{}, err
	}
	now := s.now().UTC()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var invite model.MailInvite
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", inviteID).Take(&invite).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("mail.invite_not_found", "Mail invitation not found")
			}
			return err
		}
		if invite.UsedAt != nil {
			return apperr.Conflict("mail.invite_used", "Used invitation cannot be revoked")
		}
		if invite.RevokedAt != nil {
			return nil
		}
		return tx.Model(&invite).Update("revoked_at", now).Error
	})
	if err != nil {
		return AdminMailInviteSummary{}, translateDBError(err)
	}
	invites, err := s.ListAdminInvites(actor)
	if err != nil {
		return AdminMailInviteSummary{}, err
	}
	for _, item := range invites {
		if item.ID == inviteID {
			return item, nil
		}
	}
	return AdminMailInviteSummary{}, apperr.NotFound("mail.invite_not_found", "Mail invitation not found")
}

// listAdminAccounts godoc
// @Summary Admin lists all reserved and active mail accounts
// @Tags mail
// @Produce json
// @Param status query string false "Filter by account status (all, pending, reserved, active, suspended, failed)"
// @Param q query string false "Search username or mail address"
// @Success 200 {array} AdminMailAccountSummary
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/mail/admin/accounts [get]
func (h *AccountHandler) listAdminAccounts(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	rows, err := h.service.ListAdminAccounts(user, c.Query("status"), c.Query("q"))
	mailResult(c, http.StatusOK, rows, err)
}

// approveAdminAccount godoc
// @Summary Admin approves a reserved mail account for provisioning or activation
// @Tags mail
// @Produce json
// @Param id path string true "Mail account UUID"
// @Success 200 {object} AdminMailAccountSummary
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/mail/admin/accounts/{id}/approve [post]
func (h *AccountHandler) approveAdminAccount(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	row, err := h.service.ApproveAdminAccount(c.Request.Context(), user, id, h.provision, h.devMode)
	mailResult(c, http.StatusOK, row, err)
}

// rejectAdminAccount godoc
// @Summary Admin rejects and removes an unactivated mail account reservation
// @Tags mail
// @Produce json
// @Param id path string true "Mail account UUID"
// @Success 200 {object} map[string]bool
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/mail/admin/accounts/{id}/reject [post]
func (h *AccountHandler) rejectAdminAccount(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	err := h.service.RejectAdminAccount(user, id)
	mailResult(c, http.StatusOK, gin.H{"rejected": err == nil}, err)
}

// suspendAdminAccount godoc
// @Summary Admin suspends an active mail account
// @Tags mail
// @Produce json
// @Param id path string true "Mail account UUID"
// @Success 200 {object} AdminMailAccountSummary
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/mail/admin/accounts/{id}/suspend [post]
func (h *AccountHandler) suspendAdminAccount(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	row, err := h.service.SetAdminAccountSuspended(user, id, true)
	mailResult(c, http.StatusOK, row, err)
}

// restoreAdminAccount godoc
// @Summary Admin restores a suspended mail account
// @Tags mail
// @Produce json
// @Param id path string true "Mail account UUID"
// @Success 200 {object} AdminMailAccountSummary
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/mail/admin/accounts/{id}/restore [post]
func (h *AccountHandler) restoreAdminAccount(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	row, err := h.service.SetAdminAccountSuspended(user, id, false)
	mailResult(c, http.StatusOK, row, err)
}

// listAdminInvites godoc
// @Summary Admin lists issued mail invitations
// @Tags mail
// @Produce json
// @Success 200 {array} AdminMailInviteSummary
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/mail/admin/invites [get]
func (h *AccountHandler) listAdminInvites(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	rows, err := h.service.ListAdminInvites(user)
	mailResult(c, http.StatusOK, rows, err)
}

// revokeAdminInvite godoc
// @Summary Admin revokes an unused mail invitation
// @Tags mail
// @Produce json
// @Param id path string true "Mail invite UUID"
// @Success 200 {object} AdminMailInviteSummary
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/mail/admin/invites/{id}/revoke [post]
func (h *AccountHandler) revokeAdminInvite(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	row, err := h.service.RevokeAdminInvite(user, id)
	mailResult(c, http.StatusOK, row, err)
}
