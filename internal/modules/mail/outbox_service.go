package mail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const mailUndoWindow = 5 * time.Second

type MailOutboxInfo struct {
	DraftID   *uuid.UUID `json:"draft_id,omitempty"`
	ID        uuid.UUID  `json:"id"`
	Status    string     `json:"status"`
	SendAfter time.Time  `json:"send_after"`
	Scheduled bool       `json:"scheduled"`
	CreatedAt time.Time  `json:"created_at"`
}

func mailOutboxInfo(o model.MailPendingSend) MailOutboxInfo {
	return MailOutboxInfo{DraftID: o.DraftID, ID: o.ID, Status: o.Status, SendAfter: o.SendAfter, CreatedAt: o.CreatedAt, Scheduled: o.SendAfter.Sub(o.CreatedAt) > 10*time.Second}
}

type MailDeliverer interface {
	// Submit may have accepted mail even if it returns an error. After a claim,
	// the job must never be retried without operator reconciliation.
	Submit(context.Context, uuid.UUID, string, uuid.UUID, outboxPayload) error
}

type MailOutboxService struct {
	db                  *gorm.DB
	store               MailPendingStore
	vault               *MailOutboxVault
	deliverer           MailDeliverer
	allowed             bool
	externalSMTPAllowed bool
	aliasesAllowed      bool
	now                 func() time.Time
}

// This service is NOT constructed by the default production route builder.
// SendAllowed and both verified operational gates must be supplied by a
// controlled operator when Stalwart and recovery are genuinely available.
func NewMailOutboxService(db *gorm.DB, store MailPendingStore, vault *MailOutboxVault, deliverer MailDeliverer, sendAllowed bool, gates MailActivationGates) *MailOutboxService {
	return &MailOutboxService{db: db, store: store, vault: vault, deliverer: deliverer, allowed: sendAllowed && gates.ready(), now: time.Now}
}

// EnableExternalSMTP is only called under an additional explicit operational gate.
func (s *MailOutboxService) EnableExternalSMTP(enabled bool) {
	if s != nil {
		s.externalSMTPAllowed = enabled
	}
}
func (s *MailOutboxService) EnableMailAliases(enabled bool) {
	if s != nil {
		s.aliasesAllowed = enabled
	}
}
func (s *MailOutboxService) ready() bool {
	return s != nil && s.allowed && s.db != nil && s.store != nil && s.vault != nil
}

func (s *MailOutboxService) Status(ctx context.Context, user authctx.CurrentUser, id uuid.UUID) (MailOutboxInfo, error) {
	if user.ID == uuid.Nil {
		return MailOutboxInfo{}, apperr.Unauthorized("Login required")
	}
	if s == nil || s.db == nil {
		return MailOutboxInfo{}, apperr.New(503, "mail.send_not_ready", "Mail sending is not available", nil)
	}
	if id == uuid.Nil {
		return MailOutboxInfo{}, apperr.BadRequest("mail.invalid_outbox", "Invalid pending mail identifier")
	}
	var entry model.MailPendingSend
	if err := s.db.WithContext(ctx).Where("id=? AND user_id=?", id, user.ID).Take(&entry).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return MailOutboxInfo{}, apperr.NotFound("mail.outbox_not_found", "Pending mail not found")
		}
		return MailOutboxInfo{}, apperr.Internal(err)
	}
	return mailOutboxInfo(entry), nil
}

