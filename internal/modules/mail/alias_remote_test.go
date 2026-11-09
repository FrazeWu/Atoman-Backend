package mail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStalwartAliasDecoderMatchesLiveNumericKeyedObject(t *testing.T) {
	for _, input := range []string{
		`{}`, `[]`, `null`,
		`{"0":{"name":"one","domainId":"domain","enable":true},"1":{"name":"two","domainId":"domain","enable":false}}`,
		`[{"name":"one","domainId":"domain","enable":true}]`,
	} {
		rows, err := decodeStalwartAccountAliases(json.RawMessage(input))
		require.NoError(t, err, input)
		if input[0] == '{' && len(input) > 2 {
			require.Len(t, rows, 2)
			require.Equal(t, "one", rows[0].Name)
			require.Equal(t, "two", rows[1].Name)
		}
	}
}
func TestStalwartAliasDecoderRejectsMissingProperty(t *testing.T) {
	_, err := decodeStalwartAccountAliases(nil)
	require.Error(t, err)
}

func TestStalwartAliasDecoderRejectsSparseNonNumericAndMalformedSlots(t *testing.T) {
	for _, input := range []string{
		`{"1":{"name":"ghost","domainId":"domain","enable":true}}`,
		`{"01":{"name":"ghost","domainId":"domain","enable":true}}`,
		`{"-1":{"name":"ghost","domainId":"domain","enable":true}}`,
		`{"admin":{"name":"ghost","domainId":"domain","enable":true}}`,
		`{"0":{"name":"ghost","domainId":"","enable":true}}`,
		`"not an array"`,
		`{"0":"untrusted"}`,
	} {
		_, err := decodeStalwartAccountAliases(json.RawMessage(input))
		require.Error(t, err, input)
	}
}
func TestStalwartAliasRemoteReadsMapThenMutatesOnlyMatchingSlot(t *testing.T) {
	aliases := map[string]any{}
	writes := []map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "/jmap/", r.URL.Path)
		user, passwd, ok := r.BasicAuth()
		require.True(t, ok)
		require.Equal(t, "admin", user)
		require.Equal(t, "secret", passwd)
		var request struct {
			MethodCalls [][]json.RawMessage `json:"methodCalls"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Len(t, request.MethodCalls, 1)
		var method string
		require.NoError(t, json.Unmarshal(request.MethodCalls[0][0], &method))
		var args map[string]any
		require.NoError(t, json.Unmarshal(request.MethodCalls[0][1], &args))
		result := map[string]any{}
		switch method {
		case "x:Account/get":
			ids, ok := args["ids"].([]any)
			require.True(t, ok)
			require.Equal(t, []any{"owner-id"}, ids)
			result["list"] = []any{map[string]any{"id": "owner-id", "aliases": aliases}}
		case "x:Account/set":
			changes := args["update"].(map[string]any)
			require.Len(t, changes, 1)
			patch := changes["owner-id"].(map[string]any)
			require.Len(t, patch, 1)
			writes = append(writes, patch)
			if v, ok := patch["aliases/0"]; ok {
				aliases["0"] = v
			}
			if v, ok := patch["aliases/0/enable"]; ok {
				aliases["0"].(map[string]any)["enable"] = v
			}
			result["updated"] = map[string]any{"owner-id": nil}
		default:
			t.Fatalf("Unexpected admin method %s", method)
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"methodResponses": []any{[]any{method, result, "c1"}}}))
	}))
	defer server.Close()
	client, err := NewStalwartJMAPClient(server.URL, nil)
	require.NoError(t, err)
	admin := StalwartBasic("admin", "secret")
	slot, err := client.AddAlias(context.Background(), admin, "owner-id", "domain-id", "test-alias")
	require.NoError(t, err)
	require.Equal(t, uint32(0), slot)
	require.Len(t, writes, 1)
	require.Equal(t, map[string]any{"aliases/0": map[string]any{"name": "test-alias", "domainId": "domain-id", "enable": true}}, writes[0])
	err = client.SetAliasEnabled(context.Background(), admin, "owner-id", "domain-id", "test-alias", slot, false)
	require.NoError(t, err)
	require.Len(t, writes, 2)
	require.Equal(t, map[string]any{"aliases/0/enable": false}, writes[1])
	err = client.SetAliasEnabled(context.Background(), admin, "owner-id", "domain-id", "different-name", slot, true)
	require.Error(t, err)
	require.Len(t, writes, 2)
	err = client.SetAliasEnabled(context.Background(), admin, "owner-id", "domain-id", "test-alias", slot, true)
	require.NoError(t, err)
	require.Len(t, writes, 3)
	require.Equal(t, map[string]any{"aliases/0/enable": true}, writes[2])
}
