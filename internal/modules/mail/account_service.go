package mail

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const MailDomain = "atoman.org"

var (
	localPartPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,30}[a-z0-9]$`)
	reservedLocalParts = map[string]struct{}{
		"admin": {}, "administrator": {}, "abuse": {}, "billing": {}, "bounce": {},
		"bounces": {}, "contact": {}, "dmarc": {}, "help": {}, "hostmaster": {},
		"info": {}, "legal": {}, "mail": {}, "mailer-daemon": {}, "newsletter": {},
		"no-reply": {}, "noreply": {}, "postmaster": {}, "privacy": {},
		"reports": {}, "root": {}, "security": {}, "service": {}, "smtp": {},
		"staff": {}, "support": {}, "system": {}, "team": {}, "test": {},
		"webmaster": {}, "www": {}, "imap": {},
	}
)

// AccountService reserves addresses without provisioning mailboxes. Invitations
// require an operator switch; administrators may reserve only their own address.
type AccountService struct {
	db             *gorm.DB
	invitesEnabled bool
	now            func() time.Time
}

func NewAccountService(db *gorm.DB, invitesEnabled bool) *AccountService {
	return &AccountService{db: db, invitesEnabled: invitesEnabled, now: time.Now}
}

func (s *AccountService) requireInvitesEnabled() error {
	if !s.invitesEnabled {
		return apperr.New(503, "mail.not_ready", "Mail invitations are not enabled", nil)
	}
	if s.db == nil {
		return apperr.New(503, "mail.storage_unavailable", "Mail account storage is not available", nil)
	}
	return nil
}

func validateLocalPart(raw string) (string, error) {
	local := strings.ToLower(strings.TrimSpace(raw))
	if !localPartPattern.MatchString(local) || strings.Contains(local, "..") {
		return "", apperr.BadRequest("mail.invalid_address", "Mail local part must be 3-32 ASCII characters")
	}
	if _, reserved := reservedLocalParts[local]; reserved {
		return "", apperr.BadRequest("mail.reserved_address", "Mail address is reserved")
	}
	return local, nil
}

type AccountInfo struct {
	Available bool   `json:"available"`
	Status    string `json:"status"`
	Address   string `json:"address,omitempty"`
}

func (s *AccountService) Me(user authctx.CurrentUser) (AccountInfo, error) {
	if user.ID == uuid.Nil {
		return AccountInfo{}, apperr.Unauthorized("Login required")
	}
	// This is never a readiness assertion for actual mail delivery. Stalwart,
	// R2 and backup recovery still require separate operational verification.
	result := AccountInfo{Available: false, Status: "not_invited"}
	if s.db == nil {
		result.Status = "not_ready"
		return result, nil
	}
	var account model.MailAccount
	err := s.db.Where("user_id = ?", user.ID).Take(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return result, nil
	}
	if err != nil {
		return AccountInfo{}, apperr.Internal(err)
	}
	result.Status = account.Status
	result.Address = account.Address
	result.Available = account.Status == "active"
	return result, nil
}

type IssueInviteInput struct {
	TargetUserID uuid.UUID `json:"target_user_id"`
	LocalPart    string    `json:"local_part"`
}

type IssuedInvite struct {
	ID        uuid.UUID `json:"id"`
	Address   string    `json:"address,omitempty"`
	Token     string    `json:"token"` // only returned once to authorized admin
	ExpiresAt time.Time `json:"expires_at"`
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *AccountService) IssueInvite(admin authctx.CurrentUser, in IssueInviteInput) (IssuedInvite, error) {
	if !authctx.RoleAtLeast(admin.Role, authctx.RoleAdmin) || admin.ID == uuid.Nil {
		return IssuedInvite{}, apperr.Forbidden("mail.admin_required", "Administrator access required")
	}
	if err := s.requireInvitesEnabled(); err != nil {
		return IssuedInvite{}, err
	}
	// A new invitation normally grants the target user the right to choose
	// their own local part. A named invite remains supported for compatibility.
	local := ""
	if strings.TrimSpace(in.LocalPart) != "" {
		var err error
		local, err = validateLocalPart(in.LocalPart)
		if err != nil {
			return IssuedInvite{}, err
		}
	}
	if in.TargetUserID == uuid.Nil {
		return IssuedInvite{}, apperr.BadRequest("mail.target_required", "Valid Atoman user is required")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return IssuedInvite{}, apperr.Internal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	issuedAt := s.now().UTC()
	invite := model.MailInvite{
		ID: uuid.New(), TokenHash: tokenHash(token), TargetUserID: in.TargetUserID,
		CreatedBy: admin.ID, LocalPart: local, ExpiresAt: issuedAt.Add(7 * 24 * time.Hour),
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var activeUsers int64
		if err := tx.Model(&model.User{}).Where("uuid = ? AND is_active = ?", in.TargetUserID, true).Count(&activeUsers).Error; err != nil {
			return err
		}
		if activeUsers != 1 {
			return apperr.NotFound("mail.user_not_found", "Active Atoman user not found")
		}
		var usedAddresses int64
		accountCheck := tx.Model(&model.MailAccount{}).Where("user_id = ?", in.TargetUserID)
		if local != "" {
			accountCheck = accountCheck.Or("local_part = ?", local)
		}
		if err := accountCheck.Count(&usedAddresses).Error; err != nil {
			return err
		}
		if usedAddresses > 0 {
			return apperr.Conflict("mail.address_unavailable", "User or address already has a mailbox reservation")
		}
		var outstanding int64
		inviteCheck := tx.Model(&model.MailInvite{}).Where("target_user_id = ?", in.TargetUserID)
		if local != "" {
			inviteCheck = inviteCheck.Or("local_part = ?", local)
		}
		if err := inviteCheck.Where("used_at IS NULL AND revoked_at IS NULL AND expires_at > ?", issuedAt).
			Count(&outstanding).Error; err != nil {
			return err
		}
		if outstanding > 0 {
			return apperr.Conflict("mail.invite_pending", "An unexpired invitation already exists")
		}
		return tx.Create(&invite).Error
	})
	if err != nil {
		return IssuedInvite{}, translateDBError(err)
	}
	issued := IssuedInvite{ID: invite.ID, Token: token, ExpiresAt: invite.ExpiresAt}
	if local != "" {
		issued.Address = local + "@" + MailDomain
	}
	return issued, nil
}

type ReserveInput struct {
	Token     string `json:"token"`
	LocalPart string `json:"local_part"`
}

type ReserveSelfInput struct {
	LocalPart string `json:"local_part"`
}

// ReserveSelf lets an Atoman administrator select their own permanent email
// address without an invitation. This does NOT create a Stalwart mailbox.
func (s *AccountService) ReserveSelf(user authctx.CurrentUser, rawLocal string) (AccountInfo, error) {
	if user.ID == uuid.Nil || !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return AccountInfo{}, apperr.Forbidden("mail.admin_required", "Administrator access required")
	}
	if s == nil || s.db == nil {
		return AccountInfo{}, apperr.New(503, "mail.storage_unavailable", "Mail account storage is not available", nil)
	}
	local, err := validateLocalPart(rawLocal)
	if err != nil {
		return AccountInfo{}, err
	}
	var account model.MailAccount
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.User{}).Where("uuid = ? AND is_active = ?", user.ID, true).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return apperr.Forbidden("mail.user_inactive", "Active Atoman account required")
		}
		if err := tx.Model(&model.MailInvite{}).
			Where("local_part = ? AND target_user_id <> ? AND used_at IS NULL AND revoked_at IS NULL AND expires_at > ?", local, user.ID, s.now().UTC()).
			Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return apperr.Conflict("mail.invite_pending", "An invitation already reserves this address")
		}
		account = model.MailAccount{ID: uuid.New(), UserID: user.ID, LocalPart: local, Address: local + "@" + MailDomain, Status: "reserved"}
		return tx.Create(&account).Error
	})
	if err != nil {
		return AccountInfo{}, translateDBError(err)
	}
	return AccountInfo{Available: false, Status: "reserved", Address: account.Address}, nil
}

// Legacy callers may redeem preassigned addresses without a local part.
func (s *AccountService) RedeemInvite(user authctx.CurrentUser, token string) (AccountInfo, error) {
	return s.RedeemInviteWithLocalPart(user, token, "")
}

func (s *AccountService) RedeemInviteWithLocalPart(user authctx.CurrentUser, token, chosenLocal string) (AccountInfo, error) {
	if user.ID == uuid.Nil {
		return AccountInfo{}, apperr.Unauthorized("Login required")
	}
	if err := s.requireInvitesEnabled(); err != nil {
		return AccountInfo{}, err
	}
	if len(token) != 43 || strings.Trim(token, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
		return AccountInfo{}, apperr.BadRequest("mail.invalid_invite", "Invalid invitation token")
	}
	var account model.MailAccount
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var invite model.MailInvite
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("token_hash = ?", tokenHash(token)).Take(&invite).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("mail.invalid_invite", "Invitation is invalid or expired")
			}
			return err
		}
		now := s.now().UTC()
		if invite.TargetUserID != user.ID {
			return apperr.Forbidden("mail.invite_owner_required", "Invitation belongs to another user")
		}
		if invite.UsedAt != nil || invite.RevokedAt != nil || !invite.ExpiresAt.After(now) {
			return apperr.Conflict("mail.invite_unavailable", "Invitation is already used or expired")
		}
		var userRecord model.User
		if err := tx.Where("uuid = ? AND is_active = ?", user.ID, true).Take(&userRecord).Error; err != nil {
			return apperr.Forbidden("mail.user_inactive", "Active Atoman account required")
		}
		// Custom address wins over an optional preselected address. Address is
		// immutable after reservation and never coupled to the site username.
		selected := invite.LocalPart
		if strings.TrimSpace(chosenLocal) != "" {
			selected = chosenLocal
		}
		if selected == "" {
			return apperr.BadRequest("mail.address_required", "Choose a mailbox name")
		}
		local, err := validateLocalPart(selected)
		if err != nil {
			return err
		}
		var otherPending int64
		if err := tx.Model(&model.MailInvite{}).
			Where("id <> ? AND local_part = ? AND target_user_id <> ? AND used_at IS NULL AND revoked_at IS NULL AND expires_at > ?", invite.ID, local, user.ID, now).
			Count(&otherPending).Error; err != nil {
			return err
		}
		if otherPending > 0 {
			return apperr.Conflict("mail.invite_pending", "An invitation already reserves this address")
		}
		account = model.MailAccount{
			ID: uuid.New(), UserID: user.ID, LocalPart: local,
			Address: local + "@" + MailDomain, Status: "reserved",
		}
		if err := tx.Create(&account).Error; err != nil {
			return err
		}
		return tx.Model(&invite).Update("used_at", now).Error
	})
	if err != nil {
		return AccountInfo{}, translateDBError(err)
	}
	// Reserved is deliberately not active: it cannot receive or send messages
	// until a separate authenticated Stalwart provisioner confirms success.
	return AccountInfo{Available: false, Status: "reserved", Address: account.Address}, nil
}

func translateDBError(err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return apperr.Conflict("mail.address_unavailable", "Mailbox address or user already reserved")
	}
	var appError *apperr.AppError
	if errors.As(err, &appError) {
		return appError
	}
	return apperr.Internal(fmt.Errorf("mail account storage: %w", err))
}
