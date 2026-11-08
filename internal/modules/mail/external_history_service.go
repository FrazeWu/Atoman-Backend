package mail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type MailExternalHistoryService struct {
	external *MailExternalService
	source   ExternalHistoryRemote
	target   ExternalHistoryTarget
}

func NewMailExternalHistoryService(external *MailExternalService, source ExternalHistoryRemote, target ExternalHistoryTarget) *MailExternalHistoryService {
	if external == nil || external.workspace == nil || external.workspace.db == nil || external.workspace.vault == nil {
		return nil
	}
	if source == nil {
		source = liveExternalHistoryRemote{}
	}
	if target == nil && external.workspace.read != nil {
		target = stalwartExternalHistoryTarget{read: external.workspace.read}
	}
	return &MailExternalHistoryService{external: external, source: source, target: target}
}

type ExternalHistoryJobInfo struct {
	ID            uuid.UUID  `json:"id"`
	AccountID     uuid.UUID  `json:"account_id"`
	Folder        string     `json:"folder"`
	Status        string     `json:"status"`
	Continuous    bool       `json:"continuous"`
	LastPolledAt  *time.Time `json:"last_polled_at,omitempty"`
	UIDValidity   uint32     `json:"uid_validity"`
	UpperUID      uint32     `json:"upper_uid"`
	NextUID       uint32     `json:"next_uid"`
	Imported      uint32     `json:"imported"`
	Skipped       uint32     `json:"skipped"`
	LastErrorCode string     `json:"last_error_code,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func externalHistoryFolderKey(accountID uuid.UUID, folder string) string {
	sum := sha256.Sum256([]byte(accountID.String() + "\x00" + folder))
	return hex.EncodeToString(sum[:])
}
func externalHistoryFolderName(provider string, folderKey string, folder string) string {
	// Stable, non-ambiguous top-level folder name. Keep the source path readable
	// without leaking external account credentials or using untrusted hierarchy.
	text := strings.Map(func(r rune) rune {
		if r < 32 {
			return -1
		}
		return r
	}, strings.TrimSpace(folder))
	runes := []rune(text)
	if len(runes) > 35 {
		runes = runes[:35]
	}
	name := "Imported " + provider + " - " + string(runes) + " [" + folderKey[:10] + "]"
	for len([]byte(name)) > 100 && len(runes) > 0 {
		runes = runes[:len(runes)-1]
		name = "Imported " + provider + " - " + string(runes) + " [" + folderKey[:10] + "]"
	}
	return name
}
func (s *MailExternalHistoryService) valid() error {
	if s == nil || s.external == nil || s.source == nil || s.target == nil {
		return apperr.New(503, "mail.external_history_disabled", "History import is not configured", nil)
	}
	if !s.external.workspace.db.Migrator().HasTable(&model.MailExternalImportJob{}) || !s.external.workspace.db.Migrator().HasTable(&model.MailExternalImportItem{}) {
		return apperr.New(503, "mail.external_history_migration_required", "History import migration required", nil)
	}
	return nil
}
func (s *MailExternalHistoryService) info(row model.MailExternalImportJob) (ExternalHistoryJobInfo, error) {
	raw, e := s.external.workspace.vault.Open(row.UserID, row.ID, row.FolderCiphertext)
	if e != nil {
		return ExternalHistoryJobInfo{}, workspaceErr(e)
	}
	var data struct {
		Folder string `json:"folder"`
	}
	if json.Unmarshal(raw, &data) != nil || !validExternalFolder(data.Folder) {
		return ExternalHistoryJobInfo{}, workspaceErr(errors.New("invalid protected folder"))
	}
	return ExternalHistoryJobInfo{ID: row.ID, AccountID: row.ExternalAccountID, Folder: data.Folder, Status: row.Status, Continuous: row.Continuous, LastPolledAt: row.LastPolledAt, UIDValidity: row.UIDValidity, UpperUID: row.UpperUID, NextUID: row.NextUID, Imported: row.Imported, Skipped: row.Skipped, LastErrorCode: row.LastErrorCode, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
}
func (s *MailExternalHistoryService) List(ctx context.Context, u authctx.CurrentUser, accountID uuid.UUID) ([]ExternalHistoryJobInfo, error) {
	if e := s.valid(); e != nil {
		return nil, e
	}
	if e := s.external.access(ctx, u); e != nil {
		return nil, e
	}
	if _, e := s.external.ownRow(ctx, u, accountID); e != nil {
		return nil, e
	}
	var jobs []model.MailExternalImportJob
	if e := s.external.workspace.db.WithContext(ctx).Where("user_id=? AND external_account_id=?", u.ID, accountID).Order("created_at DESC").Limit(200).Find(&jobs).Error; e != nil {
		return nil, apperr.Internal(e)
	}
	out := make([]ExternalHistoryJobInfo, 0, len(jobs))
	for _, job := range jobs {
		item, e := s.info(job)
		if e != nil {
			return nil, e
		}
		out = append(out, item)
	}
	return out, nil
}
func (s *MailExternalHistoryService) Start(ctx context.Context, u authctx.CurrentUser, accountID uuid.UUID, folder string) (ExternalHistoryJobInfo, error) {
	return s.StartWithOptions(ctx, u, accountID, folder, false)
}
func (s *MailExternalHistoryService) StartWithOptions(ctx context.Context, u authctx.CurrentUser, accountID uuid.UUID, folder string, continuous bool) (ExternalHistoryJobInfo, error) {
	if e := s.valid(); e != nil {
		return ExternalHistoryJobInfo{}, e
	}
	if e := s.external.access(ctx, u); e != nil {
		return ExternalHistoryJobInfo{}, e
	}
	if !validExternalFolder(folder) {
		return ExternalHistoryJobInfo{}, apperr.BadRequest("mail.external_invalid_folder", "Invalid source folder")
	}
	account, e := s.external.ownRow(ctx, u, accountID)
	if e != nil {
		return ExternalHistoryJobInfo{}, e
	}
	// Authorize both the source account and the native target mailbox. Importing
	// into someone else's Stalwart account is never permitted.
	if _, _, e = s.external.workspace.auth(ctx, u); e != nil {
		return ExternalHistoryJobInfo{}, e
	}
	key := externalHistoryFolderKey(accountID, folder)
	var existing model.MailExternalImportJob
	found := s.external.workspace.db.WithContext(ctx).Where("user_id=? AND external_account_id=? AND folder_key=?", u.ID, accountID, key).Limit(1).Find(&existing)
	if found.Error != nil {
		return ExternalHistoryJobInfo{}, apperr.Internal(found.Error)
	}
	if found.RowsAffected != 0 {
		return ExternalHistoryJobInfo{}, apperr.Conflict("mail.external_history_exists", "An import job already exists for this folder")
	}
	creds, e := s.external.secrets(account)
	if e != nil {
		return ExternalHistoryJobInfo{}, e
	}
	probe, e := s.source.Inspect(ctx, account.Provider, creds.Address, creds.AppPassword, folder)
	if e != nil || probe.UIDValidity == 0 {
		return ExternalHistoryJobInfo{}, apperr.New(502, "mail.external_history_inspect_failed", "Cannot inspect the source folder", nil)
	}
	now := time.Now().UTC()
	job := model.MailExternalImportJob{ID: uuid.New(), UserID: u.ID, ExternalAccountID: accountID, FolderKey: key, UIDValidity: probe.UIDValidity, UpperUID: probe.UpperUID, NextUID: 1, Status: "queued", Continuous: continuous, CreatedAt: now, UpdatedAt: now}
	payload, _ := json.Marshal(map[string]string{"folder": folder})
	job.FolderCiphertext, e = s.external.workspace.vault.Seal(u.ID, job.ID, payload)
	if e != nil {
		return ExternalHistoryJobInfo{}, workspaceErr(e)
	}
	if e = s.external.workspace.db.WithContext(ctx).Create(&job).Error; e != nil {
		return ExternalHistoryJobInfo{}, apperr.Conflict("mail.external_history_exists", "An import job already exists for this folder")
	}
	return s.info(job)
}
func (s *MailExternalHistoryService) Action(ctx context.Context, u authctx.CurrentUser, accountID, jobID uuid.UUID, action string) (ExternalHistoryJobInfo, error) {
	if e := s.valid(); e != nil {
		return ExternalHistoryJobInfo{}, e
	}
	if e := s.external.access(ctx, u); e != nil {
		return ExternalHistoryJobInfo{}, e
	}
	if _, e := s.external.ownRow(ctx, u, accountID); e != nil {
		return ExternalHistoryJobInfo{}, e
	}
	var row model.MailExternalImportJob
	db := s.external.workspace.db.WithContext(ctx)
	if e := db.Where("id=? AND user_id=? AND external_account_id=?", jobID, u.ID, accountID).Take(&row).Error; e != nil {
		return ExternalHistoryJobInfo{}, apperr.NotFound("mail.external_history_missing", "Import job unavailable")
	}
	// Live copying is explicit opt-in. It never authorizes remote deletion,
	// modification of source mail, or unsolicited copying from other folders.
	if action == "enable_live" || action == "disable_live" {
		updatedStatus := row.Status
		enabled := action == "enable_live"
		if enabled {
			switch row.Status {
			case "completed":
				updatedStatus = "watching"
			case "queued", "running", "paused", "failed":
			default:
				return ExternalHistoryJobInfo{}, apperr.Conflict("mail.external_history_state_changed", "Import cannot enter live mode from its current state")
			}
		} else {
			switch row.Status {
			case "watching":
				updatedStatus = "completed"
			case "running":
				updatedStatus = "paused" // stop an in-flight poll after the current safe item
			case "queued", "paused", "failed":
			default:
				return ExternalHistoryJobInfo{}, apperr.Conflict("mail.external_history_state_changed", "Live mode is not active")
			}
		}
		changes := map[string]any{"continuous": enabled, "status": updatedStatus, "updated_at": time.Now().UTC()}
		if enabled && updatedStatus == "watching" {
			changes["last_polled_at"] = nil
		}
		result := db.Model(&model.MailExternalImportJob{}).
			Where("id=? AND user_id=? AND status=? AND continuous=?", jobID, u.ID, row.Status, row.Continuous).
			Updates(changes)
		if result.Error != nil {
			return ExternalHistoryJobInfo{}, apperr.Internal(result.Error)
		}
		if result.RowsAffected != 1 {
			return ExternalHistoryJobInfo{}, apperr.Conflict("mail.external_history_state_changed", "Import state changed; refresh")
		}
		row.Continuous = enabled
		row.Status = updatedStatus
		if enabled && updatedStatus == "watching" {
			row.LastPolledAt = nil
		}
		row.UpdatedAt = time.Now().UTC()
		return s.info(row)
	}
	var from []string
	to := ""
	switch action {
	case "pause":
		from = []string{"queued", "running", "watching"}
		to = "paused"
	case "resume":
		from = []string{"paused", "failed"}
		to = "queued"
	case "cancel":
		from = []string{"queued", "paused", "failed", "needs_review"}
		to = "cancelled"
	default:
		return ExternalHistoryJobInfo{}, apperr.BadRequest("mail.external_history_invalid_action", "Invalid import action")
	}
	// A failed/paused job with an in-flight item cannot be resumed until a
	// human reconciles whether JMAP accepted that original message.
	if action == "resume" {
		var uncertain int64
		if e := db.Model(&model.MailExternalImportItem{}).Where("job_id=? AND status IN ?", jobID, []string{"importing", "uncertain"}).Count(&uncertain).Error; e != nil {
			return ExternalHistoryJobInfo{}, apperr.Internal(e)
		}
		if uncertain != 0 {
			return ExternalHistoryJobInfo{}, apperr.Conflict("mail.external_history_reconcile_required", "Uncertain message requires manual reconciliation")
		}
	}
	now := time.Now().UTC()
	changes := map[string]any{"status": to, "claimed_at": nil, "updated_at": now}
	if !(action == "cancel" && row.Status == "needs_review") {
		changes["last_error_code"] = ""
	}
	result := db.Model(&model.MailExternalImportJob{}).Where("id=? AND user_id=? AND status IN ?", jobID, u.ID, from).Updates(changes)
	if result.Error != nil {
		return ExternalHistoryJobInfo{}, apperr.Internal(result.Error)
	}
	if result.RowsAffected != 1 {
		return ExternalHistoryJobInfo{}, apperr.Conflict("mail.external_history_state_changed", "Import state changed; refresh")
	}
	preserveReason := action == "cancel" && row.Status == "needs_review"
	row.Status = to
	if !preserveReason {
		row.LastErrorCode = ""
	}
	row.UpdatedAt = now
	return s.info(row)
}

// NewConfiguredExternalHistory uses the existing backend's two independent
// user-scoped vaults. It never accepts a Stalwart administrator credential.
func NewConfiguredExternalHistory(db *gorm.DB, vault *MailOutboxVault, read *MailReadService) (*MailExternalHistoryService, error) {
	if db == nil || vault == nil || read == nil || read.client == nil || read.vault == nil {
		return nil, errors.New("History import dependencies missing")
	}
	workspace := &MailWorkspaceService{db: db, vault: vault, read: read}
	external := NewMailExternalService(workspace, nil)
	service := NewMailExternalHistoryService(external, nil, nil)
	if service == nil {
		return nil, errors.New("Cannot initialize history importer")
	}
	return service, nil
}
