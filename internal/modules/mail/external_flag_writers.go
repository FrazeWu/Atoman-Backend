package mail

import (
	"context"
	"errors"

	"atoman/internal/platform/authctx"
	"github.com/emersion/go-imap/v2"
)

// MailExternalFlagSourceWriter uses exact, read-only identity checks before a
// conditional IMAP STORE. Providers without CONDSTORE are deliberately read-only.
// ErrMailFlagPrecondition means no remote modification request was sent;
// unlike network ambiguity after STORE/Email/set, it is safe to release the
// prepared intent without treating a message as possibly modified.
var ErrMailFlagPrecondition = errors.New("Mail flag conditional precondition failed before write")

type MailExternalFlagSourceWriter interface {
	SetFlagIfUnchanged(context.Context, string, string, string, string, uint32, uint32, string, ExternalFlagSnapshot, bool) error
}

type MailExternalFlagTargetWriter interface {
	SetFlagIfUnchanged(context.Context, authctx.CurrentUser, string, string, ExternalFlagSnapshot, bool) error
}

func flagValue(s ExternalFlagSnapshot, flag string) (bool, error) {
	switch flag {
	case "seen":
		return s.Seen, nil
	case "flagged":
		return s.Flagged, nil
	default:
		return false, errors.New("Unsupported flag")
	}
}
func flagIMAPName(flag string) (imap.Flag, error) {
	switch flag {
	case "seen":
		return imap.FlagSeen, nil
	case "flagged":
		return imap.FlagFlagged, nil
	default:
		return "", errors.New("Unsupported IMAP flag")
	}
}
func imapMessageFlags(flags []imap.Flag) ExternalFlagSnapshot {
	var state ExternalFlagSnapshot
	for _, value := range flags {
		if value == imap.FlagSeen {
			state.Seen = true
		}
		if value == imap.FlagFlagged {
			state.Flagged = true
		}
	}
	return state
}
func (liveExternalHistoryRemote) SetFlagIfUnchanged(ctx context.Context, provider, username, password, folder string, validity, uid uint32, flag string, expected ExternalFlagSnapshot, desired bool) error {
	f, err := flagIMAPName(flag)
	if err != nil || !validExternalFolder(folder) || uid == 0 || validity == 0 {
		return errors.New("Invalid conditional source flag update")
	}
	observed, _ := flagValue(expected, flag)
	if observed == desired {
		return errors.New("No source flag change requested")
	}
	client, closeClient, err := externalIMAPConnect(ctx, provider, username, password)
	if err != nil {
		return err
	}
	defer closeClient()
	if !client.Caps().Has(imap.CapCondStore) {
		return ErrMailFlagPrecondition
	}
	mailbox, err := client.Select(folder, &imap.SelectOptions{CondStore: true}).Wait()
	if err != nil || mailbox.UIDValidity != validity || mailbox.HighestModSeq == 0 {
		return ErrMailFlagPrecondition
	}
	set := imap.UIDSetNum(imap.UID(uid))
	before, err := client.Fetch(set, &imap.FetchOptions{UID: true, Flags: true, ModSeq: true}).Collect()
	if err != nil || len(before) != 1 || before[0] == nil || uint32(before[0].UID) != uid || before[0].ModSeq == 0 {
		return ErrMailFlagPrecondition
	}
	if imapMessageFlags(before[0].Flags) != expected {
		return ErrMailFlagPrecondition
	}
	op := imap.StoreFlagsDel
	if desired {
		op = imap.StoreFlagsAdd
	}
	_, err = client.Store(set, &imap.StoreFlags{Op: op, Flags: []imap.Flag{f}}, &imap.StoreOptions{UnchangedSince: before[0].ModSeq}).Collect()
	if err != nil {
		return errors.New("Conditional source flag outcome not confirmed")
	}
	after, err := client.Fetch(set, &imap.FetchOptions{UID: true, Flags: true, ModSeq: true}).Collect()
	if err != nil || len(after) != 1 || after[0] == nil || uint32(after[0].UID) != uid || after[0].ModSeq == 0 {
		return errors.New("Source confirmation unavailable")
	}
	actual := imapMessageFlags(after[0].Flags)
	observed, _ = flagValue(actual, flag)
	if observed != desired {
		return errors.New("Conditional source write not confirmed")
	}
	if (flag == "seen" && actual.Flagged != expected.Flagged) || (flag == "flagged" && actual.Seen != expected.Seen) {
		return errors.New("Conditional source write changed another tracked flag")
	}
	// CONDSTORE gates changes since the exact FETCH snapshot; all other flags
	// remain untouched by the narrow +FLAGS/-FLAGS command.
	return nil
}

