package mail

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"context"
	"encoding/json"
	"errors"
	"net/mail"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const maxExternalAccounts = 5

type MailExternalService struct {
	workspace *MailWorkspaceService
	remote    ExternalIMAPRemote
}

func NewMailExternalService(workspace *MailWorkspaceService, remote ExternalIMAPRemote) *MailExternalService {
	if workspace == nil || workspace.db == nil || workspace.vault == nil {
		return nil
	}
	if remote == nil {
		remote = liveExternalIMAPRemote{}
	}
	return &MailExternalService{workspace: workspace, remote: remote}
}

type ExternalConnectInput struct {
	Provider    string `json:"provider"`
	Address     string `json:"address"`
	AppPassword string `json:"app_password"`
}
type externalSecrets struct {
	Address     string `json:"address"`
	AppPassword string `json:"app_password"`
}
type MailExternalAccountInfo struct {
	ID            uuid.UUID  `json:"id"`
	Provider      string     `json:"provider"`
	Address       string     `json:"address"`
	LastSyncedAt  *time.Time `json:"last_synced_at,omitempty"`
	LastErrorCode string     `json:"last_error_code,omitempty"`
	MessagesCount int        `json:"messages_count"`
	UnreadCount   int        `json:"unread_count"`
	HasMore       bool       `json:"has_more"`
	Revision      uint64     `json:"revision"`
}
type ExternalUnifiedMessage struct {
	UIDValidity    uint32    `json:"uid_validity"`
	AccountID      uuid.UUID `json:"account_id"`
	AccountAddress string    `json:"account_address"`
	Provider       string    `json:"provider"`
	ExternalMessageInfo
}
type ExternalUnifiedInbox struct {
	Accounts []MailExternalAccountInfo `json:"accounts"`
	Messages []ExternalUnifiedMessage  `json:"messages"`
}

