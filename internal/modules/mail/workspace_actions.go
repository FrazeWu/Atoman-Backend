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
	"gorm.io/gorm/clause"
	"reflect"
	"time"
)

type MailActionInput struct {
	IDs       []string `json:"ids"`
	Action    string   `json:"action"`
	MailboxID string   `json:"mailbox_id"`
}
type MailActionResult struct {
	Updated   []string `json:"updated"`
	Failed    []string `json:"failed,omitempty"`
	UndoToken string   `json:"undo_token,omitempty"`
}
type emailState struct {
	ID         string          `json:"id"`
	MailboxIDs map[string]bool `json:"mailboxIds"`
	Keywords   map[string]bool `json:"keywords"`
}
type actionSnapshot struct {
	Before []emailState `json:"before"`
	After  []emailState `json:"after"`
}

func (s *MailWorkspaceService) states(ctx context.Context, c StalwartCredential, a string, ids []string) ([]emailState, string, error) {
	var out struct {
		List     []emailState `json:"list"`
		State    string       `json:"state"`
		NotFound []string     `json:"notFound"`
	}
	e := s.read.client.call(ctx, c, false, "Email/get", map[string]any{"accountId": a, "ids": ids, "properties": []string{"id", "mailboxIds", "keywords"}}, &out)
	if e != nil {
		return nil, "", e
	}
	if len(out.List) != len(ids) || len(out.NotFound) > 0 {
		return nil, "", errors.New("some messages unavailable")
	}
	known := map[string]bool{}
	for _, id := range ids {
		known[id] = true
	}
	for _, m := range out.List {
		if !known[m.ID] {
			return nil, "", errors.New("scope mismatch")
		}
		delete(known, m.ID)
	}
	if len(known) > 0 {
		return nil, "", errors.New("missing messages")
	}
	return out.List, out.State, nil
}
func copyFlags(a map[string]bool) map[string]bool {
	b := map[string]bool{}
	for k, v := range a {
		if v {
			b[k] = true
		}
	}
	return b
}
func (s *MailWorkspaceService) roleFolder(ctx context.Context, c StalwartCredential, aid, role string) (string, error) {
	folders, e := s.read.client.Mailboxes(ctx, c, aid)
	if e != nil {
		return "", e
	}
	for _, f := range folders {
		if f.Role != nil && *f.Role == role {
			return f.ID, nil
		}
	}
	names := map[string]string{"archive": "Archive", "trash": "Trash", "junk": "Junk", "inbox": "Inbox", "drafts": "Drafts", "sent": "Sent"}
	var out struct {
		Created map[string]struct {
			ID string `json:"id"`
		} `json:"created"`
	}
	e = s.read.client.call(ctx, c, false, "Mailbox/set", map[string]any{"accountId": aid, "create": map[string]any{"folder": map[string]any{"name": names[role], "role": role}}}, &out)
	if e != nil || out.Created["folder"].ID == "" {
		return "", errors.New("folder unavailable")
	}
	return out.Created["folder"].ID, nil
}
func (s *MailWorkspaceService) Actions(ctx context.Context, u authctx.CurrentUser, in MailActionInput) (MailActionResult, error) {
	result := MailActionResult{Updated: []string{}}
	if len(in.IDs) == 0 || len(in.IDs) > 100 {
		return result, apperr.BadRequest("mail.invalid_selection", "Select 1 to 100 messages")
	}
	unique := map[string]bool{}
	for _, id := range in.IDs {
		if !validJMAPScopedID(id) || unique[id] {
			return result, apperr.BadRequest("mail.invalid_selection", "Invalid selection")
		}
		unique[id] = true
	}
	c, a, e := s.auth(ctx, u)
	if e != nil {
		return result, e
	}
	before, state, e := s.states(ctx, c, a, in.IDs)
	if e != nil {
		return result, apperr.NotFound("mail.message_missing", "Some selected messages are unavailable")
	}
	target, retention := "", ""
	switch in.Action {
	case "read", "unread", "star", "unstar":
	case "archive", "trash", "junk", "restore":
		role := in.Action
		if role == "restore" {
			role = "inbox"
		}
		target, e = s.roleFolder(ctx, c, a, role)
		if role == "trash" || role == "junk" {
			retention = role
		}
	case "move":
		folders, er := s.read.client.Mailboxes(ctx, c, a)
		if er != nil {
			return result, workspaceErr(er)
		}
		for _, f := range folders {
			if f.ID == in.MailboxID {
				target = f.ID
				if f.Role != nil && (*f.Role == "trash" || *f.Role == "junk") {
					retention = *f.Role
				}
				break
			}
		}
		if target == "" {
			return result, apperr.BadRequest("mail.invalid_folder", "Choose an existing folder")
		}
	default:
		return result, apperr.BadRequest("mail.invalid_action", "Unknown mail action")
	}
	if e != nil {
		return result, workspaceErr(e)
	}
	after := make([]emailState, 0, len(before))
	updates := map[string]any{}
	for _, m := range before {
		next := emailState{ID: m.ID, MailboxIDs: copyFlags(m.MailboxIDs), Keywords: copyFlags(m.Keywords)}
		if target != "" {
			next.MailboxIDs = map[string]bool{target: true}
		} else {
			switch in.Action {
			case "read":
				next.Keywords["$seen"] = true
			case "unread":
				delete(next.Keywords, "$seen")
			case "star":
				next.Keywords["$flagged"] = true
			case "unstar":
				delete(next.Keywords, "$flagged")
			}
		}
		updates[m.ID] = map[string]any{"mailboxIds": next.MailboxIDs, "keywords": next.Keywords}
		after = append(after, next)
	}
	// Persist the reversal before mutating the upstream; an unknown outcome is
	// never automatically retried. Conditional JMAP state protects concurrent edits.
	id := uuid.New()
	data, _ := json.Marshal(actionSnapshot{Before: before, After: after})
	blob, e := s.vault.Seal(u.ID, id, data)
	if e != nil {
		return result, workspaceErr(e)
	}
	receipt := model.MailActionReceipt{ID: id, UserID: u.ID, Ciphertext: blob, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(5 * time.Minute)}
	if e = s.db.WithContext(ctx).Create(&receipt).Error; e != nil {
		return result, apperr.Internal(e)
	}
	var out struct {
		Updated    map[string]any `json:"updated"`
		NotUpdated map[string]any `json:"notUpdated"`
	}
	args := map[string]any{"accountId": a, "update": updates}
	if state != "" {
		args["ifInState"] = state
	}
	if e = s.read.client.call(ctx, c, false, "Email/set", args, &out); e != nil {
		return result, workspaceErr(e)
	}
	for _, id := range in.IDs {
		if _, ok := out.Updated[id]; ok {
			result.Updated = append(result.Updated, id)
			if target != "" {
				if retention != "" {
					_ = s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}, {Name: "email_id"}}, DoUpdates: clause.AssignmentColumns([]string{"role", "moved_at"})}).Create(&model.MailRetention{UserID: u.ID, EmailID: id, Role: retention, MovedAt: time.Now().UTC()}).Error
				} else {
					_ = s.db.WithContext(ctx).Where("user_id=? AND email_id=?", u.ID, id).Delete(&model.MailRetention{}).Error
				}
			}
		} else {
			result.Failed = append(result.Failed, id)
		}
	}
	if len(result.Updated) == len(in.IDs) {
		result.UndoToken = receipt.ID.String()
	}
	return result, nil
}
func (s *MailWorkspaceService) UndoAction(ctx context.Context, u authctx.CurrentUser, token uuid.UUID) (MailActionResult, error) {
	result := MailActionResult{Updated: []string{}}
	c, a, e := s.auth(ctx, u)
	if e != nil {
		return result, e
	}
	if token == uuid.Nil {
		return result, apperr.BadRequest("mail.invalid_undo", "Invalid undo")
	}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var r model.MailActionReceipt
		if tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND user_id=? AND used_at IS NULL AND expires_at>?", token, u.ID, time.Now().UTC()).Take(&r).Error != nil {
			return apperr.Conflict("mail.undo_expired", "Undo has expired")
		}
		raw, e := s.vault.Open(u.ID, r.ID, r.Ciphertext)
		if e != nil {
			return workspaceErr(e)
		}
		var snap actionSnapshot
		if json.Unmarshal(raw, &snap) != nil {
			return workspaceErr(errors.New("invalid undo"))
		}
		ids := []string{}
		expected := map[string]emailState{}
		for _, v := range snap.After {
			ids = append(ids, v.ID)
			expected[v.ID] = v
		}
		current, state, e := s.states(ctx, c, a, ids)
		if e != nil {
			return workspaceErr(e)
		}
		for _, v := range current {
			exp := expected[v.ID]
			if !reflect.DeepEqual(copyFlags(v.MailboxIDs), copyFlags(exp.MailboxIDs)) || !reflect.DeepEqual(copyFlags(v.Keywords), copyFlags(exp.Keywords)) {
				return apperr.Conflict("mail.undo_changed", "Message changed since the action")
			}
		}
		updates := map[string]any{}
		for _, v := range snap.Before {
			updates[v.ID] = map[string]any{"mailboxIds": v.MailboxIDs, "keywords": v.Keywords}
		}
		var out struct {
			Updated map[string]any `json:"updated"`
		}
		args := map[string]any{"accountId": a, "update": updates}
		if state != "" {
			args["ifInState"] = state
		}
		if e = s.read.client.call(ctx, c, false, "Email/set", args, &out); e != nil {
			return workspaceErr(e)
		}
		for _, id := range ids {
			if _, ok := out.Updated[id]; ok {
				result.Updated = append(result.Updated, id)
				_ = tx.Where("user_id=? AND email_id=?", u.ID, id).Delete(&model.MailRetention{}).Error
			} else {
				result.Failed = append(result.Failed, id)
			}
		}
		return tx.Model(&r).Update("used_at", time.Now().UTC()).Error
	})
	return result, e
}
