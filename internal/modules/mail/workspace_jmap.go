package mail

import (
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Explicit capability envelopes for Sieve and account-security operations.
func (s *StalwartJMAPClient) capabilityCall(ctx context.Context, cred StalwartCredential, capability, method string, args, target any) error {
	req := map[string]any{"using": []string{jmapCoreCapability, capability}, "methodCalls": []any{[]any{method, args, "c1"}}}
	var out struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	if e := s.request(ctx, cred, http.MethodPost, "/jmap/", req, &out); e != nil {
		return e
	}
	if len(out.MethodResponses) != 1 || len(out.MethodResponses[0]) != 3 {
		return errors.New("invalid method response")
	}
	var name, id string
	_ = json.Unmarshal(out.MethodResponses[0][0], &name)
	_ = json.Unmarshal(out.MethodResponses[0][2], &id)
	if name != method || id != "c1" {
		return errors.New("method failed")
	}
	return json.Unmarshal(out.MethodResponses[0][1], target)
}
func (s *StalwartJMAPClient) uploadBlob(ctx context.Context, cred StalwartCredential, aid, ct string, data []byte) (string, error) {
	if len(data) == 0 || len(data) > maxMailSnapshotBytes || !validJMAPScopedID(aid) {
		return "", errors.New("invalid upload")
	}
	h, e := cred.authorization()
	if e != nil {
		return "", e
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/jmap/upload/"+url.PathEscape(aid)+"/", bytes.NewReader(data))
	if e != nil {
		return "", e
	}
	req.Header.Set("Authorization", h)
	req.Header.Set("Content-Type", ct)
	res, e := s.http.Do(req)
	if e != nil {
		return "", errors.New("upload failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 && res.StatusCode != 201 {
		return "", fmt.Errorf("upload status %d", res.StatusCode)
	}
	var out struct {
		AccountID string `json:"accountId"`
		BlobID    string `json:"blobId"`
	}
	e = json.NewDecoder(io.LimitReader(res.Body, 16384)).Decode(&out)
	if e != nil || out.AccountID != aid || !validJMAPScopedID(out.BlobID) {
		return "", errors.New("invalid blob scope")
	}
	return out.BlobID, nil
}
func (s *StalwartJMAPClient) downloadBlob(ctx context.Context, cred StalwartCredential, aid, blob string) ([]byte, error) {
	if !validJMAPScopedID(aid) || !validJMAPScopedID(blob) {
		return nil, errors.New("invalid blob")
	}
	h, e := cred.authorization()
	if e != nil {
		return nil, e
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, s.base+"/jmap/download/"+url.PathEscape(aid)+"/"+url.PathEscape(blob)+"/download?accept=application%2Foctet-stream", nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", h)
	res, e := s.http.Do(req)
	if e != nil {
		return nil, errors.New("download failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, errors.New("blob unavailable")
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, maxMailMIMEBytes+1))
	if e != nil || len(raw) > maxMailMIMEBytes {
		return nil, errors.New("blob too large")
	}
	return raw, nil
}

type MailSearchResult struct {
	Items    []EmailSummary `json:"items"`
	Total    uint64         `json:"total"`
	Position uint64         `json:"position"`
	HasMore  bool           `json:"has_more"`
}

func (s *MailWorkspaceService) Search(ctx context.Context, u authctx.CurrentUser, folder, q, thread string, position, limit int) (MailSearchResult, error) {
	if position < 0 || position > 100000 || limit < 1 || limit > 100 || len(q) > 512 {
		return MailSearchResult{}, apperr.BadRequest("mail.invalid_query", "Invalid search")
	}
	cred, aid, e := s.auth(ctx, u)
	if e != nil {
		return MailSearchResult{}, e
	}
	filter := map[string]any{}
	if folder != "" {
		if !validJMAPScopedID(folder) {
			return MailSearchResult{}, apperr.BadRequest("mail.invalid_folder", "Invalid folder")
		}
		filter["inMailbox"] = folder
	}
	if q != "" {
		filter["text"] = q
	}
	var query struct {
		IDs      []string `json:"ids"`
		Total    uint64   `json:"total"`
		Position uint64   `json:"position"`
	}
	if thread != "" {
		if !validJMAPScopedID(thread) {
			return MailSearchResult{}, apperr.BadRequest("mail.invalid_thread", "Invalid conversation")
		}
		var got struct {
			List []struct {
				ID       string   `json:"id"`
				EmailIDs []string `json:"emailIds"`
			} `json:"list"`
		}
		e = s.read.client.call(ctx, cred, false, "Thread/get", map[string]any{"accountId": aid, "ids": []string{thread}}, &got)
		if e != nil {
			return MailSearchResult{}, workspaceErr(e)
		}
		if len(got.List) != 1 || got.List[0].ID != thread {
			return MailSearchResult{}, apperr.NotFound("mail.thread_missing", "Conversation unavailable")
		}
		all := got.List[0].EmailIDs
		query.Total = uint64(len(all))
		query.Position = uint64(position)
		if position < len(all) {
			end := position + limit
			if end > len(all) {
				end = len(all)
			}
			query.IDs = all[position:end]
		}
	} else {
		e = s.read.client.call(ctx, cred, false, "Email/query", map[string]any{"accountId": aid, "filter": filter, "sort": []map[string]any{{"property": "receivedAt", "isAscending": false}}, "position": position, "limit": limit, "calculateTotal": true}, &query)
		if e != nil {
			return MailSearchResult{}, workspaceErr(e)
		}
	}
	out := MailSearchResult{Items: []EmailSummary{}, Total: query.Total, Position: query.Position, HasMore: query.Position+uint64(len(query.IDs)) < query.Total}
	if len(query.IDs) == 0 {
		return out, nil
	}
	if len(query.IDs) > limit {
		return out, workspaceErr(errors.New("too many messages"))
	}
	var got struct {
		List []EmailSummary `json:"list"`
	}
	e = s.read.client.call(ctx, cred, false, "Email/get", map[string]any{"accountId": aid, "ids": query.IDs, "properties": []string{"id", "threadId", "subject", "receivedAt", "preview", "hasAttachment", "from", "to", "keywords", "mailboxIds"}}, &got)
	if e != nil {
		return out, workspaceErr(e)
	}
	byID := map[string]EmailSummary{}
	for _, m := range got.List {
		m.IsRead = m.Keywords["$seen"]
		m.IsStarred = m.Keywords["$flagged"]
		byID[m.ID] = m
	}
	for _, id := range query.IDs {
		if m, ok := byID[id]; ok {
			out.Items = append(out.Items, m)
		}
	}
	return out, nil
}
func (s *MailWorkspaceService) Download(ctx context.Context, u authctx.CurrentUser, emailID, partID string, raw bool) ([]byte, string, string, error) {
	if !validJMAPScopedID(emailID) || (!raw && !validJMAPScopedID(partID)) {
		return nil, "", "", apperr.BadRequest("mail.invalid_id", "Invalid attachment")
	}
	cred, aid, e := s.auth(ctx, u)
	if e != nil {
		return nil, "", "", e
	}
	var got struct {
		List []struct {
			ID          string `json:"id"`
			BlobID      string `json:"blobId"`
			Attachments []struct {
				PartID string `json:"partId"`
				BlobID string `json:"blobId"`
				Name   string `json:"name"`
				Type   string `json:"type"`
			} `json:"attachments"`
		} `json:"list"`
	}
	e = s.read.client.call(ctx, cred, false, "Email/get", map[string]any{"accountId": aid, "ids": []string{emailID}, "properties": []string{"id", "blobId", "attachments"}, "bodyProperties": []string{"partId", "blobId", "name", "type", "size"}}, &got)
	if e != nil || len(got.List) != 1 || got.List[0].ID != emailID {
		return nil, "", "", apperr.NotFound("mail.message_missing", "Message unavailable")
	}
	blob, name, ct := got.List[0].BlobID, "message.eml", "message/rfc822"
	if !raw {
		blob = ""
		for _, a := range got.List[0].Attachments {
			if a.PartID == partID {
				blob, name, ct = a.BlobID, a.Name, a.Type
				break
			}
		}
	}
	if blob == "" {
		return nil, "", "", apperr.NotFound("mail.attachment_missing", "Attachment unavailable")
	}
	data, e := s.read.client.downloadBlob(ctx, cred, aid, blob)
	if e != nil {
		return nil, "", "", workspaceErr(e)
	}
	name = strings.ReplaceAll(strings.ReplaceAll(name, "\r", ""), "\n", "")
	if name == "" {
		name = "attachment"
	}
	return data, name, ct, nil
}
