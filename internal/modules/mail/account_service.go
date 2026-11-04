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

// AccountService reserves addresses without provisioning mailboxes. The feature
// gate defaults to false and may be enabled only for invitation testing.
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
	Address   string    `json:"address"`
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
	local, err := validateLocalPart(in.LocalPart)
	if err != nil {
		return IssuedInvite{}, err
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
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var activeUsers int64
		if err := tx.Model(&model.User{}).Where("uuid = ? AND is_active = ?", in.TargetUserID, true).Count(&activeUsers).Error; err != nil {
			return err
		}
		if activeUsers != 1 {
			return apperr.NotFound("mail.user_not_found", "Active Atoman user not found")
		}
		var usedAddresses int64
		if err := tx.Model(&model.MailAccount{}).Where("user_id = ? OR local_part = ?", in.TargetUserID, local).Count(&usedAddresses).Error; err != nil {
			return err
		}
		if usedAddresses > 0 {
			return apperr.Conflict("mail.address_unavailable", "User or address already has a mailbox reservation")
		}
		var outstanding int64
		if err := tx.Model(&model.MailInvite{}).
			Where("(target_user_id = ? OR local_part = ?) AND used_at IS NULL AND revoked_at IS NULL AND expires_at > ?", in.TargetUserID, local, issuedAt).
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
	return IssuedInvite{ID: invite.ID, Address: local + "@" + MailDomain, Token: token, ExpiresAt: invite.ExpiresAt}, nil
}

type ReserveInput struct {
	Token string `json:"token"`
}

func (s *AccountService) RedeemInvite(user authctx.CurrentUser, token string) (AccountInfo, error) {
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
		account = model.MailAccount{
			ID: uuid.New(), UserID: user.ID, LocalPart: invite.LocalPart,
			Address: invite.LocalPart + "@" + MailDomain, Status: "reserved",
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
