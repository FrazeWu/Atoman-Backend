package mail

import (
	"context"
	"errors"
	"fmt"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MailActivationGates represent verified operational release gates, not raw
// user-supplied HTTP flags. No production endpoint constructs this service in
// stage 5. Until R2/TrueNAS restore checks pass, no caller may turn these on.
type MailActivationGates struct {
	ProvisioningAllowed    bool
	R2ReadWriteVerified    bool
	TrueNASRestoreVerified bool
}

func (g MailActivationGates) ready() bool {
	return g.ProvisioningAllowed && g.R2ReadWriteVerified && g.TrueNASRestoreVerified
}

type mailStalwartProvisioner interface {
	FindDomainID(context.Context, StalwartCredential, string) (string, error)
	CreateUser(context.Context, StalwartCredential, string, string, uuid.UUID, uint64) (StalwartCreatedUser, error)
	CreateUserAPIKey(context.Context, StalwartCredential, string) (string, string, error)
	RevokeUserAPIKey(context.Context, StalwartCredential, string, string) error
}

// MailProvisionService owns the cross-store lifecycle. A partial remote JMAP
// failure leaves the reservation in 'provisioning' for manual reconciliation;
// it never auto-retries an uncertain account/key creation.
type MailProvisionService struct {
	db       *gorm.DB
	provider mailStalwartProvisioner
	vault    *MailCredentialVault
	admin    StalwartCredential
	gates    MailActivationGates
}

func NewMailProvisionService(db *gorm.DB, provider mailStalwartProvisioner, vault *MailCredentialVault, admin StalwartCredential, gates MailActivationGates) *MailProvisionService {
	return &MailProvisionService{db: db, provider: provider, vault: vault, admin: admin, gates: gates}
}

func (s *MailProvisionService) ProvisionReserved(ctx context.Context, actor authctx.CurrentUser, userID uuid.UUID) (AccountInfo, error) {
	if actor.ID == uuid.Nil || !authctx.RoleAtLeast(actor.Role, authctx.RoleAdmin) {
		return AccountInfo{}, apperr.Forbidden("mail.admin_required", "Administrator access required")
	}
	if s == nil || !s.gates.ready() || s.db == nil || s.provider == nil || s.vault == nil || userID == uuid.Nil {
		return AccountInfo{}, apperr.New(503, "mail.provisioning_not_ready", "Mail provisioning is not operationally approved", nil)
	}
	if _, err := s.admin.authorization(); err != nil {
		return AccountInfo{}, apperr.New(503, "mail.admin_key_unavailable", "Stalwart service authentication unavailable", nil)
	}
	domainID, err := s.provider.FindDomainID(ctx, s.admin, MailDomain)
	if err != nil {
		return AccountInfo{}, apperr.New(503, "mail.domain_not_ready", "Stalwart mail domain unavailable", nil)
	}
	var account model.MailAccount
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", userID).Take(&account).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("mail.not_reserved", "No mail reservation found")
			}
			return err
		}
		if account.Status != "reserved" || account.StalwartAccountID != nil || account.Address != account.LocalPart+"@"+MailDomain {
			return apperr.Conflict("mail.not_reservable", "Mail reservation cannot be provisioned again")
		}
		var active int64
		if err := tx.Model(&model.User{}).Where("uuid = ? AND is_active = ?", userID, true).Count(&active).Error; err != nil {
			return err
		}
		if active != 1 {
			return apperr.Forbidden("mail.user_inactive", "Active Atoman account required")
		}
		return tx.Model(&account).Update("status", "provisioning").Error
	})
	if err != nil {
		return AccountInfo{}, translateDBError(err)
	}

	created, err := s.provider.CreateUser(ctx, s.admin, domainID, account.LocalPart, userID, 2<<30)
	if err != nil {
		// Unknown outcome: remote request may have succeeded. Never silently
		// issue a duplicate create or mark active.
		return AccountInfo{}, apperr.New(503, "mail.provisioning_reconcile", "Mail account creation requires reconciliation", nil)
	}
	if created.AccountID == "" {
		return AccountInfo{}, apperr.New(503, "mail.provisioning_reconcile", "Mail account creation requires reconciliation", nil)
	}
	linkResult := s.db.Model(&model.MailAccount{}).
		Where("id = ? AND status = ? AND stalwart_account_id IS NULL", account.ID, "provisioning").
		Update("stalwart_account_id", created.AccountID)
	if linkResult.Error != nil || linkResult.RowsAffected != 1 {
		return AccountInfo{}, apperr.New(503, "mail.provisioning_reconcile", "Mail account mapping requires reconciliation", nil)
	}

	keyID, secret, err := s.provider.CreateUserAPIKey(ctx, s.admin, created.AccountID)
	if err != nil || keyID == "" || secret == "" {
		return AccountInfo{}, apperr.New(503, "mail.provisioning_reconcile", "Mail credential creation requires reconciliation", nil)
	}
	ciphertext, nonce, err := s.vault.Seal(userID, created.AccountID, secret)
	if err != nil {
		_ = s.provider.RevokeUserAPIKey(ctx, s.admin, created.AccountID, keyID)
		return AccountInfo{}, apperr.New(503, "mail.provisioning_reconcile", "Mail credential encryption failed", nil)
	}
	credential := model.MailAccessCredential{
		ID: uuid.New(), UserID: userID, StalwartAccountID: created.AccountID,
		APIKeyID: keyID, Ciphertext: ciphertext, Nonce: nonce, KeyVersion: 1,
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&credential).Error; err != nil {
			return err
		}
		result := tx.Model(&model.MailAccount{}).
			Where("id = ? AND status = ? AND stalwart_account_id = ?", account.ID, "provisioning", created.AccountID).
			Update("status", "active")
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("mail provisioning status changed unexpectedly")
		}
		return nil
	})
	if err != nil {
		_ = s.provider.RevokeUserAPIKey(ctx, s.admin, created.AccountID, keyID)
		return AccountInfo{}, apperr.New(503, "mail.provisioning_reconcile", "Mail credential persistence requires reconciliation", nil)
	}
	return AccountInfo{Available: true, Status: "active", Address: account.Address}, nil
}
