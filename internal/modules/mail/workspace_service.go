package mail

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const maxMailAttachmentBytes = 10 << 20
const maxMailMIMEBytes = 40 << 20
const maxMailSnapshotBytes = 56 << 20

type MailWorkspaceService struct {
	read  *MailReadService
	db    *gorm.DB
	vault *MailOutboxVault
	store MailPendingStore
}

func NewMailWorkspaceService(read *MailReadService, outbox *MailOutboxService) *MailWorkspaceService {
	if read == nil || outbox == nil {
		return nil
	}
	return &MailWorkspaceService{read: read, db: read.db, vault: outbox.vault, store: outbox.store}
}
func (s *MailWorkspaceService) auth(ctx context.Context, u authctx.CurrentUser) (StalwartCredential, string, error) {
	if s == nil || s.db == nil || s.read == nil {
		return StalwartCredential{}, "", apperr.New(503, "mail.not_ready", "Mail is not available", nil)
	}
	return s.read.authorizedClient(ctx, u)
}
func workspaceErr(err error) error {
	if err == nil {
		return nil
	}
	return apperr.New(503, "mail.upstream_unavailable", "Mail operation could not be completed", nil)
}

type DraftCompose struct {
	ExternalAccountID *uuid.UUID         `json:"external_account_id,omitempty"`
	To                []string           `json:"to"`
	Cc                []string           `json:"cc"`
	Bcc               []string           `json:"bcc"`
	Subject           string             `json:"subject"`
	Text              string             `json:"text"`
	HTML              string             `json:"html,omitempty"`
	SendAt            *time.Time         `json:"send_at,omitempty"`
	InReplyTo         string             `json:"in_reply_to,omitempty"`
	References        []string           `json:"references,omitempty"`
	Attachments       []model.MailUpload `json:"attachments"`
}
type MailDraftInfo struct {
	ID        uuid.UUID     `json:"id"`
	Revision  uint64        `json:"revision"`
	Subject   string        `json:"subject"`
	UpdatedAt time.Time     `json:"updated_at"`
	Compose   *DraftCompose `json:"compose,omitempty"`
}
type SaveMailDraftInput struct {
	Revision uint64       `json:"revision"`
	Compose  DraftCompose `json:"compose"`
}

func (s *MailWorkspaceService) openDraft(d model.MailDraft, full bool) (MailDraftInfo, error) {
	raw, e := s.vault.Open(d.UserID, d.ID, d.Ciphertext)
	if e != nil {
		return MailDraftInfo{}, workspaceErr(e)
	}
	var c DraftCompose
	if json.Unmarshal(raw, &c) != nil {
		return MailDraftInfo{}, workspaceErr(errors.New("invalid draft"))
	}
	info := MailDraftInfo{ID: d.ID, Revision: d.Revision, Subject: c.Subject, UpdatedAt: d.UpdatedAt}
	if full {
		info.Compose = &c
	}
	return info, nil
}
func (s *MailWorkspaceService) Drafts(ctx context.Context, u authctx.CurrentUser) ([]MailDraftInfo, error) {
	if _, _, e := s.auth(ctx, u); e != nil {
		return nil, e
	}
	var rows []model.MailDraft
	if e := s.db.WithContext(ctx).Where("user_id=?", u.ID).Order("updated_at DESC").Limit(100).Find(&rows).Error; e != nil {
		return nil, apperr.Internal(e)
	}
	out := make([]MailDraftInfo, 0, len(rows))
	for _, r := range rows {
		v, e := s.openDraft(r, false)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *MailWorkspaceService) Draft(ctx context.Context, u authctx.CurrentUser, id uuid.UUID) (MailDraftInfo, error) {
	if _, _, e := s.auth(ctx, u); e != nil {
		return MailDraftInfo{}, e
	}
	var d model.MailDraft
	if e := s.db.WithContext(ctx).Where("id=? AND user_id=?", id, u.ID).Take(&d).Error; e != nil {
		return MailDraftInfo{}, apperr.NotFound("mail.draft_missing", "Draft unavailable")
	}
	return s.openDraft(d, true)
}

// Draft publication and upload retirement share a transaction-scoped user lock.
// Retiring is committed before remote object deletion, so a later draft can never
// publish a reference to bytes that a cleanup worker has already selected.
func lockMailDraftArtifacts(tx *gorm.DB, userID uuid.UUID) error {
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "mail-draft-artifacts:"+userID.String()).Error
}

