package mail

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/mail"
	"strings"
	"time"
)

type MailAutoReply struct {
	Enabled bool   `json:"enabled"`
	Subject string `json:"subject"`
	Text    string `json:"text"`
}
type MailFilterRule struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Field     string `json:"field"`
	Contains  string `json:"contains"`
	Action    string `json:"action"`
	MailboxID string `json:"mailbox_id,omitempty"`
}

// MailSignature is an account-scoped encrypted preference, never a global Sieve setting.
// HTML is sanitized at save time; text remains the accessibility/plaintext fallback.
type MailSignature struct {
	Enabled bool   `json:"enabled"`
	Text    string `json:"text"`
	HTML    string `json:"html,omitempty"`
	OnReply bool   `json:"on_reply"`
}
type MailSettings struct {
	Signature        MailSignature    `json:"signature"`
	ConversationMode bool             `json:"conversation_mode"`
	BlockedSenders   []string         `json:"blocked_senders"`
	AutoReply        MailAutoReply    `json:"auto_reply"`
	Rules            []MailFilterRule `json:"rules"`
}

func defaultMailSettings() MailSettings {
	return MailSettings{ConversationMode: true, BlockedSenders: []string{}, Rules: []MailFilterRule{}}
}
func sieveQuote(v string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", "", "\n", "\r\n").Replace(v) + `"`
}
func validateSettings(v *MailSettings) error {
	if len(v.BlockedSenders) > 100 || len(v.Rules) > 50 || len(v.AutoReply.Subject) > 200 || len(v.AutoReply.Text) > 8192 || len(v.Signature.Text) > 8192 || len(v.Signature.HTML) > 16384 {
		return errors.New("Mail settings exceed limits")
	}
	if strings.ContainsAny(v.AutoReply.Subject, "\r\n\x00") || strings.ContainsRune(v.AutoReply.Text, '\x00') || strings.ContainsRune(v.Signature.Text, '\x00') || strings.ContainsRune(v.Signature.HTML, '\x00') {
		return errors.New("Invalid auto reply")
	}
	if v.AutoReply.Enabled && strings.TrimSpace(v.AutoReply.Text) == "" {
		return errors.New("Auto reply text is required")
	}
	// Keep a narrowly sanitized formatting subset; reject nonempty hostile-only markup.
	v.Signature.HTML = sanitizeMailHTML(v.Signature.HTML)
	if v.Signature.Enabled && strings.TrimSpace(v.Signature.Text) == "" && strings.TrimSpace(mailHTMLText(v.Signature.HTML)) == "" {
		return errors.New("Enabled signature requires text")
	}
	seen := map[string]bool{}
	normalized := []string{}
	for _, a := range v.BlockedSenders {
		parsed, e := mail.ParseAddress(strings.TrimSpace(a))
		if e != nil || !strings.Contains(parsed.Address, "@") {
			return errors.New("Invalid blocked address")
		}
		a = strings.ToLower(parsed.Address)
		if !seen[a] {
			seen[a] = true
			normalized = append(normalized, a)
		}
	}
	v.BlockedSenders = normalized
	seen = map[string]bool{}
	for i := range v.Rules {
		r := &v.Rules[i]
		if r.ID == "" {
			r.ID = uuid.NewString()
		}
		if seen[r.ID] || len(r.ID) > 100 || len(r.Name) > 100 || len(r.Contains) > 512 || strings.TrimSpace(r.Contains) == "" || strings.ContainsAny(r.Contains, "\r\n\x00") {
			return errors.New("Invalid filter rule")
		}
		seen[r.ID] = true
		switch r.Field {
		case "from", "to", "subject":
		default:
			return errors.New("Invalid rule field")
		}
		switch r.Action {
		case "archive", "trash", "junk":
		case "move":
			if r.MailboxID == "" {
				return errors.New("Rule needs folder")
			}
		default:
			return errors.New("Invalid rule action")
		}
	}
	if v.Rules == nil {
		v.Rules = []MailFilterRule{}
	}
	return nil
}
func (s *MailWorkspaceService) Settings(ctx context.Context, user authctx.CurrentUser) (MailSettings, error) {
	if _, _, e := s.auth(ctx, user); e != nil {
		return MailSettings{}, e
	}
	v := defaultMailSettings()
	var row model.MailPreference
	if e := s.db.Where("user_id = ?", user.ID).Take(&row).Error; e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return v, nil
		}
		return v, apperr.New(503, "mail.upstream_unavailable", "Mail operation could not be completed", nil)
	}
	plain, e := s.vault.Open(user.ID, user.ID, row.Ciphertext)
	if e != nil || json.Unmarshal(plain, &v) != nil {
		return v, apperr.New(503, "mail.upstream_unavailable", "Mail operation could not be completed", nil)
	}
	return v, nil
}
func renderMailSieve(v MailSettings, folders map[string]string) string {
	var b strings.Builder
	b.WriteString("require [\"fileinto\", \"mailboxid\", \"vacation\"];\n# Managed by Atoman Webmail.\n")
	for _, address := range v.BlockedSenders {
		fmt.Fprintf(&b, "if address :is \"from\" %s { fileinto :mailboxid %s \"INBOX\"; stop; }\n", sieveQuote(address), sieveQuote(folders["junk"]))
	}
	// Vacation is in the same active script so enabling it preserves filters.
	if v.AutoReply.Enabled {
		subject := v.AutoReply.Subject
		if subject == "" {
			subject = "Automatic reply"
		}
		fmt.Fprintf(&b, "vacation :days 1 :subject %s %s;\n", sieveQuote(subject), sieveQuote(v.AutoReply.Text))
	}
	for _, r := range v.Rules {
		if !r.Enabled {
			continue
		}
		target := r.MailboxID
		if r.Action != "move" {
			target = folders[r.Action]
		}
		fmt.Fprintf(&b, "if header :contains %s %s { fileinto :mailboxid %s \"INBOX\"; stop; }\n", sieveQuote(r.Field), sieveQuote(r.Contains), sieveQuote(target))
	}
	b.WriteString("keep;\n")
	return b.String()
}
func (s *MailWorkspaceService) SaveSettings(ctx context.Context, user authctx.CurrentUser, v MailSettings) (MailSettings, error) {
	cred, aid, e := s.auth(ctx, user)
	if e != nil {
		return v, e
	}
	if e = validateSettings(&v); e != nil {
		return v, apperr.New(400, "mail.invalid_settings", e.Error(), nil)
	}
	e = s.db.Transaction(func(tx *gorm.DB) error {
		// Serialize changes for the same account, including first save (no row yet).
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "mail-settings:"+user.ID.String()).Error; err != nil {
			return err
		}
		folders := map[string]string{}
		needs := map[string]bool{}
		if len(v.BlockedSenders) > 0 {
			needs["junk"] = true
		}
		for _, r := range v.Rules {
			if r.Enabled && r.Action != "move" {
				needs[r.Action] = true
			}
		}
		for role := range needs {
			id, err := s.roleFolder(ctx, cred, aid, role)
			if err != nil {
				return err
			}
			folders[role] = id
		}
		all, err := s.read.client.Mailboxes(ctx, cred, aid)
		if err != nil {
			return err
		}
		valid := map[string]bool{}
		for _, f := range all {
			valid[f.ID] = true
		}
		for _, r := range v.Rules {
			if r.Action == "move" && !valid[r.MailboxID] {
				return apperr.New(400, "mail.invalid_folder", "Filter folder does not exist", nil)
			}
		}
		script := renderMailSieve(v, folders)
		blob, err := s.read.client.uploadBlob(ctx, cred, aid, "application/sieve", []byte(script))
		if err != nil {
			return err
		}
		var get struct {
			State string `json:"state"`
			List  []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"list"`
		}
		const cap = "urn:ietf:params:jmap:sieve"
		if err = s.read.client.capabilityCall(ctx, cred, cap, "SieveScript/get", map[string]any{"accountId": aid, "properties": []string{"id", "name"}}, &get); err != nil {
			return err
		}
		id := ""
		for _, it := range get.List {
			if it.Name == "Atoman Webmail" {
				id = it.ID
				break
			}
		}
		args := map[string]any{"accountId": aid, "ifInState": get.State}
		if id == "" {
			args["create"] = map[string]any{"atoman": map[string]any{"name": "Atoman Webmail", "blobId": blob}}
			args["onSuccessActivateScript"] = "#atoman"
		} else {
			args["update"] = map[string]any{id: map[string]any{"blobId": blob}}
			args["onSuccessActivateScript"] = id
		}
		var applied struct {
			Created    map[string]json.RawMessage `json:"created"`
			Updated    map[string]json.RawMessage `json:"updated"`
			NotCreated map[string]any             `json:"notCreated"`
			NotUpdated map[string]any             `json:"notUpdated"`
		}
		if err = s.read.client.capabilityCall(ctx, cred, cap, "SieveScript/set", args, &applied); err != nil {
			return err
		}
		if len(applied.NotCreated) > 0 || len(applied.NotUpdated) > 0 || (id == "" && len(applied.Created) != 1) || (id != "" && len(applied.Updated) != 1) {
			return errors.New("Sieve script was not saved")
		}
		payload, _ := json.Marshal(v)
		cipher, err := s.vault.Seal(user.ID, user.ID, payload)
		if err != nil {
			return err
		}
		row := model.MailPreference{UserID: user.ID, Ciphertext: cipher, UpdatedAt: time.Now().UTC()}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}}, DoUpdates: clause.AssignmentColumns([]string{"ciphertext", "updated_at"})}).Create(&row).Error
	})
	if e != nil {
		return v, apperr.New(503, "mail.filter_sync_failed", "Mail filter settings could not be confirmed; retry saving to reconcile", nil)
	}
	return v, nil
}

