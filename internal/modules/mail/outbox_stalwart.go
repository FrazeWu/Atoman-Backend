package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"atoman/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// StalwartOutboxSubmitter sends solely as the mapped Atoman user; never use
// an administrator token to send as arbitrary mailboxes.
type StalwartOutboxSubmitter struct {
	db             *gorm.DB
	client         *StalwartJMAPClient
	vault          *MailCredentialVault
	externalVault  *MailOutboxVault
	externalRemote ExternalSMTPRemote
}

func NewStalwartOutboxSubmitter(db *gorm.DB, client *StalwartJMAPClient, vault *MailCredentialVault) *StalwartOutboxSubmitter {
	return &StalwartOutboxSubmitter{db: db, client: client, vault: vault}
}
func (s *StalwartOutboxSubmitter) EnableExternalSMTP(vault *MailOutboxVault, remote ExternalSMTPRemote) {
	if s != nil {
		s.externalVault = vault
		s.externalRemote = remote
	}
}
func (s *StalwartOutboxSubmitter) Submit(ctx context.Context, userID uuid.UUID, accountID string, pendingID uuid.UUID, p outboxPayload) error {
	if s == nil || s.db == nil || s.client == nil || s.vault == nil || userID == uuid.Nil || !validJMAPScopedID(accountID) {
		return errors.New("Mail submission is not configured")
	}
	var account model.MailAccount
	if err := s.db.WithContext(ctx).Where("user_id=? AND status=? AND stalwart_account_id=?", userID, "active", accountID).Take(&account).Error; err != nil {
		return errors.New("Mail account mapping unavailable")
	}
	if p.ExternalAccountID != nil {
		return s.submitExternalSMTP(ctx, userID, accountID, pendingID, p)
	}
	if p.AliasID != nil {
		if p.ExternalAccountID != nil || *p.AliasID == uuid.Nil {
			return errors.New("Invalid multiple sender identities")
		}
		var alias model.MailAlias
		if err := s.db.WithContext(ctx).Where("id=? AND user_id=? AND stalwart_account_id=? AND status=?", *p.AliasID, userID, accountID, "active").Take(&alias).Error; err != nil {
			return errors.New("Alias no longer authorized")
		}
		if alias.Address != p.Sender {
			return errors.New("Alias sender mismatch")
		}
		var pending model.MailPendingSend
		if err := s.db.WithContext(ctx).Where("id=? AND user_id=? AND account_id=? AND alias_id=? AND status=?", pendingID, userID, accountID, *p.AliasID, "dispatching").Take(&pending).Error; err != nil {
			return errors.New("Alias pending send mismatch")
		}
	} else if account.Address != p.Sender {
		return errors.New("Mail sender does not match reserved identity")
	}
	if len(p.Recipients) == 0 || len(p.Recipients) > 50 || len(p.MIME) == 0 || len(p.MIME) > maxMailSnapshotBytes {
		return errors.New("Invalid encrypted mail composition")
	}
	if _, err := validEnvelopeAddresses(p.Recipients); err != nil {
		return errors.New("Invalid mail submission envelope")
	}
	var stored model.MailAccessCredential
	if err := s.db.WithContext(ctx).Where("user_id=? AND stalwart_account_id=?", userID, accountID).Take(&stored).Error; err != nil || stored.KeyVersion != 1 {
		return errors.New("Mail scoped credential unavailable")
	}
	secret, err := s.vault.Open(userID, accountID, stored.Ciphertext, stored.Nonce)
	if err != nil {
		return errors.New("Mail scoped credential authentication failed")
	}
	credential := StalwartBearer(secret)
	session, err := s.client.Session(ctx, credential)
	if err != nil || session.PrimaryAccounts[jmapMailCapability] != accountID {
		return errors.New("Mail JMAP user scope mismatch")
	}
	return s.client.submitRFC822(ctx, credential, accountID, p, func(emailID string) error {
		r := s.db.WithContext(ctx).Model(&model.MailPendingSend{}).Where("id = ? AND user_id = ? AND status = ?", pendingID, userID, "dispatching").Update("imported_email_id", emailID)
		if r.Error != nil || r.RowsAffected != 1 {
			return errors.New("Mail import receipt could not be persisted")
		}
		return nil
	}, func(submissionID string) error {
		r := s.db.WithContext(ctx).Model(&model.MailPendingSend{}).Where("id = ? AND user_id = ? AND status = ?", pendingID, userID, "dispatching").Update("submission_id", submissionID)
		if r.Error != nil || r.RowsAffected != 1 {
			return errors.New("Mail submission receipt could not be persisted")
		}
		return nil
	})
}

