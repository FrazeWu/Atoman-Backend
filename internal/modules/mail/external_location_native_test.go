package mail

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocationNativeMoveUsesAuthenticatedJMAPStateAndNarrowMailboxChange(t *testing.T) {
	mailboxCalls := 0
	setCalls := 0
	getCalls := 0
	moved := false
	workspace, _, _, alice, bob := workspaceFixture(t, func(method string, args map[string]any) any {
		switch method {
		case "Mailbox/get":
			mailboxCalls++
			require.Equal(t, "stalwart-user-123", args["accountId"])
			return map[string]any{"list": []any{
				map[string]any{"id": "imported-inbox", "name": "Imported gmail - INBOX [aaaaaaaaaa]", "parentId": nil, "role": nil},
				map[string]any{"id": "native-trash", "name": "Trash", "parentId": nil, "role": "trash"},
			}}
		case "Email/get":
			if args["accountId"] == "workspace-bob-account" {
				return map[string]any{"state": "bob-state", "list": []any{}, "notFound": []string{"native-original"}}
			}
			getCalls++
			require.Equal(t, []any{"native-original"}, args["ids"])
			folders := map[string]bool{"imported-inbox": true}
			state := "state-before"
			if moved {
				folders = map[string]bool{"native-trash": true}
				state = "state-after"
			}
			return map[string]any{"state": state, "list": []any{map[string]any{"id": "native-original", "mailboxIds": folders}}}
		case "Email/set":
			setCalls++
			require.Equal(t, "state-before", args["ifInState"])
			updates := args["update"].(map[string]any)
			require.Len(t, updates, 1)
			patch := updates["native-original"].(map[string]any)
			require.Len(t, patch, 1)
			require.Equal(t, map[string]any{"native-trash": true}, patch["mailboxIds"])
			moved = true
			return map[string]any{"updated": map[string]any{"native-original": nil}}
		default:
			t.Fatalf("unexpected JMAP method %s", method)
			return nil
		}
	})
	target := stalwartMailNativeLocation{read: workspace.read}
	name := "Imported gmail - INBOX [aaaaaaaaaa]"
	orig, err := target.FindImportedFolder(context.Background(), alice, name)
	require.NoError(t, err)
	require.Equal(t, "imported-inbox", orig)
	trash, err := target.TrashMailbox(context.Background(), alice)
	require.NoError(t, err)
	require.Equal(t, "native-trash", trash)
	before, err := target.ReadLocation(context.Background(), alice, "native-original")
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"imported-inbox": true}, before.MailboxIDs)
	err = target.MoveLocation(context.Background(), alice, "native-original", before, map[string]bool{trash: true})
	require.NoError(t, err)
	require.Equal(t, 1, setCalls)
	require.GreaterOrEqual(t, mailboxCalls, 3)
	require.GreaterOrEqual(t, getCalls, 3)
	_, err = target.ReadLocation(context.Background(), bob, "native-original")
	require.Error(t, err) // Bob cannot read Alice's native ID in this staged responder.
}
func TestLocationNativeMovePreconditionStopsBeforeEmailSet(t *testing.T) {
	setCalls := 0
	workspace, _, _, alice, _ := workspaceFixture(t, func(method string, args map[string]any) any {
		switch method {
		case "Mailbox/get":
			return map[string]any{"list": []any{map[string]any{"id": "native-trash", "name": "Trash", "role": "trash"}, map[string]any{"id": "imported-inbox", "name": "Imported", "role": nil}}}
		case "Email/get":
			return map[string]any{"state": "changed-state", "list": []any{map[string]any{"id": "native-original", "mailboxIds": map[string]bool{"imported-inbox": true}}}}
		case "Email/set":
			setCalls++
			return map[string]any{"updated": map[string]any{"native-original": nil}}
		default:
			t.Fatalf("unexpected method %s", method)
			return nil
		}
	})
	native := stalwartMailNativeLocation{read: workspace.read}
	expected := MailNativeLocationSnapshot{MailboxIDs: map[string]bool{"imported-inbox": true}, State: "previous-state"}
	err := native.MoveLocation(context.Background(), alice, "native-original", expected, map[string]bool{"native-trash": true})
	require.True(t, errors.Is(err, ErrMailFlagPrecondition))
	require.Zero(t, setCalls)
	err = native.MoveLocation(context.Background(), alice, "native-original", expected, map[string]bool{"attacker-injected": true})
	require.True(t, errors.Is(err, ErrMailFlagPrecondition))
	require.Zero(t, setCalls)
}
func TestLocationNativeMoveNoAutoRetryOnJMAPConflict(t *testing.T) {
	writes := 0
	workspace, _, _, alice, _ := workspaceFixture(t, func(method string, args map[string]any) any {
		switch method {
		case "Mailbox/get":
			return map[string]any{"list": []any{map[string]any{"id": "native-trash", "name": "Trash", "role": "trash"}, map[string]any{"id": "imported-inbox", "name": "Imported", "role": nil}}}
		case "Email/get":
			return map[string]any{"state": "state-before", "list": []any{map[string]any{"id": "native-original", "mailboxIds": map[string]bool{"imported-inbox": true}}}}
		case "Email/set":
			writes++
			require.Equal(t, "state-before", args["ifInState"])
			return map[string]any{"notUpdated": map[string]any{"native-original": map[string]any{"type": "stateMismatch"}}}
		default:
			t.Fatalf("unexpected method %s", method)
			return nil
		}
	})
	native := stalwartMailNativeLocation{read: workspace.read}
	before, err := native.ReadLocation(context.Background(), alice, "native-original")
	require.NoError(t, err)
	err = native.MoveLocation(context.Background(), alice, "native-original", before, map[string]bool{"native-trash": true})
	require.Error(t, err)
	require.Equal(t, 1, writes)
	after, err := native.ReadLocation(context.Background(), alice, "native-original")
	require.NoError(t, err)
	require.True(t, reflect.DeepEqual(before.MailboxIDs, after.MailboxIDs))
}