func validateExternalConnect(in ExternalConnectInput) (ExternalConnectInput, error) {
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	in.Address = strings.ToLower(strings.TrimSpace(in.Address))
	// App passwords are not normalized except for trim; Yahoo, QQ, Gmail and
	// iCloud each have different codes. Never log or echo a rejected token.
	in.AppPassword = strings.TrimSpace(in.AppPassword)
	_, providerOK := externalProviderHost(in.Provider)
	parsed, e := mail.ParseAddress(in.Address)
	if !providerOK || !utf8.ValidString(in.Address) || len(in.Address) > 254 ||
		e != nil || parsed.Address != in.Address || strings.ContainsAny(in.Address, "\r\n\t\x00 ") ||
		len(in.AppPassword) < 4 || len(in.AppPassword) > 256 ||
		strings.ContainsAny(in.AppPassword, "\x00\r\n") {
		return in, apperr.BadRequest("mail.invalid_external_account", "Invalid external IMAP provider or account")
	}
	for _, r := range in.Address {
		if r > 127 {
			return in, apperr.BadRequest("mail.invalid_external_account", "Use an ASCII email address")
		}
	}
	return in, nil
}
func (s *MailExternalService) access(ctx context.Context, u authctx.CurrentUser) error {
	if s == nil || s.workspace == nil {
		return apperr.New(503, "mail.external_unavailable", "External mailbox integration is disabled", nil)
	}
	_, _, err := s.workspace.auth(ctx, u)
	return err
}
func (s *MailExternalService) secrets(row model.MailExternalAccount) (externalSecrets, error) {
	raw, err := s.workspace.vault.Open(row.UserID, row.ID, row.CredentialCiphertext)
	if err != nil {
		return externalSecrets{}, workspaceErr(err)
	}
	var data externalSecrets
	if json.Unmarshal(raw, &data) != nil || data.Address == "" || data.AppPassword == "" {
		return externalSecrets{}, workspaceErr(errors.New("invalid encrypted external credential record"))
	}
	return data, nil
}
func (s *MailExternalService) snapshot(row model.MailExternalAccount) (ExternalInboxSnapshot, error) {
	if len(row.SnapshotCiphertext) == 0 {
		return ExternalInboxSnapshot{Messages: []ExternalMessageInfo{}}, nil
	}
	raw, err := s.workspace.vault.Open(row.UserID, row.ID, row.SnapshotCiphertext)
	if err != nil {
		return ExternalInboxSnapshot{}, workspaceErr(err)
	}
	var data ExternalInboxSnapshot
	if json.Unmarshal(raw, &data) != nil || len(data.Messages) > 60 {
		return ExternalInboxSnapshot{}, workspaceErr(errors.New("invalid encrypted external snapshot"))
	}
	if data.Messages == nil {
		data.Messages = []ExternalMessageInfo{}
	}
	return data, nil
}
func (s *MailExternalService) info(row model.MailExternalAccount) (MailExternalAccountInfo, error) {
	creds, err := s.secrets(row)
	if err != nil {
		return MailExternalAccountInfo{}, err
	}
	snap, err := s.snapshot(row)
	if err != nil {
		return MailExternalAccountInfo{}, err
	}
	unread := 0
	for _, m := range snap.Messages {
		if !m.Seen {
			unread++
		}
	}
	return MailExternalAccountInfo{
		ID: row.ID, Provider: row.Provider, Address: creds.Address, LastSyncedAt: row.LastSyncedAt,
		LastErrorCode: row.LastErrorCode, MessagesCount: len(snap.Messages),
		UnreadCount: unread, HasMore: snap.HasMore, Revision: row.Revision,
	}, nil
}
func (s *MailExternalService) ownRow(ctx context.Context, u authctx.CurrentUser, id uuid.UUID) (model.MailExternalAccount, error) {
	var row model.MailExternalAccount
	err := s.workspace.db.WithContext(ctx).Where("id=? AND user_id=?", id, u.ID).Take(&row).Error
	if err != nil {
		return row, apperr.NotFound("mail.external_account_missing", "External mailbox unavailable")
	}
	return row, nil
}
func (s *MailExternalService) listRows(ctx context.Context, u authctx.CurrentUser) ([]model.MailExternalAccount, error) {
	var rows []model.MailExternalAccount
	if err := s.workspace.db.WithContext(ctx).Where("user_id=?", u.ID).Order("created_at ASC").Limit(maxExternalAccounts + 1).Find(&rows).Error; err != nil {
		return nil, apperr.Internal(err)
	}
	if len(rows) > maxExternalAccounts {
		return nil, apperr.New(503, "mail.external_limit", "External mailbox limit exceeded", nil)
	}
	return rows, nil
}
func (s *MailExternalService) Accounts(ctx context.Context, u authctx.CurrentUser) ([]MailExternalAccountInfo, error) {
	if err := s.access(ctx, u); err != nil {
		return nil, err
	}
	rows, err := s.listRows(ctx, u)
	if err != nil {
		return nil, err
	}
	out := make([]MailExternalAccountInfo, 0, len(rows))
	for _, row := range rows {
		info, err := s.info(row)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}
func (s *MailExternalService) Connect(ctx context.Context, u authctx.CurrentUser, in ExternalConnectInput) (MailExternalAccountInfo, error) {
	if err := s.access(ctx, u); err != nil {
		return MailExternalAccountInfo{}, err
	}
	normalized, err := validateExternalConnect(in)
	if err != nil {
		return MailExternalAccountInfo{}, err
	}
	existing, err := s.Accounts(ctx, u)
	if err != nil {
		return MailExternalAccountInfo{}, err
	}
	if len(existing) >= maxExternalAccounts {
		return MailExternalAccountInfo{}, apperr.Conflict("mail.external_limit", "Too many external mailboxes")
	}
	for _, item := range existing {
		if strings.EqualFold(item.Address, normalized.Address) {
			return MailExternalAccountInfo{}, apperr.Conflict("mail.external_duplicate", "Mailbox is already connected")
		}
	}
	// Verify credentials and download bounded metadata BEFORE storing app password.
	// A bad password never enters the database.
	snap, err := s.remote.Sync(ctx, normalized.Provider, normalized.Address, normalized.AppPassword)
	if err != nil {
		return MailExternalAccountInfo{}, apperr.New(422, "mail.external_connection_failed", "External provider rejected connection or app password", nil)
	}
	if len(snap.Messages) > 60 {
		return MailExternalAccountInfo{}, apperr.New(422, "mail.external_too_many", "External provider result is too large", nil)
	}
	now := time.Now().UTC()
	id := uuid.New()
	credentials, _ := json.Marshal(externalSecrets{Address: normalized.Address, AppPassword: normalized.AppPassword})
	ciphertext, err := s.workspace.vault.Seal(u.ID, id, credentials)
	if err != nil {
		return MailExternalAccountInfo{}, workspaceErr(err)
	}
	rawSnapshot, _ := json.Marshal(snap)
	snapCipher, err := s.workspace.vault.Seal(u.ID, id, rawSnapshot)
	if err != nil {
		return MailExternalAccountInfo{}, workspaceErr(err)
	}
	row := model.MailExternalAccount{ID: id, UserID: u.ID, Provider: normalized.Provider,
		CredentialCiphertext: ciphertext, SnapshotCiphertext: snapCipher, Revision: 1,
		LastSyncedAt: &now, LastAttemptAt: &now, CreatedAt: now, UpdatedAt: now}
	err = s.workspace.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if e := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "mail-external:"+u.ID.String()).Error; e != nil {
				return e
			}
		}
		var others []model.MailExternalAccount
		if e := tx.Where("user_id=?", u.ID).Limit(maxExternalAccounts + 1).Find(&others).Error; e != nil {
			return e
		}
		if len(others) >= maxExternalAccounts {
			return apperr.Conflict("mail.external_limit", "Too many external mailboxes")
		}
		for _, candidate := range others {
			creds, e := s.secrets(candidate)
			if e != nil {
				return e
			}
			if strings.EqualFold(creds.Address, normalized.Address) {
				return apperr.Conflict("mail.external_duplicate", "Mailbox is already connected")
			}
		}
		return tx.Create(&row).Error
	})
	if err != nil {
		return MailExternalAccountInfo{}, err
	}
	return s.info(row)
}
func (s *MailExternalService) Sync(ctx context.Context, u authctx.CurrentUser, id uuid.UUID) (MailExternalAccountInfo, error) {
	if err := s.access(ctx, u); err != nil {
		return MailExternalAccountInfo{}, err
	}
	row, err := s.ownRow(ctx, u, id)
	if err != nil {
		return MailExternalAccountInfo{}, err
	}
	creds, err := s.secrets(row)
	if err != nil {
		return MailExternalAccountInfo{}, err
	}
	snap, err := s.remote.Sync(ctx, row.Provider, creds.Address, creds.AppPassword)
	if err != nil {
		_ = s.workspace.db.WithContext(ctx).Model(&model.MailExternalAccount{}).
			Where("id=? AND user_id=?", id, u.ID).Updates(map[string]any{"last_error_code": "connection_failed", "last_attempt_at": time.Now().UTC()}).Error
		return MailExternalAccountInfo{}, apperr.New(502, "mail.external_sync_failed", "Cannot synchronize external mailbox", nil)
	}
	if len(snap.Messages) > 60 {
		return MailExternalAccountInfo{}, apperr.BadRequest("mail.external_invalid_response", "Too many provider messages")
	}
	raw, _ := json.Marshal(snap)
	ciphertext, err := s.workspace.vault.Seal(u.ID, id, raw)
	if err != nil {
		return MailExternalAccountInfo{}, workspaceErr(err)
	}
	now := time.Now().UTC()
	updated := s.workspace.db.WithContext(ctx).Model(&model.MailExternalAccount{}).
		Where("id=? AND user_id=? AND revision=?", id, u.ID, row.Revision).
		Updates(map[string]any{"snapshot_ciphertext": ciphertext, "revision": row.Revision + 1,
			"last_synced_at": now, "last_attempt_at": now, "last_error_code": "", "updated_at": now})
	if updated.Error != nil {
		return MailExternalAccountInfo{}, apperr.Internal(updated.Error)
	}
	if updated.RowsAffected != 1 {
		return MailExternalAccountInfo{}, apperr.Conflict("mail.external_sync_conflict", "Mailbox changed; refresh")
	}
	row.SnapshotCiphertext = ciphertext
	row.Revision++
	row.LastSyncedAt = &now
	row.LastAttemptAt = &now
	row.LastErrorCode = ""
	return s.info(row)
}
func (s *MailExternalService) Delete(ctx context.Context, u authctx.CurrentUser, id uuid.UUID) error {
	if err := s.access(ctx, u); err != nil {
		return err
	}
	// A queued/running history transfer must not silently lose its source
	// credential while importing. Users can pause/cancel first.
	if s.workspace.db.Migrator().HasTable(&model.MailExternalImportJob{}) {
		var active int64
		err := s.workspace.db.WithContext(ctx).Model(&model.MailExternalImportJob{}).
			Where("user_id=? AND external_account_id=? AND status IN ?", u.ID, id, []string{"queued", "running", "watching"}).Count(&active).Error
		if err != nil {
			return apperr.Internal(err)
		}
		if active > 0 {
			return apperr.Conflict("mail.external_import_active", "Pause or cancel history import before removing this account")
		}
	}
	// A disconnected source must not strand a user-approved automated flag
	// job or its never-retry-until-reconciled receipt.
	if s.workspace.db.Migrator().HasTable(&model.MailExternalImportJob{}) && s.workspace.db.Migrator().HasColumn(&model.MailExternalImportJob{}, "flag_sync_enabled") {
		var enabled int64
		err := s.workspace.db.WithContext(ctx).Model(&model.MailExternalImportJob{}).Where("user_id=? AND external_account_id=? AND flag_sync_enabled=?", u.ID, id, true).Count(&enabled).Error
		if err != nil {
			return apperr.Internal(err)
		}
		if enabled > 0 {
			return apperr.Conflict("mail.external_flag_sync_active", "Pause two-way flag synchronization before disconnecting")
		}
	}
	if s.workspace.db.Migrator().HasTable(&model.MailExternalImportItem{}) && s.workspace.db.Migrator().HasColumn(&model.MailExternalImportItem{}, "flag_mutation_id") {
		var flagged int64
		err := s.workspace.db.WithContext(ctx).Model(&model.MailExternalImportItem{}).Where("user_id=? AND external_account_id=? AND flag_mutation_id IS NOT NULL", u.ID, id).Count(&flagged).Error
		if err != nil {
			return apperr.Internal(err)
		}
		if flagged > 0 {
			return apperr.Conflict("mail.external_flag_reconcile_required", "Reconcile uncertain flag changes before disconnecting")
		}
	}
	if s.workspace.db.Migrator().HasTable(&model.MailPendingSend{}) {
		var pending int64
		err := s.workspace.db.WithContext(ctx).Model(&model.MailPendingSend{}).Where("user_id=? AND external_account_id=? AND status IN ?", u.ID, id, []string{"queued", "dispatching", "uncertain"}).Count(&pending).Error
		if err != nil {
			return apperr.Internal(err)
		}
		if pending > 0 {
			return apperr.Conflict("mail.external_send_active", "Cancel or reconcile pending external mail before disconnecting")
		}
	}
	result := s.workspace.db.WithContext(ctx).Where("id=? AND user_id=?", id, u.ID).Delete(&model.MailExternalAccount{})
	if result.Error != nil {
		return apperr.Internal(result.Error)
	}
	if result.RowsAffected != 1 {
		return apperr.NotFound("mail.external_account_missing", "External mailbox unavailable")
	}
	return nil
}
func (s *MailExternalService) Unified(ctx context.Context, u authctx.CurrentUser) (ExternalUnifiedInbox, error) {
	if err := s.access(ctx, u); err != nil {
		return ExternalUnifiedInbox{}, err
	}
	rows, err := s.listRows(ctx, u)
	if err != nil {
		return ExternalUnifiedInbox{}, err
	}
	result := ExternalUnifiedInbox{Accounts: []MailExternalAccountInfo{}, Messages: []ExternalUnifiedMessage{}}
	for _, row := range rows {
		info, e := s.info(row)
		if e != nil {
			return ExternalUnifiedInbox{}, e
		}
		snap, e := s.snapshot(row)
		if e != nil {
			return ExternalUnifiedInbox{}, e
		}
		result.Accounts = append(result.Accounts, info)
		for _, message := range snap.Messages {
			result.Messages = append(result.Messages, ExternalUnifiedMessage{AccountID: row.ID, UIDValidity: snap.UIDValidity,
				AccountAddress: info.Address, Provider: info.Provider, ExternalMessageInfo: message})
		}
	}
	sort.SliceStable(result.Messages, func(i, j int) bool { return result.Messages[i].Date.After(result.Messages[j].Date) })
	if len(result.Messages) > 200 {
		result.Messages = result.Messages[:200]
	}
	return result, nil
}
func (s *MailExternalService) Read(ctx context.Context, u authctx.CurrentUser, id uuid.UUID, uid, validity uint32) (string, error) {
	if err := s.access(ctx, u); err != nil {
		return "", err
	}
	row, err := s.ownRow(ctx, u, id)
	if err != nil {
		return "", err
	}
	snap, err := s.snapshot(row)
	if err != nil {
		return "", err
	}
	if validity == 0 || snap.UIDValidity != validity {
		return "", apperr.Conflict("mail.external_stale", "Mailbox changed; synchronize again")
	}
	present := false
	for _, m := range snap.Messages {
		if m.UID == uid {
			present = true
			break
		}
	}
	if !present {
		return "", apperr.NotFound("mail.external_message_missing", "Message unavailable")
	}
	creds, err := s.secrets(row)
	if err != nil {
		return "", err
	}
	plaintext, err := s.remote.ReadPlain(ctx, row.Provider, creds.Address, creds.AppPassword, "INBOX", validity, uid)
	if err != nil {
		return "", apperr.New(502, "mail.external_read_failed", "Cannot read provider message", nil)
	}
	return plaintext, nil
}

