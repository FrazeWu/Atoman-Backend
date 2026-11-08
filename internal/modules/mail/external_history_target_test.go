package mail

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStalwartHistoryImportCreatesOneScopedFolderAndPreservesMIMEAndFlags(t *testing.T) {
	name := "Imported gmail - INBOX [1234567890]"
	folderID := "native-import-id"
	created := false
	uploaded := false
	imports := 0
	workspace, _, _, alice, bob := workspaceFixture(t, func(method string, args map[string]any) any {
		switch method {
		case "Mailbox/get":
			if args["accountId"] == "workspace-bob-account" {
				return map[string]any{"state": "bob-state", "list": []map[string]any{{"id": "bob-owned-inbox", "name": "Inbox", "role": "inbox", "parentId": nil}}}
			}
			list := []map[string]any{{"id": "native-inbox", "name": "Inbox", "role": "inbox", "parentId": nil}}
			if created {
				list = append(list, map[string]any{"id": folderID, "name": name, "parentId": nil})
			}
			return map[string]any{"state": "test-state", "list": list}
		case "Mailbox/set":
			input := args["create"].(map[string]any)["history"].(map[string]any)
			require.Equal(t, name, input["name"])
			require.Nil(t, input["parentId"])
			require.False(t, created)
			created = true
			return map[string]any{"created": map[string]any{"history": map[string]any{"id": folderID}}}
		case "upload":
			body := args["body"].(string)
			require.Contains(t, body, "Message-ID: <historical@test.example>")
			require.Contains(t, body, "Attachment-marker")
			require.Equal(t, "message/rfc822", args["type"])
			uploaded = true
			return nil
		case "Email/import":
			require.True(t, uploaded)
			imports++
			input := args["emails"].(map[string]any)["history"].(map[string]any)
			require.Equal(t, "test-blob", input["blobId"])
			require.Equal(t, true, input["mailboxIds"].(map[string]any)[folderID])
			require.Equal(t, true, input["keywords"].(map[string]any)["$seen"])
			require.Equal(t, true, input["keywords"].(map[string]any)["$flagged"])
			require.Equal(t, "2020-01-02T03:04:05Z", input["receivedAt"])
			return map[string]any{"created": map[string]any{"history": map[string]any{"id": "native-email-id"}}}
		}
		return map[string]any{}
	})
	target := stalwartExternalHistoryTarget{read: workspace.read}
	ctx := context.Background()
	id, e := target.EnsureFolder(ctx, alice, name)
	require.NoError(t, e)
	require.Equal(t, folderID, id)
	_, e = target.EnsureFolder(ctx, alice, name)
	require.NoError(t, e)
	raw := []byte(strings.Join([]string{
		"From: sender@test.example", "To: recipient@test.example", "Message-ID: <historical@test.example>",
		"MIME-Version: 1.0", "Content-Type: text/plain; charset=utf-8", "", "Attachment-marker",
	}, "\r\n"))
	result, uncertain, e := target.ImportRaw(ctx, alice, id, raw, time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC), true, true)
	require.NoError(t, e)
	require.False(t, uncertain)
	require.Equal(t, "native-email-id", result)
	require.Equal(t, 1, imports)
	_, uncertain, e = target.ImportRaw(ctx, bob, id, raw, time.Now(), false, false)
	require.Error(t, e)
	require.False(t, uncertain)
	require.Equal(t, 1, imports)
}
func TestStalwartHistoryImportRejectionIsUncertainAfterAttempt(t *testing.T) {
	workspace, _, _, alice, _ := workspaceFixture(t, func(method string, args map[string]any) any {
		switch method {
		case "Mailbox/get":
			return map[string]any{"list": []map[string]any{{"id": "native-import-id", "name": "import", "parentId": nil}}, "state": "x"}
		case "upload":
			return nil
		case "Email/import":
			return map[string]any{"notCreated": map[string]any{"history": map[string]any{"type": "overQuota"}}}
		}
		return map[string]any{}
	})
	target := stalwartExternalHistoryTarget{read: workspace.read}
	_, uncertain, e := target.ImportRaw(context.Background(), alice, "native-import-id", []byte("Subject: hi\r\n\r\nbody"), time.Time{}, false, false)
	require.Error(t, e)
	require.True(t, uncertain)
}
