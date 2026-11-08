package mail

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"atoman/internal/platform/authctx"
)

type ExternalHistoryTarget interface {
	EnsureFolder(ctx context.Context, user authctx.CurrentUser, name string) (string, error)
	ImportRaw(ctx context.Context, user authctx.CurrentUser, folderID string, raw []byte, receivedAt time.Time, seen, flagged bool) (emailID string, resultUncertain bool, err error)
}

type stalwartExternalHistoryTarget struct{ read *MailReadService }

func (t stalwartExternalHistoryTarget) EnsureFolder(ctx context.Context, user authctx.CurrentUser, name string) (string, error) {
	if t.read == nil || t.read.client == nil || len(name) == 0 || len(name) > 100 {
		return "", errors.New("Invalid target")
	}
	cred, aid, err := t.read.authorizedClient(ctx, user)
	if err != nil {
		return "", err
	}
	for attempt := 0; attempt < 2; attempt++ {
		var state struct {
			State string        `json:"state"`
			List  []MailboxInfo `json:"list"`
		}
		err = t.read.client.call(ctx, cred, false, "Mailbox/get", map[string]any{"accountId": aid, "ids": nil, "properties": []string{"id", "name", "parentId", "role"}}, &state)
		if err != nil {
			return "", errors.New("Target mailboxes unavailable")
		}
		for _, folder := range state.List {
			if strings.EqualFold(folder.Name, name) && folder.ParentID == nil && validJMAPScopedID(folder.ID) {
				return folder.ID, nil
			}
		}
		if len(state.List) >= 100 {
			return "", errors.New("Target mailbox quota exceeded")
		}
		var created struct {
			Created map[string]struct {
				ID string `json:"id"`
			} `json:"created"`
			NotCreated map[string]any `json:"notCreated"`
		}
		err = t.read.client.call(ctx, cred, false, "Mailbox/set", map[string]any{"accountId": aid, "ifInState": state.State, "create": map[string]any{"history": map[string]any{"name": name, "parentId": nil, "isSubscribed": true}}}, &created)
		if err == nil && validJMAPScopedID(created.Created["history"].ID) {
			return created.Created["history"].ID, nil
		}
		// On a conflict or uncertain create response, first LOOK UP the stable
		// folder name; never create another folder blindly.
	}
	return "", errors.New("Target import folder not confirmed")
}

func (t stalwartExternalHistoryTarget) ImportRaw(ctx context.Context, user authctx.CurrentUser, folderID string, raw []byte, date time.Time, seen, flagged bool) (string, bool, error) {
	if t.read == nil || t.read.client == nil || !validJMAPScopedID(folderID) || len(raw) == 0 || len(raw) > maxExternalHistoryMIMEBytes {
		return "", false, errors.New("Invalid import payload")
	}
	cred, aid, err := t.read.authorizedClient(ctx, user)
	if err != nil {
		return "", false, err
	}
	// Ensure a user may only import into one of their own currently visible folders.
	folders, err := t.read.client.Mailboxes(ctx, cred, aid)
	if err != nil {
		return "", false, err
	}
	valid := false
	for _, folder := range folders {
		if folder.ID == folderID {
			valid = true
			break
		}
	}
	if !valid {
		return "", false, errors.New("Target folder not owned")
	}
	blob, err := t.read.client.uploadBlob(ctx, cred, aid, "message/rfc822", raw)
	if err != nil {
		return "", false, err
	} // No Email/import attempted yet.
	keywords := map[string]bool{}
	if seen {
		keywords["$seen"] = true
	}
	if flagged {
		keywords["$flagged"] = true
	}
	input := map[string]any{"blobId": blob, "mailboxIds": map[string]bool{folderID: true}, "keywords": keywords}
	if !date.IsZero() {
		input["receivedAt"] = date.UTC().Format(time.RFC3339)
	}
	var reply struct {
		Created map[string]struct {
			ID string `json:"id"`
		} `json:"created"`
		NotCreated map[string]any `json:"notCreated"`
	}
	// After this call is initiated, the outcome must be durable-confirmed
	// before another attempt can be sent. Errors are always uncertain.
	err = t.read.client.call(ctx, cred, false, "Email/import", map[string]any{"accountId": aid, "emails": map[string]any{"history": input}}, &reply)
	if err != nil {
		return "", true, errors.New("Target import outcome uncertain")
	}
	result := reply.Created["history"].ID
	if !validJMAPScopedID(result) || len(reply.NotCreated) > 0 {
		return "", true, fmt.Errorf("Target import response unconfirmed")
	}
	return result, false, nil
}
