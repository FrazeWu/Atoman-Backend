package mail

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNativeConditionalFlagWriterUsesJMAPStateAndNarrowKeywordPatch(t *testing.T) {
	stage := 0
	writeCalls := 0
	workspace, _, _, alice, _ := workspaceFixture(t, func(method string, args map[string]any) any {
		switch method {
		case "Email/get":
			keywords := map[string]any{"$flagged": true}
			state := "state-before"
			if stage > 0 {
				keywords["$seen"] = true
				state = "state-after"
			}
			require.Equal(t, []any{"imported-id"}, args["ids"])
			return map[string]any{"state": state, "list": []any{map[string]any{"id": "imported-id", "keywords": keywords}}}
		case "Email/set":
			writeCalls++
			require.Equal(t, "state-before", args["ifInState"])
			update := args["update"].(map[string]any)
			require.Len(t, update, 1)
			patch := update["imported-id"].(map[string]any)
			require.Len(t, patch, 1)
			require.Equal(t, true, patch["keywords/$seen"])
			stage = 1
			return map[string]any{"updated": map[string]any{"imported-id": nil}}
		default:
			t.Fatalf("unexpected JMAP method %s", method)
			return nil
		}
	})
	writer := stalwartMailFlagWriter{read: workspace.read}
	err := writer.SetFlagIfUnchanged(context.Background(), alice, "imported-id", "seen", ExternalFlagSnapshot{Seen: false, Flagged: true}, true)
	require.NoError(t, err)
	require.Equal(t, 1, writeCalls)
	// The same stale expected flag state must never trigger a second JMAP/set.
	err = writer.SetFlagIfUnchanged(context.Background(), alice, "imported-id", "seen", ExternalFlagSnapshot{Seen: false, Flagged: true}, true)
	require.Error(t, err)
	require.Equal(t, 1, writeCalls)
}
func TestNativeConditionalFlagWriterUsesNullToRemoveOnlyTargetKeyword(t *testing.T) {
	stage := 0
	workspace, _, _, alice, _ := workspaceFixture(t, func(method string, args map[string]any) any {
		switch method {
		case "Email/get":
			keywords := map[string]any{"$seen": true, "$flagged": true, "custom_label": true}
			state := "state-start"
			if stage > 0 {
				delete(keywords, "$seen")
				state = "state-new"
			}
			return map[string]any{"state": state, "list": []any{map[string]any{"id": "native-id", "keywords": keywords}}}
		case "Email/set":
			require.Equal(t, "state-start", args["ifInState"])
			update := args["update"].(map[string]any)["native-id"].(map[string]any)
			require.Len(t, update, 1)
			actual, exists := update["keywords/$seen"]
			require.True(t, exists)
			require.Nil(t, actual)
			stage = 1
			return map[string]any{"updated": map[string]any{"native-id": nil}}
		default:
			t.Fatalf("unexpected method %s", method)
			return nil
		}
	})
	writer := stalwartMailFlagWriter{read: workspace.read}
	err := writer.SetFlagIfUnchanged(context.Background(), alice, "native-id", "seen", ExternalFlagSnapshot{Seen: true, Flagged: true}, false)
	require.NoError(t, err)
}
func TestNativeConditionalFlagWriterRejectsJMAPConflictWithoutBlindRetry(t *testing.T) {
	writes := 0
	workspace, _, _, alice, _ := workspaceFixture(t, func(method string, args map[string]any) any {
		switch method {
		case "Email/get":
			return map[string]any{"state": "state-current", "list": []any{map[string]any{"id": "native-id", "keywords": map[string]bool{}}}}
		case "Email/set":
			writes++
			require.Equal(t, "state-current", args["ifInState"])
			return map[string]any{"notUpdated": map[string]any{"native-id": map[string]any{"type": "stateMismatch"}}}
		default:
			t.Fatalf("unexpected method %s", method)
			return nil
		}
	})
	writer := stalwartMailFlagWriter{read: workspace.read}
	err := writer.SetFlagIfUnchanged(context.Background(), alice, "native-id", "seen", ExternalFlagSnapshot{}, true)
	require.Error(t, err)
	require.Equal(t, 1, writes)
}