func (s *MailOutboxService) Queue(ctx context.Context, user authctx.CurrentUser, input ComposeMailInput) (MailOutboxInfo, error) {
	if user.ID == uuid.Nil {
		return MailOutboxInfo{}, apperr.Unauthorized("Login required")
	}
	if !s.ready() {
		return MailOutboxInfo{}, apperr.New(503, "mail.send_not_ready", "Mail sending is not available", nil)
	}
	if input.RequestID == uuid.Nil {
		return MailOutboxInfo{}, apperr.BadRequest("mail.invalid_request", "A request id is required")
	}
	fingerprint, err := input.fingerprint()
	if err != nil {
		return MailOutboxInfo{}, apperr.BadRequest("mail.invalid_compose", "Invalid mail contents")
	}
	var prior model.MailPendingSend
	err = s.db.WithContext(ctx).Where("user_id=? AND request_id=?", user.ID, input.RequestID).Take(&prior).Error
	if err == nil {
		if prior.PayloadHash != fingerprint {
			return MailOutboxInfo{}, apperr.Conflict("mail.idempotency_conflict", "Request id was used for different mail contents")
		}
		return mailOutboxInfo(prior), nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return MailOutboxInfo{}, apperr.Internal(err)
	}
	var account model.MailAccount
	if err := s.db.WithContext(ctx).Where("user_id=? AND status=?", user.ID, "active").Take(&account).Error; err != nil {
		return MailOutboxInfo{}, apperr.Forbidden("mail.mailbox_inactive", "Active mailbox required")
	}
	if account.StalwartAccountID == nil || *account.StalwartAccountID == "" {
		return MailOutboxInfo{}, apperr.New(503, "mail.mailbox_unavailable", "Mailbox is not fully provisioned", nil)
	}
	var active int64
	if err := s.db.WithContext(ctx).Model(&model.User{}).Where("uuid=? AND is_active=?", user.ID, true).Count(&active).Error; err != nil {
		return MailOutboxInfo{}, apperr.Internal(err)
	}
	if active != 1 {
		return MailOutboxInfo{}, apperr.Forbidden("mail.account_inactive", "Atoman account disabled")
	}
	var draftRevision uint64
	if input.DraftID != nil {
		if *input.DraftID == uuid.Nil {
			return MailOutboxInfo{}, apperr.BadRequest("mail.invalid_draft", "Invalid draft")
		}
		var draft model.MailDraft
		if e := s.db.WithContext(ctx).Where("id = ? AND user_id = ?", *input.DraftID, user.ID).Take(&draft).Error; e != nil {
			return MailOutboxInfo{}, apperr.NotFound("mail.draft_missing", "Draft unavailable")
		}
		if input.DraftRevision != 0 && input.DraftRevision != draft.Revision {
			return MailOutboxInfo{}, apperr.Conflict("mail.draft_conflict", "Draft changed; reload before sending")
		}
		draftRevision = draft.Revision
	}
	now := s.now().UTC()
	if input.SendAt != nil {
		planned := input.SendAt.UTC()
		if planned.Before(now.Add(mailUndoWindow)) || planned.After(now.Add(90*24*time.Hour)) {
			return MailOutboxInfo{}, apperr.BadRequest("mail.invalid_schedule", "Scheduled time must be at least 5 seconds ahead and within 90 days")
		}
	}
	if input.ExternalAccountID != nil && input.AliasID != nil {
		return MailOutboxInfo{}, apperr.BadRequest("mail.invalid_sender", "Select exactly one sender identity")
	}
	sender := account.Address
	var externalID *uuid.UUID
	var aliasID *uuid.UUID
	if input.AliasID != nil {
		if !s.aliasesAllowed || *input.AliasID == uuid.Nil {
			return MailOutboxInfo{}, apperr.New(503, "mail.aliases_disabled", "Mail alias sending not enabled", nil)
		}
		var alias model.MailAlias
		if err := s.db.WithContext(ctx).Where("id=? AND user_id=? AND stalwart_account_id=? AND status=?", *input.AliasID, user.ID, *account.StalwartAccountID, "active").Take(&alias).Error; err != nil {
			return MailOutboxInfo{}, apperr.NotFound("mail.alias_missing", "Active alias unavailable")
		}
		sender = alias.Address
		idCopy := alias.ID
		aliasID = &idCopy
	}
	if input.ExternalAccountID != nil {
		if !s.externalSMTPAllowed {
			return MailOutboxInfo{}, apperr.New(503, "mail.external_send_disabled", "External SMTP sending is not enabled", nil)
		}
		if *input.ExternalAccountID == uuid.Nil {
			return MailOutboxInfo{}, apperr.BadRequest("mail.external_invalid_account", "Choose a connected mailbox")
		}
		var external model.MailExternalAccount
		if e := s.db.WithContext(ctx).Where("id=? AND user_id=?", *input.ExternalAccountID, user.ID).Take(&external).Error; e != nil {
			return MailOutboxInfo{}, apperr.NotFound("mail.external_account_missing", "External mailbox unavailable")
		}
		creds, e := openExternalSMTPSecrets(s.vault, external)
		if e != nil {
			return MailOutboxInfo{}, apperr.New(503, "mail.external_credential_unavailable", "External mailbox credential unavailable", nil)
		}
		sender = creds.Address
		idCopy := external.ID
		externalID = &idCopy
	}
	id := uuid.New()
	if err := s.resolveAttachments(ctx, user.ID, &input); err != nil {
		return MailOutboxInfo{}, apperr.BadRequest("mail.invalid_attachment", "Attachment unavailable or total size exceeds 25 MB")
	}
	payload, err := buildOutboxPayload(sender, id, input, now)
	payload.ExternalAccountID = externalID
	payload.AliasID = aliasID
	if err != nil {
		return MailOutboxInfo{}, apperr.BadRequest("mail.invalid_compose", "Invalid recipients or message contents")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return MailOutboxInfo{}, apperr.Internal(err)
	}
	blob, err := s.vault.Seal(user.ID, id, raw)
	if err != nil {
		return MailOutboxInfo{}, apperr.New(503, "mail.encrypt_unavailable", "Cannot secure pending mail", nil)
	}
	objectKey := "pending/v1/" + user.ID.String() + "/" + id.String()
	if err := s.store.Put(ctx, objectKey, blob); err != nil {
		return MailOutboxInfo{}, apperr.New(503, "mail.pending_storage_unavailable", "Cannot store pending mail", nil)
	}
	queuedAt := s.now().UTC()
	// The five-second Undo Send window always starts AFTER encrypted storage
	// succeeds, even when upload latency or the operator clock moves.
	sendAfter := queuedAt.Add(mailUndoWindow)
	if input.SendAt != nil && input.SendAt.UTC().After(sendAfter) {
		sendAfter = input.SendAt.UTC()
	}
	entry := model.MailPendingSend{DraftID: input.DraftID, DraftRevision: draftRevision, ID: id, UserID: user.ID, RequestID: input.RequestID, AccountID: *account.StalwartAccountID, ExternalAccountID: externalID, AliasID: aliasID, SnapshotKey: objectKey, PayloadHash: fingerprint, Status: "queued", SendAfter: sendAfter, CreatedAt: queuedAt, UpdatedAt: queuedAt}
	if err := s.db.WithContext(ctx).Create(&entry).Error; err != nil {
		// We only remove the uniquely named *new* R2 object. A concurrent retry
		// uses its own random object key and never deletes the winner's snapshot.
		cleanCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.store.Delete(cleanCtx, objectKey)
		var winner model.MailPendingSend
		if getErr := s.db.WithContext(ctx).Where("user_id=? AND request_id=?", user.ID, input.RequestID).Take(&winner).Error; getErr == nil {
			if winner.PayloadHash != fingerprint {
				return MailOutboxInfo{}, apperr.Conflict("mail.idempotency_conflict", "Request id was used for different mail contents")
			}
			return mailOutboxInfo(winner), nil
		}
		return MailOutboxInfo{}, apperr.New(503, "mail.queue_failed", "Could not persist pending mail", nil)
	}
	return mailOutboxInfo(entry), nil
}