// SetSeen is an explicit, authenticated action. Previewing mail never marks it
// read. Provider state is verified before encrypted local metadata is updated.
func (s *MailExternalService) SetSeen(ctx context.Context, u authctx.CurrentUser, id uuid.UUID, uid, validity uint32, seen bool) (MailExternalAccountInfo, error) {
	if err := s.access(ctx, u); err != nil {
		return MailExternalAccountInfo{}, err
	}
	row, err := s.ownRow(ctx, u, id)
	if err != nil {
		return MailExternalAccountInfo{}, err
	}
	snap, err := s.snapshot(row)
	if err != nil {
		return MailExternalAccountInfo{}, err
	}
	if validity == 0 || snap.UIDValidity != validity {
		return MailExternalAccountInfo{}, apperr.Conflict("mail.external_stale", "Mailbox changed; synchronize again")
	}
	index := -1
	for i, m := range snap.Messages {
		if m.UID == uid {
			index = i
			break
		}
	}
	if index < 0 {
		return MailExternalAccountInfo{}, apperr.NotFound("mail.external_message_missing", "Message unavailable")
	}
	if snap.Messages[index].Seen == seen {
		return s.info(row)
	}
	creds, err := s.secrets(row)
	if err != nil {
		return MailExternalAccountInfo{}, err
	}
	if err := s.remote.SetSeen(ctx, row.Provider, creds.Address, creds.AppPassword, "INBOX", validity, uid, seen); err != nil {
		return MailExternalAccountInfo{}, apperr.New(502, "mail.external_flag_failed", "Provider did not confirm the change; refresh mailbox", nil)
	}
	snap.Messages[index].Seen = seen
	raw, err := json.Marshal(snap)
	if err != nil {
		return MailExternalAccountInfo{}, apperr.Internal(err)
	}
	ciphertext, err := s.workspace.vault.Seal(u.ID, id, raw)
	if err != nil {
		return MailExternalAccountInfo{}, workspaceErr(err)
	}
	now := time.Now().UTC()
	updated := s.workspace.db.WithContext(ctx).Model(&model.MailExternalAccount{}).
		Where("id=? AND user_id=? AND revision=?", id, u.ID, row.Revision).
		Updates(map[string]any{"snapshot_ciphertext": ciphertext, "revision": row.Revision + 1, "updated_at": now})
	if updated.Error != nil {
		return MailExternalAccountInfo{}, apperr.Internal(updated.Error)
	}
	if updated.RowsAffected != 1 {
		return MailExternalAccountInfo{}, apperr.Conflict("mail.external_sync_conflict", "Provider changed; refresh mailbox")
	}
	row.SnapshotCiphertext = ciphertext
	row.Revision++
	return s.info(row)
}