func (s *MailWorkspaceService) SaveDraft(ctx context.Context, u authctx.CurrentUser, id uuid.UUID, in SaveMailDraftInput) (MailDraftInfo, error) {
	if _, _, e := s.auth(ctx, u); e != nil {
		return MailDraftInfo{}, e
	}
	c := in.Compose
	if id == uuid.Nil || len(c.Subject) > 998 || len(c.Text) > 256<<10 || len(c.HTML) > 256<<10 || len(c.To)+len(c.Cc)+len(c.Bcc) > 50 || len(c.Attachments) > 20 || !utf8.ValidString(c.Text) {
		return MailDraftInfo{}, apperr.BadRequest("mail.invalid_draft", "Draft is too large")
	}
	c.HTML = sanitizeMailHTML(c.HTML)
	var info MailDraftInfo
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := lockMailDraftArtifacts(tx, u.ID); e != nil {
			return apperr.Internal(e)
		}
		for i, a := range c.Attachments {
			var own model.MailUpload
			if e := tx.Where("id=? AND user_id=? AND retired_at IS NULL", a.ID, u.ID).Take(&own).Error; e != nil {
				return apperr.BadRequest("mail.invalid_attachment", "Attachment unavailable")
			}
			c.Attachments[i] = own
		}
		raw, _ := json.Marshal(c)
		blob, e := s.vault.Seal(u.ID, id, raw)
		if e != nil {
			return workspaceErr(e)
		}
		now := time.Now().UTC()
		if in.Revision == 0 {
			d := model.MailDraft{ID: id, UserID: u.ID, Ciphertext: blob, Revision: 1, CreatedAt: now, UpdatedAt: now}
			if e := tx.Create(&d).Error; e != nil {
				return apperr.Conflict("mail.draft_conflict", "Draft changed; reload before saving")
			}
			info, e = s.openDraft(d, true)
			return e
		}
		res := tx.Model(&model.MailDraft{}).Where("id=? AND user_id=? AND revision=?", id, u.ID, in.Revision).Updates(map[string]any{"ciphertext": blob, "revision": in.Revision + 1, "updated_at": now})
		if res.Error != nil {
			return apperr.Internal(res.Error)
		}
		if res.RowsAffected != 1 {
			return apperr.Conflict("mail.draft_conflict", "Draft changed; reload before saving")
		}
		info = MailDraftInfo{ID: id, Revision: in.Revision + 1, Subject: c.Subject, UpdatedAt: now, Compose: &c}
		return nil
	})
	if err != nil {
		return MailDraftInfo{}, err
	}
	return info, nil
}
func (s *MailWorkspaceService) DeleteDraft(ctx context.Context, u authctx.CurrentUser, id uuid.UUID) error {
	if _, _, e := s.auth(ctx, u); e != nil {
		return e
	}
	return s.db.WithContext(ctx).Where("id=? AND user_id=?", id, u.ID).Delete(&model.MailDraft{}).Error
}

func (s *MailWorkspaceService) Upload(ctx context.Context, u authctx.CurrentUser, name, contentType string, r io.Reader) (model.MailUpload, error) {
	if _, _, e := s.auth(ctx, u); e != nil {
		return model.MailUpload{}, e
	}
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if len(name) == 0 || len(name) > 255 || strings.ContainsAny(name, "\r\n\x00") || !utf8.ValidString(name) {
		return model.MailUpload{}, apperr.BadRequest("mail.invalid_filename", "Invalid attachment name")
	}
	raw, e := io.ReadAll(io.LimitReader(r, maxMailAttachmentBytes+1))
	if e != nil || len(raw) == 0 || len(raw) > maxMailAttachmentBytes {
		return model.MailUpload{}, apperr.BadRequest("mail.attachment_too_large", "Each attachment must be 10 MB or smaller")
	}
	if contentType == "" {
		contentType = http.DetectContentType(raw)
	}
	ct, _, e := mime.ParseMediaType(contentType)
	if e != nil || len(ct) > 128 {
		ct = "application/octet-stream"
	}
	// Always downloaded as an attachment; no browser content execution.
	id := uuid.New()
	blob, e := s.vault.Seal(u.ID, id, raw)
	if e != nil {
		return model.MailUpload{}, workspaceErr(e)
	}
	key := "uploads/v1/" + u.ID.String() + "/" + id.String()
	if e = s.store.Put(ctx, key, blob); e != nil {
		return model.MailUpload{}, workspaceErr(e)
	}
	item := model.MailUpload{ID: id, UserID: u.ID, Name: name, ContentType: ct, Size: int64(len(raw)), ObjectKey: key, CreatedAt: time.Now().UTC()}
	if e = s.db.WithContext(ctx).Create(&item).Error; e != nil {
		_ = s.store.Delete(ctx, key)
		return model.MailUpload{}, apperr.Internal(e)
	}
	return item, nil
}
func (s *MailWorkspaceService) DeleteUpload(ctx context.Context, u authctx.CurrentUser, id uuid.UUID) error {
	if _, _, e := s.auth(ctx, u); e != nil {
		return e
	}
	var a model.MailUpload
	if s.db.WithContext(ctx).Where("id=? AND user_id=?", id, u.ID).Take(&a).Error != nil {
		return apperr.NotFound("mail.attachment_missing", "Attachment unavailable")
	}
	if e := s.store.Delete(ctx, a.ObjectKey); e != nil {
		return workspaceErr(e)
	}
	return s.db.WithContext(ctx).Delete(&a).Error
}
func (s *MailOutboxService) resolveAttachments(ctx context.Context, u uuid.UUID, in *ComposeMailInput) error {
	if len(in.AttachmentIDs) > 20 {
		return errors.New("too many attachments")
	}
	seen := map[uuid.UUID]bool{}
	total := int64(0)
	for _, id := range in.AttachmentIDs {
		if id == uuid.Nil || seen[id] {
			return errors.New("invalid attachment")
		}
		seen[id] = true
		var a model.MailUpload
		if s.db.WithContext(ctx).Where("id=? AND user_id=? AND retired_at IS NULL", id, u).Take(&a).Error != nil {
			return errors.New("attachment unavailable")
		}
		total += a.Size
		if total > 25<<20 {
			return errors.New("attachments exceed 25 MB")
		}
		blob, e := s.store.Get(ctx, a.ObjectKey)
		if e != nil {
			return e
		}
		raw, e := s.vault.Open(u, id, blob)
		if e != nil || int64(len(raw)) != a.Size {
			return errors.New("attachment authentication failed")
		}
		in.attachments = append(in.attachments, composeAttachment{Name: a.Name, Type: a.ContentType, Data: raw})
	}
	return nil
}