func (s *MailOutboxService) Cancel(ctx context.Context, user authctx.CurrentUser, id uuid.UUID) (MailOutboxInfo, error) {
	if user.ID == uuid.Nil {
		return MailOutboxInfo{}, apperr.Unauthorized("Login required")
	}
	if !s.ready() {
		return MailOutboxInfo{}, apperr.New(503, "mail.send_not_ready", "Mail sending is not available", nil)
	}
	if id == uuid.Nil {
		return MailOutboxInfo{}, apperr.BadRequest("mail.invalid_outbox", "Invalid pending mail identifier")
	}
	now := s.now().UTC()
	changes := map[string]any{"status": "cancelled", "cancelled_at": now, "updated_at": now}
	result := s.db.WithContext(ctx).Model(&model.MailPendingSend{}).Where("id=? AND user_id=? AND status=? AND send_after>?", id, user.ID, "queued", now).Updates(changes)
	if result.Error != nil {
		return MailOutboxInfo{}, apperr.Internal(result.Error)
	}
	info, err := s.Status(ctx, user, id)
	if err != nil {
		return MailOutboxInfo{}, err
	}
	if result.RowsAffected != 1 && info.Status != "cancelled" {
		return info, apperr.Conflict("mail.undo_window_closed", "Too late to cancel: mail may already be sending")
	}
	// R2 delete is best-effort; the ciphertext cannot be submitted by workers
	// after the DB state has atomically become cancelled.
	if result.RowsAffected == 1 {
		var row model.MailPendingSend
		if e := s.db.WithContext(ctx).Select("snapshot_key").Where("id=? AND user_id=?", id, user.ID).Take(&row).Error; e == nil {
			cleanCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.store.Delete(cleanCtx, row.SnapshotKey)
		}
	}
	return info, nil
}