// Folder access always resolves the source account from the logged-in user.
// Provider hostnames and credentials never come from browse requests.
func (s *MailExternalService) Folders(ctx context.Context, u authctx.CurrentUser, accountID uuid.UUID) ([]ExternalFolderInfo, error) {
	if err := s.access(ctx, u); err != nil {
		return nil, err
	}
	row, err := s.ownRow(ctx, u, accountID)
	if err != nil {
		return nil, err
	}
	creds, err := s.secrets(row)
	if err != nil {
		return nil, err
	}
	folders, err := s.remote.ListFolders(ctx, row.Provider, creds.Address, creds.AppPassword)
	if err != nil {
		return nil, apperr.New(502, "mail.external_folders_failed", "Cannot list provider folders", nil)
	}
	return folders, nil
}
func (s *MailExternalService) BrowseFolder(ctx context.Context, u authctx.CurrentUser, accountID uuid.UUID, folder string, position uint32) (ExternalFolderPage, error) {
	if err := s.access(ctx, u); err != nil {
		return ExternalFolderPage{}, err
	}
	if !validExternalFolder(folder) || position > 100000 {
		return ExternalFolderPage{}, apperr.BadRequest("mail.external_invalid_folder", "Invalid folder")
	}
	row, err := s.ownRow(ctx, u, accountID)
	if err != nil {
		return ExternalFolderPage{}, err
	}
	creds, err := s.secrets(row)
	if err != nil {
		return ExternalFolderPage{}, err
	}
	page, err := s.remote.BrowseFolder(ctx, row.Provider, creds.Address, creds.AppPassword, folder, position)
	if err != nil {
		return ExternalFolderPage{}, apperr.New(502, "mail.external_browse_failed", "Cannot browse provider folder", nil)
	}
	return page, nil
}
func (s *MailExternalService) ReadFolder(ctx context.Context, u authctx.CurrentUser, accountID uuid.UUID, folder string, uid, validity uint32) (string, error) {
	if err := s.access(ctx, u); err != nil {
		return "", err
	}
	if !validExternalFolder(folder) || uid == 0 || validity == 0 {
		return "", apperr.BadRequest("mail.external_invalid_folder", "Invalid folder or message")
	}
	row, err := s.ownRow(ctx, u, accountID)
	if err != nil {
		return "", err
	}
	creds, err := s.secrets(row)
	if err != nil {
		return "", err
	}
	text, err := s.remote.ReadPlain(ctx, row.Provider, creds.Address, creds.AppPassword, folder, validity, uid)
	if err != nil {
		return "", apperr.New(502, "mail.external_read_failed", "Cannot read provider message", nil)
	}
	return text, nil
}

