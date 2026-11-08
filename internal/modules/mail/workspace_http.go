package mail

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"mime"
	"net/http"
	"strconv"
	"time"
)

func (h *AccountHandler) registerWorkspace(g *gin.RouterGroup) {
	g.GET("/aliases", h.listMailAliases)
	g.POST("/aliases", h.createMailAlias)
	g.POST("/aliases/:id/action", h.actionMailAlias)
	g.GET("/external/providers", h.externalProviders)
	g.GET("/external/accounts", h.listExternalAccounts)
	g.POST("/external/accounts", h.connectExternalAccount)
	g.POST("/external/accounts/:id/sync", h.syncExternalAccount)
	g.DELETE("/external/accounts/:id", h.deleteExternalAccount)
	g.GET("/external/unified", h.unifiedExternalInbox)
	g.GET("/external/accounts/:id/imports", h.externalHistoryList)
	g.POST("/external/accounts/:id/imports", h.externalHistoryStart)
	g.POST("/external/accounts/:id/imports/:job_id/action", h.externalHistoryAction)
	g.GET("/external/accounts/:id/imports/:job_id/flag-preview", h.externalFlagPreview)
	g.POST("/external/accounts/:id/imports/:job_id/flag-sync", h.externalFlagSyncConfig)
	g.GET("/external/accounts/:id/imports/:job_id/flag-attempts", h.externalFlagSyncAttempts)
	g.POST("/external/accounts/:id/imports/:job_id/flag-attempts/:attempt_id/reconcile", h.externalFlagSyncReconcile)
	g.GET("/external/accounts/:id/folders", h.listExternalFolders)
	g.GET("/external/accounts/:id/folder-messages", h.browseExternalFolder)
	g.GET("/external/accounts/:id/messages/:uid", h.readExternalMessage)
	g.POST("/external/accounts/:id/messages/:uid/seen", h.updateExternalSeen)
	g.POST("/external/accounts/:id/messages/:uid/move", h.moveExternalMessage)
	g.GET("/search", h.searchMail)
	g.GET("/unread", h.unreadMail)
	g.GET("/organize", h.mailOrganizerState)
	g.POST("/organize/labels", h.createMailOrganizerLabel)
	g.PUT("/organize/labels/:id", h.updateMailOrganizerLabel)
	g.DELETE("/organize/labels/:id", h.deleteMailOrganizerLabel)
	g.POST("/organize/messages", h.mutateMailOrganizerMessage)
	g.GET("/contacts", h.listContacts)
	g.POST("/contacts", h.createContact)
	g.PUT("/contacts/:id", h.updateContact)
	g.DELETE("/contacts/:id", h.deleteContact)
	g.POST("/folders", h.createMailFolder)
	g.DELETE("/folders/:id", h.deleteMailFolder)
	g.POST("/messages/actions", h.mailActions)
	g.POST("/messages/actions/undo", h.undoMailAction)
	g.GET("/attachment", h.downloadAttachment)
	g.GET("/raw", h.downloadRaw)
	g.GET("/drafts", h.listDrafts)
	g.GET("/drafts/:id", h.getDraft)
	g.PUT("/drafts/:id", h.saveDraft)
	g.DELETE("/drafts/:id", h.deleteDraft)
	g.POST("/attachments", h.uploadAttachment)
	g.DELETE("/attachments/:id", h.deleteAttachment)
	g.GET("/settings", h.getMailSettings)
	g.PUT("/settings", h.saveMailSettings)
	g.GET("/app-passwords", h.listAppPasswords)
	g.POST("/app-passwords", h.createAppPassword)
	g.DELETE("/app-passwords/:id", h.deleteAppPassword)
	g.GET("/outbox", h.listOutbox)
	g.POST("/outbox/:id/reconcile", h.reconcileOutbox)
	g.POST("/provision", h.provisionSelf)
	g.POST("/admin/provision", h.provisionAccount)
}
func (h *AccountHandler) workspaceUser(c *gin.Context) (authctx.CurrentUser, bool) {
	u, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return u, false
	}
	if h.workspace == nil {
		httpx.Error(c, apperr.New(503, "mail.workspace_not_ready", "Mail workspace is not enabled", nil))
		return u, false
	}
	return u, true
}
func parseMailUUID(c *gin.Context, raw string) (uuid.UUID, bool) {
	id, e := uuid.Parse(raw)
	if e != nil || id == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("mail.invalid_identifier", "Invalid mail identifier"))
		return uuid.Nil, false
	}
	return id, true
}
func mailJSON(c *gin.Context, v any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	return httpx.BindRequiredJSON(c, v)
}
func mailResult(c *gin.Context, status int, v any, e error) {
	if e != nil {
		httpx.Error(c, e)
		return
	}
	httpx.OK(c, status, v)
}
func messageQueryID(c *gin.Context) string {
	if id := c.Query("message_id"); id != "" {
		return id
	}
	return c.Query("email_id")
}