// jmapSubmissionCall uses the submission capability in addition to JMAP mail.
func (s *StalwartJMAPClient) jmapSubmissionCall(ctx context.Context, cred StalwartCredential, method string, args any, target any) error {
	request := map[string]any{"using": []string{jmapCoreCapability, jmapMailCapability, "urn:ietf:params:jmap:submission"}, "methodCalls": []any{[]any{method, args, "c1"}}}
	var response struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	if err := s.request(ctx, cred, http.MethodPost, "/jmap/", request, &response); err != nil {
		return err
	}
	if len(response.MethodResponses) != 1 || len(response.MethodResponses[0]) != 3 {
		return errors.New("Stalwart submission response malformed")
	}
	var gotMethod, gotCall string
	if err := json.Unmarshal(response.MethodResponses[0][0], &gotMethod); err != nil {
		return errors.New("Stalwart submission response invalid")
	}
	if err := json.Unmarshal(response.MethodResponses[0][2], &gotCall); err != nil || gotCall != "c1" {
		return errors.New("Stalwart submission correlation invalid")
	}
	if gotMethod != method {
		return errors.New("Stalwart submission method failed")
	}
	if target != nil && json.Unmarshal(response.MethodResponses[0][1], target) != nil {
		return errors.New("Stalwart submission response unreadable")
	}
	return nil
}