type stalwartMailFlagWriter struct{ read *MailReadService }

func (t stalwartMailFlagWriter) SetFlagIfUnchanged(ctx context.Context, user authctx.CurrentUser, emailID, flag string, expected ExternalFlagSnapshot, desired bool) error {
	if t.read == nil || t.read.client == nil || !validJMAPScopedID(emailID) {
		return errors.New("Invalid native conditional update")
	}
	name := ""
	switch flag {
	case "seen":
		name = "$seen"
	case "flagged":
		name = "$flagged"
	default:
		return errors.New("Unsupported native flag")
	}
	existing, _ := flagValue(expected, flag)
	if existing == desired {
		return errors.New("No native flag change requested")
	}
	cred, aid, err := t.read.authorizedClient(ctx, user)
	if err != nil {
		return err
	}
	var before struct {
		State string `json:"state"`
		List  []struct {
			ID       string          `json:"id"`
			Keywords map[string]bool `json:"keywords"`
		} `json:"list"`
		NotFound []string `json:"notFound"`
	}
	err = t.read.client.call(ctx, cred, false, "Email/get", map[string]any{"accountId": aid, "ids": []string{emailID}, "properties": []string{"id", "keywords"}}, &before)
	if err != nil || before.State == "" || len(before.List) != 1 || before.List[0].ID != emailID || len(before.NotFound) != 0 {
		return ErrMailFlagPrecondition
	}
	state := ExternalFlagSnapshot{Seen: before.List[0].Keywords["$seen"], Flagged: before.List[0].Keywords["$flagged"]}
	if state != expected {
		return ErrMailFlagPrecondition
	}
	var value any
	if desired {
		value = true
	}
	patch := map[string]any{"keywords/" + name: value}
	var outcome struct {
		Updated    map[string]any `json:"updated"`
		NotUpdated map[string]any `json:"notUpdated"`
	}
	err = t.read.client.call(ctx, cred, false, "Email/set", map[string]any{"accountId": aid, "ifInState": before.State, "update": map[string]any{emailID: patch}}, &outcome)
	if err != nil {
		return errors.New("Native conditional write outcome unconfirmed")
	}
	if len(outcome.NotUpdated) != 0 || len(outcome.Updated) != 1 {
		return errors.New("Native conditional write not acknowledged")
	}
	if _, ok := outcome.Updated[emailID]; !ok {
		return errors.New("Native conditional write target mismatch")
	}
	var after struct {
		List []struct {
			ID       string          `json:"id"`
			Keywords map[string]bool `json:"keywords"`
		} `json:"list"`
	}
	err = t.read.client.call(ctx, cred, false, "Email/get", map[string]any{"accountId": aid, "ids": []string{emailID}, "properties": []string{"id", "keywords"}}, &after)
	if err != nil || len(after.List) != 1 || after.List[0].ID != emailID {
		return errors.New("Native flag confirmation unavailable")
	}
	actual := after.List[0].Keywords[name]
	if actual != desired {
		return errors.New("Native flag update not confirmed")
	}
	if (flag == "seen" && after.List[0].Keywords["$flagged"] != expected.Flagged) || (flag == "flagged" && after.List[0].Keywords["$seen"] != expected.Seen) {
		return errors.New("Native write changed another tracked flag")
	}
	return nil
}
