package mail

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

const maxMailTextBodyBytes = 64 << 10

// MailAddress represents structured JMAP identities, not untrusted HTML.
type MailAddress struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type MailAttachmentInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size uint64 `json:"size"`
}

// EmailDetail is a bounded, plain-text-only representation. Raw HTML, blob
// credentials and JMAP account IDs are deliberately not returned to browsers.
type EmailDetail struct {
	ID            string               `json:"id"`
	ThreadID      string               `json:"threadId"`
	Subject       string               `json:"subject"`
	ReceivedAt    string               `json:"receivedAt"`
	From          []MailAddress        `json:"from"`
	To            []MailAddress        `json:"to"`
	Cc            []MailAddress        `json:"cc"`
	PlainText     string               `json:"plainText"`
	BodyTruncated bool                 `json:"bodyTruncated"`
	HasPlainText  bool                 `json:"hasPlainText"`
	Attachments   []MailAttachmentInfo `json:"attachments"`
	IsRead        bool                 `json:"isRead"`
}

type jmapTextPart struct {
	PartID string `json:"partId"`
	Type   string `json:"type"`
}

type jmapTextValue struct {
	Value             string `json:"value"`
	IsTruncated       bool   `json:"isTruncated"`
	IsEncodingProblem bool   `json:"isEncodingProblem"`
}

type jmapFullEmail struct {
	ID          string                   `json:"id"`
	ThreadID    string                   `json:"threadId"`
	Subject     string                   `json:"subject"`
	ReceivedAt  string                   `json:"receivedAt"`
	MailboxIDs  map[string]bool          `json:"mailboxIds"`
	Keywords    map[string]bool          `json:"keywords"`
	From        []MailAddress            `json:"from"`
	To          []MailAddress            `json:"to"`
	Cc          []MailAddress            `json:"cc"`
	TextBody    []jmapTextPart           `json:"textBody"`
	BodyValues  map[string]jmapTextValue `json:"bodyValues"`
	Attachments []MailAttachmentInfo     `json:"attachments"`
}

func validJMAPScopedID(id string) bool {
	if id == "" || len(id) > 512 || strings.TrimSpace(id) != id {
		return false
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func (s *StalwartJMAPClient) ReadEmailDetail(ctx context.Context, cred StalwartCredential, accountID, mailboxID, emailID string) (EmailDetail, error) {
	if !validJMAPScopedID(accountID) || !validJMAPScopedID(mailboxID) || !validJMAPScopedID(emailID) {
		return EmailDetail{}, errors.New("Invalid scoped mail identifiers")
	}
	var got struct {
		List     []jmapFullEmail `json:"list"`
		NotFound []string        `json:"notFound"`
	}
	args := map[string]any{
		"accountId": accountID, "ids": []string{emailID},
		"properties":          []string{"id", "threadId", "subject", "receivedAt", "mailboxIds", "keywords", "from", "to", "cc", "textBody", "bodyValues", "attachments"},
		"bodyProperties":      []string{"partId", "type", "name", "size"},
		"fetchTextBodyValues": true, "fetchHTMLBodyValues": false, "maxBodyValueBytes": maxMailTextBodyBytes,
	}
	if err := s.call(ctx, cred, false, "Email/get", args, &got); err != nil {
		return EmailDetail{}, err
	}
	if len(got.List) != 1 || len(got.NotFound) != 0 || got.List[0].ID != emailID {
		return EmailDetail{}, errors.New("Email not found in scoped account")
	}
	src := got.List[0]
	if !src.MailboxIDs[mailboxID] {
		return EmailDetail{}, errors.New("Email is not in selected mailbox")
	}
	result := EmailDetail{ID: src.ID, ThreadID: src.ThreadID, Subject: src.Subject, ReceivedAt: src.ReceivedAt, IsRead: src.Keywords["$seen"]}
	if len(src.From) > 20 || len(src.To) > 100 || len(src.Cc) > 100 || len(src.Attachments) > 100 || len(src.TextBody) > 32 {
		return EmailDetail{}, errors.New("Email metadata exceeds safe limits")
	}
	result.From, result.To, result.Cc = src.From, src.To, src.Cc
	result.Attachments = src.Attachments
	var buffer strings.Builder
	for _, part := range src.TextBody {
		if part.Type != "text/plain" || part.PartID == "" {
			continue
		}
		value, ok := src.BodyValues[part.PartID]
		if !ok {
			continue
		}
		result.HasPlainText = true
		if value.IsTruncated || value.IsEncodingProblem {
			result.BodyTruncated = true
		}
		if buffer.Len() > 0 {
			buffer.WriteString("\n")
		}
		if buffer.Len() >= maxMailTextBodyBytes {
			result.BodyTruncated = true
			break
		}
		remaining := maxMailTextBodyBytes - buffer.Len()
		text := value.Value
		if len(text) > remaining {
			text = text[:remaining]
			for !utf8.ValidString(text) {
				text = text[:len(text)-1]
			}
			result.BodyTruncated = true
		}
		buffer.WriteString(text)
	}
	result.PlainText = buffer.String()
	return result, nil
}

// MarkEmailSeen updates a single message only after the caller has verified
// account ownership, folder membership, and displayed the detail to the user.
func (s *StalwartJMAPClient) MarkEmailSeen(ctx context.Context, cred StalwartCredential, accountID, mailboxID, emailID string) error {
	if !validJMAPScopedID(accountID) || !validJMAPScopedID(mailboxID) || !validJMAPScopedID(emailID) {
		return errors.New("Invalid scoped mail identifiers")
	}
	// Verify the message belongs to the folder, without fetching or rendering
	// a body. Do not trust an arbitrary ID posted by the browser.
	var lookup struct {
		List []struct {
			ID         string          `json:"id"`
			MailboxIDs map[string]bool `json:"mailboxIds"`
		} `json:"list"`
		NotFound []string `json:"notFound"`
	}
	if err := s.call(ctx, cred, false, "Email/get", map[string]any{"accountId": accountID, "ids": []string{emailID}, "properties": []string{"id", "mailboxIds"}}, &lookup); err != nil {
		return err
	}
	if len(lookup.List) != 1 || lookup.List[0].ID != emailID || !lookup.List[0].MailboxIDs[mailboxID] || len(lookup.NotFound) != 0 {
		return errors.New("Email not found in selected mailbox")
	}
	var response struct {
		Updated    map[string]any `json:"updated"`
		NotUpdated map[string]any `json:"notUpdated"`
	}
	update := map[string]any{"accountId": accountID, "update": map[string]any{emailID: map[string]any{"keywords/$seen": true}}}
	if err := s.call(ctx, cred, false, "Email/set", update, &response); err != nil {
		return err
	}
	if _, ok := response.Updated[emailID]; !ok || len(response.NotUpdated) != 0 {
		return errors.New("Stalwart did not confirm read state")
	}
	return nil
}