// ClaimDue is database-serialized (FOR UPDATE SKIP LOCKED on PostgreSQL).
// Only a configured, authenticated worker should ever call this method.
func (s *MailOutboxService) ClaimDue(ctx context.Context) (*model.MailPendingSend, error) {
	if !s.ready() || s.deliverer == nil {
		return nil, errors.New("Mail sending worker is disabled")
	}
	now := s.now().UTC()
	var entry model.MailPendingSend
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := tx.Where("status=? AND send_after<=?", "queued", now).Order("send_after ASC").Limit(1)
		if tx.Dialector.Name() == "postgres" {
			q = q.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
		}
		if err := q.Take(&entry).Error; err != nil {
			return err
		}
		result := tx.Model(&model.MailPendingSend{}).Where("id=? AND status=?", entry.ID, "queued").Updates(map[string]any{"status": "dispatching", "claimed_at": now, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("Mail dispatch lost exclusive claim")
		}
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entry.Status = "dispatching"
	entry.ClaimedAt = &now
	return &entry, nil
}

func (s *MailOutboxService) RunDue(ctx context.Context, maxJobs int) (int, error) {
	if maxJobs < 1 || maxJobs > 100 {
		return 0, errors.New("Invalid bounded worker batch size")
	}
	if !s.ready() || s.deliverer == nil {
		return 0, errors.New("Mail delivery transport is not configured")
	}
	completed := 0
	for i := 0; i < maxJobs; i++ {
		if err := ctx.Err(); err != nil {
			return completed, err
		}
		claimed, err := s.ClaimDue(ctx)
		if err != nil {
			return completed, err
		}
		if claimed == nil {
			return completed, nil
		}
		completed++
		// If the external delivery succeeds and the process crashes before status
		// commit, it remains dispatching. It is NEVER retried automatically.
		err = s.deliverClaimed(ctx, *claimed)
		if err != nil {
			return completed, err
		}
	}
	return completed, nil
}

func (s *MailOutboxService) deliverClaimed(ctx context.Context, item model.MailPendingSend) error {
	var active int64
	if err := s.db.WithContext(ctx).Model(&model.MailAccount{}).Where("user_id=? AND status=? AND stalwart_account_id=?", item.UserID, "active", item.AccountID).Count(&active).Error; err != nil {
		return err
	}
	var userActive int64
	if err := s.db.WithContext(ctx).Model(&model.User{}).Where("uuid=? AND is_active=?", item.UserID, true).Count(&userActive).Error; err != nil {
		return err
	}
	if active != 1 || userActive != 1 {
		return s.finishClaim(ctx, item.ID, "blocked")
	}
	blob, err := s.store.Get(ctx, item.SnapshotKey)
	if err != nil {
		_ = s.finishClaim(ctx, item.ID, "uncertain")
		return errors.New("Encrypted mail snapshot unavailable; manual reconciliation required")
	}
	raw, err := s.vault.Open(item.UserID, item.ID, blob)
	if err != nil {
		_ = s.finishClaim(ctx, item.ID, "uncertain")
		return errors.New("Pending mail integrity check failed; manual reconciliation required")
	}
	var payload outboxPayload
	if err = json.Unmarshal(raw, &payload); err != nil || payload.Sender == "" || len(payload.MIME) == 0 || len(payload.Recipients) == 0 {
		_ = s.finishClaim(ctx, item.ID, "uncertain")
		return errors.New("Pending mail snapshot invalid; manual reconciliation required")
	}
	// No automatic retry after the first external attempt, even on timeout.
	if err := s.deliverer.Submit(ctx, item.UserID, item.AccountID, item.ID, payload); err != nil {
		_ = s.finishClaim(ctx, item.ID, "uncertain")
		return fmt.Errorf("Mail submission requires reconciliation: %w", errors.New("upstream outcome unknown"))
	}
	if err := s.finishClaim(ctx, item.ID, "sent"); err != nil {
		return err
	}
	cleanCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.store.Delete(cleanCtx, item.SnapshotKey)
	return nil
}

func (s *MailOutboxService) finishClaim(ctx context.Context, id uuid.UUID, state string) error {
	if state != "blocked" && state != "sent" && state != "uncertain" {
		return errors.New("Invalid dispatch result")
	}
	now := s.now().UTC()
	fields := map[string]any{"status": state, "updated_at": now}
	if state == "sent" {
		fields["sent_at"] = now
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.MailPendingSend{}).Where("id=? AND status=?", id, "dispatching").Updates(fields)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("Pending send state changed unexpectedly")
		}
		if state == "sent" {
			var row model.MailPendingSend
			if e := tx.Where("id = ?", id).Take(&row).Error; e != nil {
				return e
			}
			return deleteAcceptedDraft(tx, row)
		}
		return nil
	})
}

// Stable digest is a convenient, non-sensitive operation-level identifier;
// it is never used as a substitute for the primary user+request uniqueness.
func outboxPayloadHash(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// Delete only the exact revision queued by this send. Later edits always survive.
func deleteAcceptedDraft(tx *gorm.DB, row model.MailPendingSend) error {
	if row.DraftID == nil || row.DraftRevision == 0 {
		return nil
	}
	return tx.Where("id = ? AND user_id = ? AND revision = ?", *row.DraftID, row.UserID, row.DraftRevision).Delete(&model.MailDraft{}).Error
}
