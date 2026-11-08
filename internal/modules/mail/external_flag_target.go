package mail

import (
	"atoman/internal/platform/authctx"
	"context"
	"errors"
)

type stalwartMailFlagReader struct{ read *MailReadService }

func (r stalwartMailFlagReader) ReadFlags(ctx context.Context, user authctx.CurrentUser, ids []string) (map[string]ExternalFlagSnapshot, error) {
	out := make(map[string]ExternalFlagSnapshot, len(ids))
	if r.read == nil || r.read.client == nil || len(ids) == 0 || len(ids) > externalFlagPreviewLimit {
		return out, errors.New("Invalid native flag lookup")
	}
	for _, id := range ids {
		if !validJMAPScopedID(id) {
			return out, errors.New("Invalid native message ID")
		}
	}
	cred, accountID, err := r.read.authorizedClient(ctx, user)
	if err != nil {
		return out, err
	}
	var got struct {
		List []struct {
			ID       string          `json:"id"`
			Keywords map[string]bool `json:"keywords"`
		} `json:"list"`
		NotFound []string `json:"notFound"`
	}
	err = r.read.client.call(ctx, cred, false, "Email/get", map[string]any{"accountId": accountID, "ids": ids, "properties": []string{"id", "keywords"}}, &got)
	if err != nil {
		return out, errors.New("Cannot read native flags")
	}
	expected := make(map[string]bool, len(ids))
	for _, id := range ids {
		expected[id] = true
	}
	if len(got.List) > len(ids) {
		return out, errors.New("Native response overflow")
	}
	for _, item := range got.List {
		if !expected[item.ID] {
			return out, errors.New("Unexpected native message ID")
		}
		if _, duplicate := out[item.ID]; duplicate {
			return out, errors.New("Duplicate native message ID")
		}
		out[item.ID] = ExternalFlagSnapshot{Seen: item.Keywords["$seen"], Flagged: item.Keywords["$flagged"]}
	}
	return out, nil
}