// uploadRFC822 only accepts a bounded raw email from an authenticated
// per-user JMAP session. It never creates a public URL or returns the blob.
func (s *StalwartJMAPClient) uploadRFC822(ctx context.Context, cred StalwartCredential, accountID string, mime []byte) (string, error) {
	if s == nil || s.http == nil || !validJMAPScopedID(accountID) || len(mime) == 0 || len(mime) > maxMailSnapshotBytes {
		return "", errors.New("Invalid mail upload")
	}
	authorization, err := cred.authorization()
	if err != nil {
		return "", err
	}
	path := "/jmap/upload/" + url.PathEscape(accountID) + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+path, bytes.NewReader(mime))
	if err != nil {
		return "", errors.New("Cannot prepare mail upload")
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Content-Type", "message/rfc822")
	req.Header.Set("Accept", "application/json")
	res, err := s.http.Do(req)
	if err != nil {
		return "", errors.New("Mail upload connection failed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Mail upload rejected with HTTP %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 16*1024+1))
	if err != nil || len(raw) > 16*1024 {
		return "", errors.New("Mail upload result too large")
	}
	var out struct {
		AccountID string `json:"accountId"`
		BlobID    string `json:"blobId"`
		Size      uint64 `json:"size"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.AccountID != accountID || !validJMAPScopedID(out.BlobID) {
		return "", errors.New("Mail upload returned invalid ownership")
	}
	return out.BlobID, nil
}

// SubmitRFC822 performs JMAP upload -> Email/import -> EmailSubmission/set.
// No retry occurs after any uncertain upstream side effect. The enclosing
// outbox worker must record 'uncertain' rather than resubmitting.
func (s *StalwartJMAPClient) SubmitRFC822(ctx context.Context, cred StalwartCredential, accountID string, p outboxPayload) error {
	return s.submitRFC822(ctx, cred, accountID, p, nil, nil)
}
func (s *StalwartJMAPClient) submitRFC822(ctx context.Context, cred StalwartCredential, accountID string, p outboxPayload, onImported func(string) error, onSubmitted func(string) error) error {
	if accountID == "" || p.Sender == "" || len(p.Recipients) == 0 {
		return errors.New("Invalid JMAP submission")
	}
	folders, err := s.Mailboxes(ctx, cred, accountID)
	if err != nil {
		return err
	}
	var drafts, sent string
	for _, f := range folders {
		if f.Role == nil {
			continue
		}
		switch *f.Role {
		case "drafts":
			drafts = f.ID
		case "sent":
			sent = f.ID
		}
	}
	if drafts == "" || sent == "" {
		return errors.New("Mail drafts or sent mailbox missing")
	}
	var identities struct {
		List []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"list"`
	}
	if err := s.jmapSubmissionCall(ctx, cred, "Identity/get", map[string]any{"accountId": accountID, "ids": nil}, &identities); err != nil {
		return err
	}
	var identity string
	for _, item := range identities.List {
		if strings.EqualFold(item.Email, p.Sender) {
			identity = item.ID
			break
		}
	}
	if !validJMAPScopedID(identity) {
		return errors.New("Mail sender identity not configured")
	}
	blobID, err := s.uploadRFC822(ctx, cred, accountID, p.MIME)
	if err != nil {
		return err
	}
	var imported struct {
		Created map[string]struct {
			ID string `json:"id"`
		} `json:"created"`
		NotCreated map[string]json.RawMessage `json:"notCreated"`
	}
	args := map[string]any{"accountId": accountID, "emails": map[string]any{"m1": map[string]any{"blobId": blobID, "mailboxIds": map[string]bool{drafts: true}, "keywords": map[string]bool{"$draft": true}}}}
	if err := s.call(ctx, cred, false, "Email/import", args, &imported); err != nil {
		return err
	}
	emailID := imported.Created["m1"].ID
	if !validJMAPScopedID(emailID) || len(imported.NotCreated) != 0 {
		return errors.New("Mail import not confirmed")
	}
	if onImported != nil {
		if err := onImported(emailID); err != nil {
			return err
		}
	}
	rcpt := make([]map[string]any, 0, len(p.Recipients))
	for _, addr := range p.Recipients {
		rcpt = append(rcpt, map[string]any{"email": addr, "parameters": nil})
	}
	envelope := map[string]any{"mailFrom": map[string]any{"email": p.Sender, "parameters": nil}, "rcptTo": rcpt}
	var submitted struct {
		Created map[string]struct {
			ID string `json:"id"`
		} `json:"created"`
		NotCreated map[string]json.RawMessage `json:"notCreated"`
	}
	if err := s.jmapSubmissionCall(ctx, cred, "EmailSubmission/set", map[string]any{"accountId": accountID, "create": map[string]any{"s1": map[string]any{"identityId": identity, "emailId": emailID, "envelope": envelope}}}, &submitted); err != nil {
		return err
	}
	if !validJMAPScopedID(submitted.Created["s1"].ID) || len(submitted.NotCreated) != 0 {
		return errors.New("Mail submission not confirmed")
	}
	// Move the now-submitted copy out of Drafts without changing the outbound
	// submission result. Failure is surfaced for manual reconciliation.
	var updated struct {
		Updated    map[string]any             `json:"updated"`
		NotUpdated map[string]json.RawMessage `json:"notUpdated"`
	}
	patch := map[string]any{"mailboxIds/" + drafts: nil, "mailboxIds/" + sent: true, "keywords/$draft": nil}
	if err := s.call(ctx, cred, false, "Email/set", map[string]any{"accountId": accountID, "update": map[string]any{emailID: patch}}, &updated); err != nil {
		return err
	}
	if _, ok := updated.Updated[emailID]; !ok || len(updated.NotUpdated) != 0 {
		return errors.New("Submitted copy requires reconciliation")
	}
	if onSubmitted != nil {
		if err := onSubmitted(submitted.Created["s1"].ID); err != nil {
			return err
		}
	}
	return nil
}
