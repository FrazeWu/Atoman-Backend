package mail

import (
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
	"strings"
)

func (h *AccountHandler) externalUser(c *gin.Context) (authctx.CurrentUser, bool) {
	u, ok := h.workspaceUser(c)
	if !ok {
		return u, false
	}
	if h.external == nil {
		httpx.Error(c, apperr.New(503, "mail.external_disabled", "External IMAP accounts are not enabled", nil))
		return u, false
	}
	return u, true
}
func (h *AccountHandler) listExternalAccounts(c *gin.Context) {
	u, ok := h.externalUser(c)
	if !ok {
		return
	}
	rows, err := h.external.Accounts(c.Request.Context(), u)
	mailResult(c, http.StatusOK, rows, err)
}
func (h *AccountHandler) connectExternalAccount(c *gin.Context) {
	u, ok := h.externalUser(c)
	if !ok {
		return
	}
	var in ExternalConnectInput
	if !mailJSON(c, &in) {
		return
	}
	item, err := h.external.Connect(c.Request.Context(), u, in)
	mailResult(c, http.StatusCreated, item, err)
}
func (h *AccountHandler) syncExternalAccount(c *gin.Context) {
	u, ok := h.externalUser(c)
	if !ok {
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	item, err := h.external.Sync(c.Request.Context(), u, id)
	mailResult(c, http.StatusOK, item, err)
}
func (h *AccountHandler) deleteExternalAccount(c *gin.Context) {
	u, ok := h.externalUser(c)
	if !ok {
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	err := h.external.Delete(c.Request.Context(), u, id)
	mailResult(c, http.StatusOK, gin.H{"deleted": err == nil}, err)
}
func (h *AccountHandler) unifiedExternalInbox(c *gin.Context) {
	u, ok := h.externalUser(c)
	if !ok {
		return
	}
	result, err := h.external.Unified(c.Request.Context(), u)
	mailResult(c, http.StatusOK, result, err)
}
func (h *AccountHandler) readExternalMessage(c *gin.Context) {
	u, ok := h.externalUser(c)
	if !ok {
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	uid, e := strconv.ParseUint(c.Param("uid"), 10, 32)
	if e != nil || uid == 0 {
		httpx.Error(c, apperr.BadRequest("mail.external_invalid_uid", "Invalid provider message UID"))
		return
	}
	validity, e := strconv.ParseUint(c.Query("uid_validity"), 10, 32)
	if e != nil || validity == 0 {
		httpx.Error(c, apperr.BadRequest("mail.external_invalid_uid_validity", "Invalid provider folder UID validity"))
		return
	}
	var text string
	var err error
	folder := strings.TrimSpace(c.Query("folder"))
	if folder != "" {
		text, err = h.external.ReadFolder(c.Request.Context(), u, id, folder, uint32(uid), uint32(validity))
	} else {
		text, err = h.external.Read(c.Request.Context(), u, id, uint32(uid), uint32(validity))
	}
	mailResult(c, http.StatusOK, gin.H{"text": text}, err)
}

// A fixed catalog lets the UI explain provider-specific app passwords without
// ever accepting client-supplied destination URLs, hostnames or server ports.
func (h *AccountHandler) externalProviders(c *gin.Context) {
	_, ok := h.externalUser(c)
	if !ok {
		return
	}
	httpx.OK(c, 200, []gin.H{
		{"id": "gmail", "label": "Gmail", "host": "imap.gmail.com", "port": 993},
		{"id": "qq", "label": "QQ 邮箱", "host": "imap.qq.com", "port": 993},
		{"id": "163", "label": "网易 163", "host": "imap.163.com", "port": 993},
		{"id": "icloud", "label": "iCloud 邮箱", "host": "imap.mail.me.com", "port": 993},
		{"id": "yahoo", "label": "Yahoo Mail", "host": "imap.mail.yahoo.com", "port": 993},
	})
}

func (h *AccountHandler) updateExternalSeen(c *gin.Context) {
	u, ok := h.externalUser(c)
	if !ok {
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	uid, e := strconv.ParseUint(c.Param("uid"), 10, 32)
	if e != nil || uid == 0 {
		httpx.Error(c, apperr.BadRequest("mail.external_invalid_uid", "Invalid provider UID"))
		return
	}
	var payload struct {
		UIDValidity uint32 `json:"uid_validity" binding:"required"`
		Folder      string `json:"folder,omitempty"`
		Seen        *bool  `json:"seen" binding:"required"`
	}
	if !mailJSON(c, &payload) {
		return
	}
	if payload.UIDValidity == 0 || payload.Seen == nil {
		httpx.Error(c, apperr.BadRequest("mail.external_invalid_flag", "Invalid message flag request"))
		return
	}
	if payload.Folder != "" {
		err := h.external.SetFolderSeen(c.Request.Context(), u, id, payload.Folder, uint32(uid), payload.UIDValidity, *payload.Seen)
		mailResult(c, http.StatusOK, gin.H{"updated": err == nil}, err)
		return
	}
	result, err := h.external.SetSeen(c.Request.Context(), u, id, uint32(uid), payload.UIDValidity, *payload.Seen)
	mailResult(c, http.StatusOK, result, err)
}

func (h *AccountHandler) listExternalFolders(c *gin.Context) {
	u, ok := h.externalUser(c)
	if !ok {
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	rows, err := h.external.Folders(c.Request.Context(), u, id)
	mailResult(c, http.StatusOK, rows, err)
}
func (h *AccountHandler) browseExternalFolder(c *gin.Context) {
	u, ok := h.externalUser(c)
	if !ok {
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	pos, e := strconv.ParseUint(c.DefaultQuery("position", "0"), 10, 32)
	if e != nil || pos > 100000 {
		httpx.Error(c, apperr.BadRequest("mail.external_invalid_offset", "Invalid folder position"))
		return
	}
	page, err := h.external.BrowseFolder(c.Request.Context(), u, id, c.Query("folder"), uint32(pos))
	mailResult(c, http.StatusOK, page, err)
}

func (h *AccountHandler) moveExternalMessage(c *gin.Context) {
	u, ok := h.externalUser(c)
	if !ok {
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	uid, e := strconv.ParseUint(c.Param("uid"), 10, 32)
	if e != nil || uid == 0 {
		httpx.Error(c, apperr.BadRequest("mail.external_invalid_uid", "Invalid UID"))
		return
	}
	var payload struct {
		UIDValidity uint32 `json:"uid_validity" binding:"required"`
		Source      string `json:"source" binding:"required"`
		Destination string `json:"destination" binding:"required"`
	}
	if !mailJSON(c, &payload) {
		return
	}
	receipt, err := h.external.MoveFolderMessage(c.Request.Context(), u, id, payload.Source, payload.Destination, uint32(uid), payload.UIDValidity)
	mailResult(c, http.StatusOK, receipt, err)
}