// A folder-specific write cannot silently become an INBOX operation. Live
// provider UIDVALIDITY and message UID are checked again on the write connection.
func (s *MailExternalService) SetFolderSeen(ctx context.Context, u authctx.CurrentUser, accountID uuid.UUID, folder string, uid, validity uint32, seen bool) error {
	if err := s.access(ctx, u); err != nil {
		return err
	}
	if !validExternalFolder(folder) || uid == 0 || validity == 0 {
		return apperr.BadRequest("mail.external_invalid_folder", "Invalid folder or message")
	}
	row, err := s.ownRow(ctx, u, accountID)
	if err != nil {
		return err
	}
	// Reuse the revision-protected cache update when this is a cached Inbox UID.
	if strings.EqualFold(folder, "INBOX") {
		snap, e := s.snapshot(row)
		if e != nil {
			return e
		}
		if snap.UIDValidity == validity {
			for _, m := range snap.Messages {
				if m.UID == uid {
					_, e = s.SetSeen(ctx, u, accountID, uid, validity, seen)
					return e
				}
			}
		}
	}
	creds, err := s.secrets(row)
	if err != nil {
		return err
	}
	if err := s.remote.SetSeen(ctx, row.Provider, creds.Address, creds.AppPassword, folder, validity, uid, seen); err != nil {
		return apperr.New(502, "mail.external_flag_failed", "Provider did not confirm the change; refresh mailbox", nil)
	}
	return nil
}

