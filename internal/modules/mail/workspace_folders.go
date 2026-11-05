package mail

import (
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"context"
	"strings"
	"unicode/utf8"
)

func (s *MailWorkspaceService) CreateFolder(ctx context.Context, u authctx.CurrentUser, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 || !utf8.ValidString(name) || strings.ContainsAny(name, "\r\n\x00") {
		return "", apperr.BadRequest("mail.invalid_folder_name", "Folder name must contain 1 to 100 bytes")
	}
	cred, aid, e := s.auth(ctx, u)
	if e != nil {
		return "", e
	}
	var got struct {
		List  []MailboxInfo `json:"list"`
		State string        `json:"state"`
	}
	e = s.read.client.call(ctx, cred, false, "Mailbox/get", map[string]any{"accountId": aid, "ids": nil, "properties": []string{"id", "name", "role"}}, &got)
	if e != nil {
		return "", workspaceErr(e)
	}
	if len(got.List) >= 100 {
		return "", apperr.Conflict("mail.folder_limit", "Mailbox folder limit reached")
	}
	for _, f := range got.List {
		if strings.EqualFold(f.Name, name) {
			return "", apperr.Conflict("mail.folder_exists", "A folder with this name already exists")
		}
	}
	var result struct {
		Created map[string]struct {
			ID string `json:"id"`
		} `json:"created"`
	}
	e = s.read.client.call(ctx, cred, false, "Mailbox/set", map[string]any{"accountId": aid, "ifInState": got.State, "create": map[string]any{"folder": map[string]any{"name": name, "parentId": nil, "isSubscribed": true}}}, &result)
	if e != nil {
		return "", workspaceErr(e)
	}
	id := result.Created["folder"].ID
	if !validJMAPScopedID(id) {
		return "", apperr.Conflict("mail.folder_create_failed", "Folder could not be created; refresh folders")
	}
	return id, nil
}

func (s *MailWorkspaceService) DeleteFolder(ctx context.Context, u authctx.CurrentUser, id string) error {
	if !validJMAPScopedID(id) {
		return apperr.BadRequest("mail.invalid_folder", "Invalid folder")
	}
	cred, aid, e := s.auth(ctx, u)
	if e != nil {
		return e
	}
	var got struct {
		List  []MailboxInfo `json:"list"`
		State string        `json:"state"`
	}
	e = s.read.client.call(ctx, cred, false, "Mailbox/get", map[string]any{"accountId": aid, "ids": nil, "properties": []string{"id", "role", "parentId", "totalEmails"}}, &got)
	if e != nil {
		return workspaceErr(e)
	}
	var target *MailboxInfo
	for i := range got.List {
		f := &got.List[i]
		if f.ID == id {
			target = f
		}
		if f.ParentID != nil && *f.ParentID == id {
			return apperr.Conflict("mail.folder_has_children", "Delete child folders first")
		}
	}
	if target == nil {
		return apperr.NotFound("mail.folder_not_found", "Folder unavailable")
	}
	if target.Role != nil && *target.Role != "" {
		return apperr.Forbidden("mail.system_folder", "System folders cannot be deleted")
	}
	if target.TotalEmails != 0 {
		return apperr.Conflict("mail.folder_not_empty", "Only empty custom folders can be deleted")
	}
	var result struct {
		Destroyed []string `json:"destroyed"`
	}
	// Stalwart rechecks emptiness atomically. No message deletion is requested.
	e = s.read.client.call(ctx, cred, false, "Mailbox/set", map[string]any{"accountId": aid, "ifInState": got.State, "destroy": []string{id}, "onDestroyRemoveEmails": false}, &result)
	if e != nil {
		return workspaceErr(e)
	}
	if len(result.Destroyed) != 1 || result.Destroyed[0] != id {
		return apperr.Conflict("mail.folder_delete_failed", "Folder changed or could not be deleted")
	}
	return nil
}
