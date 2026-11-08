package mail

import (
	"context"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const maxMailAliases = 5

type MailAliasInfo struct {
	ID        uuid.UUID `json:"id"`
	Address   string    `json:"address"`
	Status    string    `json:"status"`
	ErrorCode string    `json:"error_code,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func mailAliasInfo(row model.MailAlias) MailAliasInfo {
	return MailAliasInfo{ID: row.ID, Address: row.Address, Status: row.Status, ErrorCode: row.ErrorCode, CreatedAt: row.CreatedAt}
}

type MailAliasService struct {
	db     *gorm.DB
	remote MailAliasRemote
	domain MailDomainLocator
	admin  StalwartCredential
	gates  MailActivationGates
}
type MailDomainLocator interface {
	FindDomainID(context.Context, StalwartCredential, string) (string, error)
}

func NewMailAliasService(db *gorm.DB, remote MailAliasRemote, domain MailDomainLocator, admin StalwartCredential, gates MailActivationGates) *MailAliasService {
	return &MailAliasService{db: db, remote: remote, domain: domain, admin: admin, gates: gates}
}
func (s *MailAliasService) ready() bool {
	return s != nil && s.db != nil && s.remote != nil && s.domain != nil && s.gates.ready()
}

// The three address-reservation flows and alias reservation use one global
// advisory lock before checking cross-table uniqueness. Stalwart still makes
// the authoritative remote uniqueness decision and can fail closed.
func lockMailAddressSpace(tx *gorm.DB) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "mail-address-space:atoman.org").Error
}
func checkMailAliasAddress(tx *gorm.DB, localPart string) error {
	if !tx.Migrator().HasTable(&model.MailAlias{}) {
		return nil
	}
	var count int64
	if err := tx.Model(&model.MailAlias{}).Where("local_part=?", localPart).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return apperr.Conflict("mail.address_unavailable", "Address is already reserved as an alias")
	}
	return nil
}
func (s *MailAliasService) owner(ctx context.Context, u authctx.CurrentUser) (model.MailAccount, error) {
	var account model.MailAccount
	if u.ID == uuid.Nil {
		return account, apperr.Unauthorized("Login required")
	}
	if !s.ready() {
		return account, apperr.New(503, "mail.aliases_disabled", "Mail aliases are not enabled", nil)
	}
	if err := s.db.WithContext(ctx).Where("user_id=? AND status=?", u.ID, "active").Take(&account).Error; err != nil || account.StalwartAccountID == nil || !validJMAPScopedID(*account.StalwartAccountID) {
		return account, apperr.Forbidden("mail.mailbox_inactive", "Active mailbox required")
	}
	var active int64
	if err := s.db.WithContext(ctx).Model(&model.User{}).Where("uuid=? AND is_active=?", u.ID, true).Count(&active).Error; err != nil {
		return account, apperr.Internal(err)
	}
	if active != 1 {
		return account, apperr.Forbidden("mail.account_inactive", "Active user required")
	}
	return account, nil
}
func (s *MailAliasService) List(ctx context.Context, u authctx.CurrentUser) ([]MailAliasInfo, error) {
	if _, err := s.owner(ctx, u); err != nil {
		return nil, err
	}
	var rows []model.MailAlias
	if err := s.db.WithContext(ctx).Where("user_id=?", u.ID).Order("created_at ASC").Limit(maxMailAliases + 1).Find(&rows).Error; err != nil {
		return nil, apperr.Internal(err)
	}
	if len(rows) > maxMailAliases {
		return nil, apperr.New(503, "mail.alias_limit", "Alias limit exceeded", nil)
	}
	out := make([]MailAliasInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, mailAliasInfo(row))
	}
	return out, nil
}
func (s *MailAliasService) Create(ctx context.Context, u authctx.CurrentUser, requested string) (MailAliasInfo, error) {
	account, err := s.owner(ctx, u)
	if err != nil {
		return MailAliasInfo{}, err
	}
	local, err := validateLocalPart(requested)
	if err != nil {
		return MailAliasInfo{}, err
	}
	now := time.Now().UTC()
	row := model.MailAlias{ID: uuid.New(), UserID: u.ID, LocalPart: local, Address: local + "@" + MailDomain, StalwartAccountID: *account.StalwartAccountID, Status: "needs_review", ErrorCode: "pending_remote_confirmation", CreatedAt: now, UpdatedAt: now}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := lockMailAddressSpace(tx); e != nil {
			return e
		}
		var current model.MailAccount
		if e := tx.Where("user_id=? AND status=? AND stalwart_account_id=?", u.ID, "active", *account.StalwartAccountID).Take(&current).Error; e != nil {
			return apperr.Conflict("mail.mailbox_changed", "Active mailbox mapping changed")
		}
		var count int64
		if e := tx.Model(&model.MailAlias{}).Where("user_id=?", u.ID).Count(&count).Error; e != nil {
			return e
		}
		if count >= maxMailAliases {
			return apperr.Conflict("mail.alias_limit", "Alias limit reached")
		}
		if e := tx.Model(&model.MailAccount{}).Where("local_part=?", local).Count(&count).Error; e != nil {
			return e
		}
		if count != 0 {
			return apperr.Conflict("mail.address_unavailable", "Address is already assigned")
		}
		if e := checkMailAliasAddress(tx, local); e != nil {
			return e
		}
		if e := tx.Model(&model.MailInvite{}).Where("local_part=? AND used_at IS NULL AND revoked_at IS NULL AND expires_at>?", local, now).Count(&count).Error; e != nil {
			return e
		}
		if count != 0 {
			return apperr.Conflict("mail.invite_pending", "Address is reserved by an invitation")
		}
		return tx.Create(&row).Error
	})
	if err != nil {
		return MailAliasInfo{}, translateDBError(err)
	}
	domainID, err := s.domain.FindDomainID(ctx, s.admin, MailDomain)
	if err == nil {
		var slot uint32
		slot, err = s.remote.AddAlias(ctx, s.admin, *account.StalwartAccountID, domainID, local)
		if err == nil {
			row.Slot = slot
		}
	}
	if err != nil || !validJMAPScopedID(domainID) {
		_ = s.db.Model(&model.MailAlias{}).Where("id=? AND user_id=? AND status=?", row.ID, u.ID, "needs_review").Update("error_code", "remote_change_unknown").Error
		return mailAliasInfo(row), apperr.New(503, "mail.alias_review_required", "Alias operation requires administrator reconciliation", nil)
	}
	changed := s.db.WithContext(ctx).Model(&model.MailAlias{}).Where("id=? AND user_id=? AND status=?", row.ID, u.ID, "needs_review").Updates(map[string]any{"status": "active", "slot": row.Slot, "error_code": "", "updated_at": time.Now().UTC()})
	if changed.Error != nil || changed.RowsAffected != 1 {
		return mailAliasInfo(row), apperr.New(503, "mail.alias_review_required", "Alias confirmation requires reconciliation", nil)
	}
	row.Status = "active"
	row.ErrorCode = ""
	return mailAliasInfo(row), nil
}
func (s *MailAliasService) SetEnabled(ctx context.Context, u authctx.CurrentUser, aliasID uuid.UUID, enabled bool) (MailAliasInfo, error) {
	account, err := s.owner(ctx, u)
	if err != nil {
		return MailAliasInfo{}, err
	}
	var row model.MailAlias
	if err = s.db.WithContext(ctx).Where("id=? AND user_id=? AND stalwart_account_id=?", aliasID, u.ID, *account.StalwartAccountID).Take(&row).Error; err != nil {
		return MailAliasInfo{}, apperr.NotFound("mail.alias_missing", "Alias unavailable")
	}
	expected := "active"
	target := "disabled"
	if enabled {
		expected = "disabled"
		target = "active"
	}
	if row.Status != expected {
		return mailAliasInfo(row), apperr.Conflict("mail.alias_state_changed", "Alias is not in a mutable state")
	}
	if !enabled {
		var pending int64
		e := s.db.WithContext(ctx).Model(&model.MailPendingSend{}).Where("user_id=? AND alias_id=? AND status IN ?", u.ID, row.ID, []string{"queued", "dispatching", "uncertain"}).Count(&pending).Error
		if e != nil {
			return mailAliasInfo(row), apperr.Internal(e)
		}
		if pending != 0 {
			return mailAliasInfo(row), apperr.Conflict("mail.alias_pending_send", "Cancel or reconcile pending messages first")
		}
	}
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&model.MailAlias{}).Where("id=? AND user_id=? AND status=?", row.ID, u.ID, expected).Updates(map[string]any{"status": "needs_review", "error_code": "pending_remote_confirmation", "updated_at": now})
	if result.Error != nil {
		return mailAliasInfo(row), apperr.Internal(result.Error)
	}
	if result.RowsAffected != 1 {
		return mailAliasInfo(row), apperr.Conflict("mail.alias_state_changed", "Alias changed; refresh")
	}
	domainID, err := s.domain.FindDomainID(ctx, s.admin, MailDomain)
	if err == nil && validJMAPScopedID(domainID) {
		err = s.remote.SetAliasEnabled(ctx, s.admin, *account.StalwartAccountID, domainID, row.LocalPart, row.Slot, enabled)
	}
	if err != nil || !validJMAPScopedID(domainID) {
		_ = s.db.Model(&model.MailAlias{}).Where("id=? AND user_id=?", row.ID, u.ID).Update("error_code", "remote_change_unknown").Error
		return mailAliasInfo(row), apperr.New(503, "mail.alias_review_required", "Alias operation requires administrator reconciliation", nil)
	}
	result = s.db.WithContext(ctx).Model(&model.MailAlias{}).Where("id=? AND user_id=? AND status=?", row.ID, u.ID, "needs_review").Updates(map[string]any{"status": target, "error_code": "", "updated_at": time.Now().UTC()})
	if result.Error != nil || result.RowsAffected != 1 {
		return mailAliasInfo(row), apperr.New(503, "mail.alias_review_required", "Alias confirmation requires reconciliation", nil)
	}
	row.Status = target
	row.ErrorCode = ""
	return mailAliasInfo(row), nil
}