type MailAppPassword struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	Password  string `json:"password,omitempty"`
}

func (s *MailWorkspaceService) AppPasswords(ctx context.Context, user authctx.CurrentUser) ([]MailAppPassword, error) {
	cred, aid, e := s.auth(ctx, user)
	if e != nil {
		return nil, e
	}
	var q struct {
		IDs []string `json:"ids"`
	}
	if e = s.read.client.call(ctx, cred, true, "x:AppPassword/query", map[string]any{"accountId": aid, "limit": 100}, &q); e != nil {
		return nil, apperr.New(503, "mail.upstream_unavailable", "Mail operation could not be completed", nil)
	}
	out := []MailAppPassword{}
	if len(q.IDs) == 0 {
		return out, nil
	}
	var g struct {
		List []struct {
			ID          string `json:"id"`
			Description string `json:"description"`
			CreatedAt   string `json:"createdAt"`
		} `json:"list"`
	}
	if e = s.read.client.call(ctx, cred, true, "x:AppPassword/get", map[string]any{"accountId": aid, "ids": q.IDs, "properties": []string{"id", "description", "createdAt"}}, &g); e != nil {
		return nil, apperr.New(503, "mail.upstream_unavailable", "Mail operation could not be completed", nil)
	}
	for _, p := range g.List {
		out = append(out, MailAppPassword{ID: p.ID, Name: p.Description, CreatedAt: p.CreatedAt})
	}
	return out, nil
}
func (s *MailWorkspaceService) CreateAppPassword(ctx context.Context, user authctx.CurrentUser, name string) (MailAppPassword, error) {
	cred, aid, e := s.auth(ctx, user)
	if e != nil {
		return MailAppPassword{}, e
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 || strings.ContainsAny(name, "\r\n\x00") {
		return MailAppPassword{}, apperr.New(400, "mail.invalid_device_name", "Device name is required (maximum 100 characters)", nil)
	}
	var res struct {
		Created map[string]struct {
			ID        string `json:"id"`
			Secret    string `json:"secret"`
			CreatedAt string `json:"createdAt"`
		} `json:"created"`
	}
	e = s.read.client.call(ctx, cred, true, "x:AppPassword/set", map[string]any{"accountId": aid, "create": map[string]any{"device": map[string]any{"description": name, "permissions": map[string]string{"@type": "Inherit"}, "allowedIps": map[string]any{}}}}, &res)
	p, ok := res.Created["device"]
	if e != nil || !ok || p.ID == "" || p.Secret == "" {
		return MailAppPassword{}, apperr.New(503, "mail.upstream_unavailable", "Mail operation could not be completed", nil)
	}
	return MailAppPassword{ID: p.ID, Name: name, CreatedAt: p.CreatedAt, Password: p.Secret}, nil
}
func (s *MailWorkspaceService) DeleteAppPassword(ctx context.Context, user authctx.CurrentUser, id string) error {
	cred, aid, e := s.auth(ctx, user)
	if e != nil {
		return e
	}
	if id == "" || len(id) > 512 {
		return apperr.New(400, "mail.invalid_device", "Invalid device", nil)
	}
	var res struct {
		Destroyed []string `json:"destroyed"`
	}
	if e = s.read.client.call(ctx, cred, true, "x:AppPassword/set", map[string]any{"accountId": aid, "destroy": []string{id}}, &res); e != nil {
		return apperr.New(503, "mail.upstream_unavailable", "Mail operation could not be completed", nil)
	}
	if len(res.Destroyed) != 1 || res.Destroyed[0] != id {
		return apperr.NotFound("mail.device_not_found", "Device not found")
	}
	return nil
}
