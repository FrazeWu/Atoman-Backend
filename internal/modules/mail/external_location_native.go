package mail

import (
	"context"
	"errors"
	"reflect"

	"atoman/internal/platform/authctx"
)

var ErrMailNativeMessageMissing = errors.New("Imported native message no longer exists")

type MailNativeLocationSnapshot struct {
	MailboxIDs map[string]bool
	State      string
}

// The interface is deliberately native-only: observing an IMAP disappearance
// never authorizes a remote IMAP MOVE, STORE or EXPUNGE.
type MailNativeLocation interface {
	ReadLocation(context.Context, authctx.CurrentUser, string) (MailNativeLocationSnapshot, error)
	TrashMailbox(context.Context, authctx.CurrentUser) (string, error)
	FindImportedFolder(context.Context, authctx.CurrentUser, string) (string, error)
	ValidateMailboxes(context.Context, authctx.CurrentUser, map[string]bool) error
	MoveLocation(context.Context, authctx.CurrentUser, string, MailNativeLocationSnapshot, map[string]bool) error
}
type stalwartMailNativeLocation struct{ read *MailReadService }

func (t stalwartMailNativeLocation) ReadLocation(ctx context.Context, u authctx.CurrentUser, emailID string) (MailNativeLocationSnapshot, error) {
	empty := MailNativeLocationSnapshot{}
	if t.read == nil || t.read.client == nil || !validJMAPScopedID(emailID) {
		return empty, errors.New("Invalid native location reader")
	}
	cred, aid, err := t.read.authorizedClient(ctx, u)
	if err != nil {
		return empty, err
	}
	var response struct {
		State string `json:"state"`
		List  []struct {
			ID         string          `json:"id"`
			MailboxIDs map[string]bool `json:"mailboxIds"`
		} `json:"list"`
		NotFound []string `json:"notFound"`
	}
	err = t.read.client.call(ctx, cred, false, "Email/get", map[string]any{"accountId": aid, "ids": []string{emailID}, "properties": []string{"id", "mailboxIds"}}, &response)
	if err == nil && len(response.NotFound) == 1 && response.NotFound[0] == emailID && len(response.List) == 0 {
		return empty, ErrMailNativeMessageMissing
	}
	if err != nil || response.State == "" || len(response.List) != 1 || len(response.NotFound) != 0 || response.List[0].ID != emailID {
		return empty, errors.New("Native message identity or state is unavailable")
	}
	mapIDs := copyFlags(response.List[0].MailboxIDs)
	if len(mapIDs) == 0 || len(mapIDs) > 10 {
		return empty, errors.New("Native message has invalid mailbox mapping")
	}
	for id := range mapIDs {
		if !validJMAPScopedID(id) {
			return empty, errors.New("Untrusted native mailbox identifier")
		}
	}
	return MailNativeLocationSnapshot{MailboxIDs: mapIDs, State: response.State}, nil
}
func (t stalwartMailNativeLocation) TrashMailbox(ctx context.Context, u authctx.CurrentUser) (string, error) {
	if t.read == nil || t.read.client == nil {
		return "", errors.New("Native mailbox unavailable")
	}
	cred, aid, err := t.read.authorizedClient(ctx, u)
	if err != nil {
		return "", err
	}
	boxes, err := t.read.client.Mailboxes(ctx, cred, aid)
	if err != nil {
		return "", err
	}
	trash := ""
	for _, box := range boxes {
		if box.Role != nil && *box.Role == "trash" {
			if !validJMAPScopedID(box.ID) || trash != "" {
				return "", errors.New("Ambiguous native trash folder")
			}
			trash = box.ID
		}
	}
	if trash == "" {
		return "", errors.New("No existing native trash folder")
	}
	return trash, nil
}

// Only an existing user-owned top-level Imported folder is accepted. A
// review operation must not silently CREATE a folder or use a name from UI.
func (t stalwartMailNativeLocation) FindImportedFolder(ctx context.Context, u authctx.CurrentUser, name string) (string, error) {
	if t.read == nil || t.read.client == nil || len(name) == 0 || len(name) > 100 {
		return "", errors.New("Invalid imported folder")
	}
	cred, aid, err := t.read.authorizedClient(ctx, u)
	if err != nil {
		return "", err
	}
	boxes, err := t.read.client.Mailboxes(ctx, cred, aid)
	if err != nil {
		return "", err
	}
	found := ""
	for _, box := range boxes {
		if box.ParentID == nil && box.Name == name {
			if found != "" || !validJMAPScopedID(box.ID) {
				return "", errors.New("Ambiguous imported mailbox")
			}
			found = box.ID
		}
	}
	if found == "" {
		return "", errors.New("Original imported folder unavailable")
	}
	return found, nil
}

func (t stalwartMailNativeLocation) ValidateMailboxes(ctx context.Context, u authctx.CurrentUser, targets map[string]bool) error {
	if t.read == nil || t.read.client == nil || len(targets) == 0 || len(targets) > 10 {
		return errors.New("Invalid target mailbox set")
	}
	cred, aid, err := t.read.authorizedClient(ctx, u)
	if err != nil {
		return err
	}
	boxes, err := t.read.client.Mailboxes(ctx, cred, aid)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, box := range boxes {
		if validJMAPScopedID(box.ID) {
			allowed[box.ID] = true
		}
	}
	for id, v := range targets {
		if !v || !allowed[id] {
			return errors.New("Target mailbox does not exist in account")
		}
	}
	return nil
}
func (t stalwartMailNativeLocation) MoveLocation(ctx context.Context, u authctx.CurrentUser, emailID string, expected MailNativeLocationSnapshot, target map[string]bool) error {
	if !validJMAPScopedID(emailID) || expected.State == "" || len(expected.MailboxIDs) == 0 || len(target) == 0 || reflect.DeepEqual(copyFlags(expected.MailboxIDs), copyFlags(target)) {
		return ErrMailFlagPrecondition
	}
	if err := t.ValidateMailboxes(ctx, u, target); err != nil {
		return ErrMailFlagPrecondition
	}
	before, err := t.ReadLocation(ctx, u, emailID)
	if err != nil {
		return ErrMailFlagPrecondition
	}
	if !reflect.DeepEqual(before.MailboxIDs, expected.MailboxIDs) || before.State != expected.State {
		return ErrMailFlagPrecondition
	}
	cred, aid, err := t.read.authorizedClient(ctx, u)
	if err != nil {
		return ErrMailFlagPrecondition
	}
	var result struct {
		Updated    map[string]any `json:"updated"`
		NotUpdated map[string]any `json:"notUpdated"`
	}
	err = t.read.client.call(ctx, cred, false, "Email/set", map[string]any{"accountId": aid, "ifInState": before.State, "update": map[string]any{emailID: map[string]any{"mailboxIds": target}}}, &result)
	if err != nil {
		return errors.New("Native move outcome uncertain")
	}
	if len(result.NotUpdated) != 0 || len(result.Updated) != 1 {
		return errors.New("Native move not acknowledged")
	}
	if _, ok := result.Updated[emailID]; !ok {
		return errors.New("Native move response for different message")
	}
	confirmed, err := t.ReadLocation(ctx, u, emailID)
	if err != nil || !reflect.DeepEqual(copyFlags(confirmed.MailboxIDs), copyFlags(target)) {
		return errors.New("Native move confirmation failed")
	}
	return nil
}