// unreadMail reports the authenticated mailbox's inbox count; no global count
// or other user's Stalwart identifiers are accepted from the browser.
func (h *AccountHandler) unreadMail(c *gin.Context) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	credential, accountID, err := h.workspace.read.authorizedClient(c.Request.Context(), u)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	folders, err := h.workspace.read.client.Mailboxes(c.Request.Context(), credential, accountID)
	if err != nil {
		httpx.Error(c, apperr.New(503, "mail.upstream_unavailable", "Mailbox unavailable", nil))
		return
	}
	var count uint64
	var latestID string
	for _, folder := range folders {
		if folder.Role != nil && *folder.Role == "inbox" {
			count = folder.UnreadEmails
			newest, e := h.workspace.read.client.ListEmails(c.Request.Context(), credential, accountID, folder.ID, 1)
			if e == nil && len(newest) > 0 {
				latestID = newest[0].ID
			}
			break
		}
	}
	httpx.OK(c, 200, gin.H{"count": count, "latest_id": latestID})
}

func (h *AccountHandler) listContacts(c *gin.Context) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	v, e := h.workspace.Contacts(c.Request.Context(), u)
	mailResult(c, 200, v, e)
}
func (h *AccountHandler) createContact(c *gin.Context) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	var in MailContactInput
	if !mailJSON(c, &in) {
		return
	}
	v, e := h.workspace.SaveContact(c.Request.Context(), u, uuid.Nil, in)
	mailResult(c, 201, v, e)
}
func (h *AccountHandler) updateContact(c *gin.Context) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	var in MailContactInput
	if !mailJSON(c, &in) {
		return
	}
	v, e := h.workspace.SaveContact(c.Request.Context(), u, id, in)
	mailResult(c, 200, v, e)
}
func (h *AccountHandler) deleteContact(c *gin.Context) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	e := h.workspace.DeleteContact(c.Request.Context(), u, id)
	mailResult(c, 200, gin.H{"deleted": e == nil}, e)
}
func (h *AccountHandler) searchMail(c *gin.Context) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	p, _ := strconv.Atoi(c.DefaultQuery("position", "0"))
	l, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	options := MailSearchOptions{Folder: c.Query("mailbox_id"), Text: c.Query("q"), Thread: c.Query("thread_id"), From: c.Query("from"), To: c.Query("to"), Subject: c.Query("subject"), After: c.Query("after"), Before: c.Query("before"), Position: p, Limit: l}
	for _, name := range []string{"unread", "starred", "attachment"} {
		raw, exists := c.GetQuery(name)
		if !exists {
			continue
		}
		val, err := strconv.ParseBool(raw)
		if err != nil {
			httpx.Error(c, apperr.BadRequest("mail.invalid_query", "Invalid search filter"))
			return
		}
		switch name {
		case "unread":
			options.Unread = &val
		case "starred":
			options.Starred = &val
		case "attachment":
			options.Attachment = &val
		}
	}
	v, e := h.workspace.SearchAdvanced(c.Request.Context(), u, options)
	mailResult(c, 200, v, e)
}
func (h *AccountHandler) mailActions(c *gin.Context) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	var in MailActionInput
	if !mailJSON(c, &in) {
		return
	}
	v, e := h.workspace.Actions(c.Request.Context(), u, in)
	mailResult(c, 200, v, e)
}
func (h *AccountHandler) undoMailAction(c *gin.Context) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if !mailJSON(c, &in) {
		return
	}
	id, ok := parseMailUUID(c, in.Token)
	if !ok {
		return
	}
	v, e := h.workspace.UndoAction(c.Request.Context(), u, id)
	mailResult(c, 200, v, e)
}
func (h *AccountHandler) downloadAttachment(c *gin.Context) { h.downloadMail(c, false) }
func (h *AccountHandler) downloadRaw(c *gin.Context)        { h.downloadMail(c, true) }
func (h *AccountHandler) downloadMail(c *gin.Context, raw bool) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	b, name, ct, e := h.workspace.Download(c.Request.Context(), u, messageQueryID(c), c.Query("part_id"), raw)
	if e != nil {
		httpx.Error(c, e)
		return
	}
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "sandbox; default-src 'none'")
	c.Data(200, ct, b)
}
func (h *AccountHandler) listDrafts(c *gin.Context) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	v, e := h.workspace.Drafts(c.Request.Context(), u)
	mailResult(c, 200, v, e)
}
func (h *AccountHandler) getDraft(c *gin.Context) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	v, e := h.workspace.Draft(c.Request.Context(), u, id)
	mailResult(c, 200, v, e)
}
func (h *AccountHandler) saveDraft(c *gin.Context) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	var in SaveMailDraftInput
	if !mailJSON(c, &in) {
		return
	}
	v, e := h.workspace.SaveDraft(c.Request.Context(), u, id, in)
	mailResult(c, 200, v, e)
}
func (h *AccountHandler) deleteDraft(c *gin.Context) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	e := h.workspace.DeleteDraft(c.Request.Context(), u, id)
	mailResult(c, 200, gin.H{"deleted": e == nil}, e)
}
func (h *AccountHandler) uploadAttachment(c *gin.Context) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxMailAttachmentBytes+65536)
	f, e := c.FormFile("file")
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	if e != nil {
		httpx.Error(c, apperr.BadRequest("mail.invalid_upload", "Select one attachment up to 10 MiB"))
		return
	}
	reader, e := f.Open()
	if e != nil {
		httpx.Error(c, workspaceErr(e))
		return
	}
	defer reader.Close()
	v, e := h.workspace.Upload(c.Request.Context(), u, f.Filename, f.Header.Get("Content-Type"), reader)
	mailResult(c, 201, v, e)
}
func (h *AccountHandler) deleteAttachment(c *gin.Context) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	e := h.workspace.DeleteUpload(c.Request.Context(), u, id)
	mailResult(c, 200, gin.H{"deleted": e == nil}, e)
}
func (h *AccountHandler) getMailSettings(c *gin.Context) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	v, e := h.workspace.Settings(c.Request.Context(), u)
	mailResult(c, 200, v, e)
}
func (h *AccountHandler) saveMailSettings(c *gin.Context) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	var in MailSettings
	if !mailJSON(c, &in) {
		return
	}
	v, e := h.workspace.SaveSettings(c.Request.Context(), u, in)
	mailResult(c, 200, v, e)
}
func (h *AccountHandler) listAppPasswords(c *gin.Context) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	v, e := h.workspace.AppPasswords(c.Request.Context(), u)
	mailResult(c, 200, v, e)
}
func (h *AccountHandler) createAppPassword(c *gin.Context) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !mailJSON(c, &in) {
		return
	}
	v, e := h.workspace.CreateAppPassword(c.Request.Context(), u, in.Name)
	mailResult(c, 201, v, e)
}
func (h *AccountHandler) deleteAppPassword(c *gin.Context) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	e := h.workspace.DeleteAppPassword(c.Request.Context(), u, c.Param("id"))
	mailResult(c, 200, gin.H{"deleted": e == nil}, e)
}
func (h *AccountHandler) listOutbox(c *gin.Context) {
	u, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	if h.outbox == nil {
		httpx.Error(c, apperr.New(503, "mail.send_not_ready", "Mail sending is not enabled", nil))
		return
	}
	var rows []model.MailPendingSend
	e := h.outbox.db.Where("user_id = ? AND (created_at > ? OR status IN ?)", u.ID, time.Now().Add(-30*24*time.Hour), []string{"queued", "dispatching", "sending", "uncertain"}).Order("created_at DESC").Limit(100).Find(&rows).Error
	out := []MailOutboxInfo{}
	for _, r := range rows {
		out = append(out, mailOutboxInfo(r))
	}
	mailResult(c, 200, out, workspaceErr(e))
}
func (h *AccountHandler) provisionSelf(c *gin.Context) {
	u, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	if h.provision == nil {
		httpx.Error(c, apperr.New(503, "mail.provisioning_not_ready", "Mail provisioning is not enabled", nil))
		return
	}
	v, e := h.provision.ProvisionSelf(c.Request.Context(), u)
	mailResult(c, 200, v, e)
}
func (h *AccountHandler) provisionAccount(c *gin.Context) {
	u, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	if h.provision == nil {
		httpx.Error(c, apperr.New(503, "mail.provisioning_not_ready", "Mail provisioning is not enabled", nil))
		return
	}
	var in struct {
		UserID string `json:"user_id"`
	}
	if !mailJSON(c, &in) {
		return
	}
	id, ok := parseMailUUID(c, in.UserID)
	if !ok {
		return
	}
	v, e := h.provision.ProvisionReserved(c.Request.Context(), u, id)
	mailResult(c, 200, v, e)
}

func (h *AccountHandler) reconcileOutbox(c *gin.Context) {
	u, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	if h.outbox == nil {
		httpx.Error(c, apperr.New(503, "mail.send_not_ready", "Mail sending is not enabled", nil))
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	v, e := h.outbox.Reconcile(c.Request.Context(), u, id)
	mailResult(c, 200, v, e)
}

func (h *AccountHandler) createMailFolder(c *gin.Context) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name" binding:"required"`
	}
	if !mailJSON(c, &in) {
		return
	}
	id, e := h.workspace.CreateFolder(c.Request.Context(), u, in.Name)
	if e != nil {
		httpx.Error(c, e)
		return
	}
	httpx.OK(c, http.StatusCreated, map[string]string{"id": id})
}
func (h *AccountHandler) deleteMailFolder(c *gin.Context) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	if e := h.workspace.DeleteFolder(c.Request.Context(), u, c.Param("id")); e != nil {
		httpx.Error(c, e)
		return
	}
	httpx.OK(c, http.StatusOK, map[string]bool{"deleted": true})
}