type ExternalMoveReceipt struct {
	Moved              bool `json:"moved"`
	InboxCacheUpdated  bool `json:"inbox_cache_updated"`
	RefreshRecommended bool `json:"refresh_recommended"`
}

// A confirmed remote MOVE is never presented as failed just because the local
// Inbox preview cache has a concurrent revision. Client can refresh safely.
func (s *MailExternalService) MoveFolderMessage(ctx context.Context, u authctx.CurrentUser, accountID uuid.UUID, source, target string, uid, validity uint32) (ExternalMoveReceipt, error) {
	if err := s.access(ctx, u); err != nil {
		return ExternalMoveReceipt{}, err
	}
	if !validExternalFolder(source) || !validExternalFolder(target) || source == target || uid == 0 || validity == 0 {
		return ExternalMoveReceipt{}, apperr.BadRequest("mail.external_invalid_move", "Invalid source or destination")
	}
	row, err := s.ownRow(ctx, u, accountID)
	if err != nil {
		return ExternalMoveReceipt{}, err
	}
	creds, err := s.secrets(row)
	if err != nil {
		return ExternalMoveReceipt{}, err
	}
	if err = s.remote.Move(ctx, row.Provider, creds.Address, creds.AppPassword, source, target, validity, uid); err != nil {
		return ExternalMoveReceipt{}, apperr.New(502, "mail.external_move_failed", "Provider did not confirm safe MOVE; refresh folders", nil)
	}
	receipt := ExternalMoveReceipt{Moved: true}
	if !strings.EqualFold(source, "INBOX") {
		return receipt, nil
	}
	snap, err := s.snapshot(row)
	if err != nil {
		receipt.RefreshRecommended = true
		return receipt, nil
	}
	if snap.UIDValidity != validity {
		receipt.RefreshRecommended = true
		return receipt, nil
	}
	index := -1
	for i, m := range snap.Messages {
		if m.UID == uid {
			index = i
			break
		}
	}
	if index < 0 {
		return receipt, nil
	}
	snap.Messages = append(snap.Messages[:index], snap.Messages[index+1:]...)
	raw, err := json.Marshal(snap)
	if err != nil {
		receipt.RefreshRecommended = true
		return receipt, nil
	}
	encrypted, err := s.workspace.vault.Seal(u.ID, accountID, raw)
	if err != nil {
		receipt.RefreshRecommended = true
		return receipt, nil
	}
	now := time.Now().UTC()
	updated := s.workspace.db.WithContext(ctx).Model(&model.MailExternalAccount{}).
		Where("id=? AND user_id=? AND revision=?", accountID, u.ID, row.Revision).
		Updates(map[string]any{"snapshot_ciphertext": encrypted, "revision": row.Revision + 1, "updated_at": now})
	if updated.Error != nil || updated.RowsAffected != 1 {
		receipt.RefreshRecommended = true
		return receipt, nil
	}
	receipt.InboxCacheUpdated = true
	return receipt, nil
}
