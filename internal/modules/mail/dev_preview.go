package mail

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type devMessageRecord struct {
	ID            string
	ThreadID      string
	Subject       string
	ReceivedAt    string
	Preview       string
	PlainText     string
	HTML          string
	From          []MailAddress
	To            []MailAddress
	Cc            []MailAddress
	FolderID      string
	IsRead        bool
	IsStarred     bool
	HasAttachment bool
	Attachments   []MailAttachmentInfo
}

type devUserMailbox struct {
	address  string
	folders  []MailboxInfo
	messages map[string]*devMessageRecord
	order    []string
	drafts   []MailDraftInfo
	outbox   []MailOutboxInfo
	contacts []MailContactInfo
	settings MailSettings
	labels   []OrganizerLabelInfo
	records  map[string]*OrganizerRecordInfo
	undo     map[string]map[string]string
}

type devMailWorkspace struct {
	db    *gorm.DB
	mu    sync.Mutex
	users map[uuid.UUID]*devUserMailbox
}

func newDevMailWorkspace(db *gorm.DB) *devMailWorkspace {
	return &devMailWorkspace{
		db:    db,
		users: map[uuid.UUID]*devUserMailbox{},
	}
}

func strPtr(v string) *string { return &v }

func (d *devMailWorkspace) requireActiveAccount(user authctx.CurrentUser) (string, error) {
	if user.ID == uuid.Nil {
		return "", apperr.Unauthorized("Login required")
	}
	var acct model.MailAccount
	if err := d.db.Where("user_id = ? AND status = ?", user.ID, "active").Take(&acct).Error; err != nil {
		return "", apperr.Forbidden("mail.mailbox_inactive", "Mailbox is not active")
	}
	return acct.Address, nil
}

func (d *devMailWorkspace) mailboxFor(user authctx.CurrentUser, address string) *devUserMailbox {
	box, ok := d.users[user.ID]
	if ok {
		box.address = address
		return box
	}
	now := time.Now().UTC()
	msgs := []*devMessageRecord{
		{
			ID:         "dev-msg-welcome",
			ThreadID:   "dev-thread-welcome",
			Subject:    "欢迎使用 Atoman 邮箱",
			ReceivedAt: now.Add(-20 * time.Minute).Format(time.RFC3339),
			Preview:    "你的邮箱已经完成审批并激活，可以在此收发邮件、管理文件夹与设置筛选规则。",
			PlainText:  "你好，\n\n你的 Atoman 邮箱（" + address + "）已审批激活。\n\n当前运行于本地开发预览模式，可用于体验三栏工作区、会话阅读、星标归档、草稿与发送队列界面。\n\n祝使用愉快！\nAtoman 团队",
			HTML:       "<p>你好，</p><p>你的 Atoman 邮箱（<strong>" + address + "</strong>）已审批激活。</p><p>当前运行于本地开发预览模式，可用于体验邮箱工作区、会话阅读与操作流程。</p>",
			From:       []MailAddress{{Name: "Atoman", Email: "support@atoman.org"}},
			To:         []MailAddress{{Name: "Atoman 用户", Email: address}},
			FolderID:   "inbox",
			IsRead:     false,
		},
		{
			ID:         "dev-msg-thread-1",
			ThreadID:   "dev-thread-sync",
			Subject:    "下周版本UI走查与发布确认",
			ReceivedAt: now.Add(-4 * time.Hour).Format(time.RFC3339),
			Preview:    "桌面端与移动端细节已初步收敛，请确认邮件客户端在窄屏下的阅读体验。",
			PlainText:  "Hi，\n\n桌面端和移动端的字体与分割线规范已经初步整理完成，方便时请确认一下站内邮件的阅读面板布局。",
			From:       []MailAddress{{Name: "林小满", Email: "lin@atoman.org"}},
			To:         []MailAddress{{Name: "Atoman 用户", Email: address}},
			FolderID:   "inbox",
			IsRead:     true,
		},
		{
			ID:            "dev-msg-thread-2",
			ThreadID:      "dev-thread-sync",
			Subject:       "下周版本UI走查与发布确认",
			ReceivedAt:    now.Add(-90 * time.Minute).Format(time.RFC3339),
			Preview:       "收到，已补充审批管理入口与本地开发联调支持，附件里是检查清单。",
			PlainText:     "收到，已经补充了后台邮件审批入口和本地开发预览状态，可以直接在 /mail 中打开查看效果。",
			From:          []MailAddress{{Name: "陈序", Email: "chen@atoman.org"}},
			To:            []MailAddress{{Name: "Atoman 用户", Email: address}},
			Cc:            []MailAddress{{Name: "林小满", Email: "lin@atoman.org"}},
			FolderID:      "inbox",
			IsRead:        false,
			IsStarred:     true,
			HasAttachment: true,
			Attachments:   []MailAttachmentInfo{{PartID: "dev-part-1", Name: "ui-checklist.txt", Type: "text/plain", Size: 2048}},
		},
	}
	msgMap := make(map[string]*devMessageRecord, len(msgs))
	order := make([]string, 0, len(msgs))
	for _, m := range msgs {
		msgMap[m.ID] = m
		order = append(order, m.ID)
	}
	box = &devUserMailbox{
		address: address,
		folders: []MailboxInfo{
			{ID: "inbox", Name: "Inbox", Role: strPtr("inbox")},
			{ID: "sent", Name: "Sent", Role: strPtr("sent")},
			{ID: "drafts", Name: "Drafts", Role: strPtr("drafts")},
			{ID: "archive", Name: "Archive", Role: strPtr("archive")},
			{ID: "junk", Name: "Junk", Role: strPtr("junk")},
			{ID: "trash", Name: "Trash", Role: strPtr("trash")},
		},
		messages: msgMap,
		order:    order,
		drafts:   []MailDraftInfo{},
		outbox:   []MailOutboxInfo{},
		contacts: []MailContactInfo{},
		settings: defaultMailSettings(),
		labels:   []OrganizerLabelInfo{},
		records:  map[string]*OrganizerRecordInfo{},
		undo:     map[string]map[string]string{},
	}
	d.users[user.ID] = box
	return box
}

func (d *devMailWorkspace) folderSummaries(box *devUserMailbox) []MailboxInfo {
	out := make([]MailboxInfo, len(box.folders))
	for i, f := range box.folders {
		var total, unread uint64
		for _, id := range box.order {
			msg := box.messages[id]
			if msg != nil && msg.FolderID == f.ID {
				total++
				if !msg.IsRead {
					unread++
				}
			}
		}
		f.TotalEmails = total
		f.UnreadEmails = unread
		out[i] = f
	}
	return out
}

func (m *devMessageRecord) toSummary() EmailSummary {
	kw := map[string]bool{}
	if m.IsRead {
		kw["$seen"] = true
	}
	if m.IsStarred {
		kw["$flagged"] = true
	}
	return EmailSummary{
		MailboxIDs:    map[string]bool{m.FolderID: true},
		ID:            m.ID,
		ThreadID:      m.ThreadID,
		Subject:       m.Subject,
		ReceivedAt:    m.ReceivedAt,
		Preview:       m.Preview,
		HasAttachment: m.HasAttachment,
		From:          m.From,
		To:            m.To,
		Keywords:      kw,
		IsRead:        m.IsRead,
		IsStarred:     m.IsStarred,
	}
}

func (h *AccountHandler) devActiveUser(c *gin.Context) (authctx.CurrentUser, *devUserMailbox, bool) {
	u, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return u, nil, false
	}
	addr, err := h.devStore.requireActiveAccount(u)
	if err != nil {
		httpx.Error(c, err)
		return u, nil, false
	}
	h.devStore.mu.Lock()
	box := h.devStore.mailboxFor(u, addr)
	return u, box, true
}

func (h *AccountHandler) devFolders(c *gin.Context) {
	_, box, ok := h.devActiveUser(c)
	if !ok {
		return
	}
	folders := h.devStore.folderSummaries(box)
	h.devStore.mu.Unlock()
	httpx.OK(c, http.StatusOK, folders)
}

func (h *AccountHandler) devUnread(c *gin.Context) {
	_, box, ok := h.devActiveUser(c)
	if !ok {
		return
	}
	defer h.devStore.mu.Unlock()
	var count uint64
	latestID := ""
	for _, id := range box.order {
		m := box.messages[id]
		if m != nil && m.FolderID == "inbox" && !m.IsRead {
			count++
			if latestID == "" || m.ReceivedAt > box.messages[latestID].ReceivedAt {
				latestID = m.ID
			}
		}
	}
	httpx.OK(c, http.StatusOK, gin.H{"count": count, "latest_id": latestID})
}

func (h *AccountHandler) devSearch(c *gin.Context) {
	_, box, ok := h.devActiveUser(c)
	if !ok {
		return
	}
	defer h.devStore.mu.Unlock()
	folder := strings.TrimSpace(c.Query("mailbox_id"))
	thread := strings.TrimSpace(c.Query("thread_id"))
	query := strings.ToLower(strings.TrimSpace(c.Query("q")))
	items := []EmailSummary{}
	for _, id := range box.order {
		m := box.messages[id]
		if m == nil {
			continue
		}
		if thread != "" && m.ThreadID != thread {
			continue
		}
		if thread == "" && folder != "" && m.FolderID != folder {
			continue
		}
		if query != "" {
			haystack := strings.ToLower(m.Subject + " " + m.PlainText + " " + m.Preview)
			if len(m.From) > 0 {
				haystack += " " + strings.ToLower(m.From[0].Name+" "+m.From[0].Email)
			}
			if !strings.Contains(haystack, query) {
				continue
			}
		}
		items = append(items, m.toSummary())
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].ReceivedAt > items[j].ReceivedAt
	})
	httpx.OK(c, http.StatusOK, MailSearchResult{Items: items, Total: uint64(len(items)), Position: 0, HasMore: false})
}

func (h *AccountHandler) devMessageDetail(c *gin.Context) {
	_, box, ok := h.devActiveUser(c)
	if !ok {
		return
	}
	defer h.devStore.mu.Unlock()
	msg := box.messages[messageQueryID(c)]
	if msg == nil {
		httpx.Error(c, apperr.NotFound("mail.message_not_found", "Mail message not found"))
		return
	}
	httpx.OK(c, http.StatusOK, EmailDetail{
		ID:           msg.ID,
		ThreadID:     msg.ThreadID,
		Subject:      msg.Subject,
		ReceivedAt:   msg.ReceivedAt,
		From:         msg.From,
		To:           msg.To,
		Cc:           msg.Cc,
		PlainText:    msg.PlainText,
		HTML:         msg.HTML,
		HasPlainText: true,
		Attachments:  msg.Attachments,
		IsRead:       msg.IsRead,
		MessageID:    []string{msg.ID + "@atoman.org"},
		ReplyTo:      msg.From,
	})
}

func (h *AccountHandler) devMarkSeen(c *gin.Context, msgID string) {
	_, box, ok := h.devActiveUser(c)
	if !ok {
		return
	}
	defer h.devStore.mu.Unlock()
	if m := box.messages[msgID]; m != nil {
		m.IsRead = true
	}
	httpx.OK(c, http.StatusOK, gin.H{"read": true})
}

func (h *AccountHandler) devActions(c *gin.Context, in MailActionInput) {
	_, box, ok := h.devActiveUser(c)
	if !ok {
		return
	}
	defer h.devStore.mu.Unlock()
	prev := map[string]string{}
	updated := []string{}
	for _, id := range in.IDs {
		m := box.messages[id]
		if m == nil {
			continue
		}
		prev[id] = m.FolderID
		switch in.Action {
		case "read":
			m.IsRead = true
		case "unread":
			m.IsRead = false
		case "star":
			m.IsStarred = true
		case "unstar":
			m.IsStarred = false
		case "archive":
			m.FolderID = "archive"
		case "trash":
			m.FolderID = "trash"
		case "junk":
			m.FolderID = "junk"
		case "restore":
			m.FolderID = "inbox"
		case "move":
			if in.MailboxID != "" {
				m.FolderID = in.MailboxID
			}
		}
		updated = append(updated, id)
	}
	token := uuid.NewString()
	box.undo[token] = prev
	httpx.OK(c, http.StatusOK, MailActionResult{Updated: updated, UndoToken: token})
}

func (h *AccountHandler) devUndoAction(c *gin.Context, token string) {
	_, box, ok := h.devActiveUser(c)
	if !ok {
		return
	}
	defer h.devStore.mu.Unlock()
	prev := box.undo[token]
	updated := []string{}
	for id, folder := range prev {
		if m := box.messages[id]; m != nil {
			m.FolderID = folder
			updated = append(updated, id)
		}
	}
	delete(box.undo, token)
	httpx.OK(c, http.StatusOK, MailActionResult{Updated: updated})
}

func (h *AccountHandler) devOrganizerState(c *gin.Context) {
	_, box, ok := h.devActiveUser(c)
	if !ok {
		return
	}
	defer h.devStore.mu.Unlock()
	records := make([]OrganizerRecordInfo, 0, len(box.records))
	for _, r := range box.records {
		records = append(records, *r)
	}
	httpx.OK(c, http.StatusOK, OrganizerState{Labels: box.labels, Records: records})
}

func (h *AccountHandler) devOutboxList(c *gin.Context) {
	_, box, ok := h.devActiveUser(c)
	if !ok {
		return
	}
	defer h.devStore.mu.Unlock()
	httpx.OK(c, http.StatusOK, box.outbox)
}

func (h *AccountHandler) devQueueMail(c *gin.Context, in ComposeMailInput) {
	_, box, ok := h.devActiveUser(c)
	if !ok {
		return
	}
	defer h.devStore.mu.Unlock()
	id := uuid.New()
	now := time.Now().UTC()
	msgID := "dev-sent-" + id.String()[:8]
	toAddrs := make([]MailAddress, 0, len(in.To))
	for _, addr := range in.To {
		toAddrs = append(toAddrs, MailAddress{Name: addr, Email: addr})
	}
	preview := strings.TrimSpace(in.Text)
	if len([]rune(preview)) > 80 {
		preview = string([]rune(preview)[:80]) + "…"
	}
	box.messages[msgID] = &devMessageRecord{
		ID:         msgID,
		ThreadID:   "dev-thread-" + id.String()[:8],
		Subject:    in.Subject,
		ReceivedAt: now.Format(time.RFC3339),
		Preview:    preview,
		PlainText:  in.Text,
		From:       []MailAddress{{Name: "我", Email: box.address}},
		To:         toAddrs,
		FolderID:   "sent",
		IsRead:     true,
	}
	box.order = append([]string{msgID}, box.order...)
	item := MailOutboxInfo{
		ID:        id,
		Status:    "sent",
		SendAfter: now,
		CreatedAt: now,
	}
	httpx.OK(c, http.StatusAccepted, item)
}

func (h *AccountHandler) devSenders(c *gin.Context) {
	_, box, ok := h.devActiveUser(c)
	if !ok {
		return
	}
	addr := box.address
	h.devStore.mu.Unlock()
	httpx.OK(c, http.StatusOK, []MailSenderInfo{{Address: addr, Provider: "atoman"}})
}

func (h *AccountHandler) devDraftsList(c *gin.Context) {
	_, box, ok := h.devActiveUser(c)
	if !ok {
		return
	}
	defer h.devStore.mu.Unlock()
	httpx.OK(c, http.StatusOK, box.drafts)
}

func (h *AccountHandler) devSaveDraft(c *gin.Context, id uuid.UUID, in SaveMailDraftInput) {
	_, box, ok := h.devActiveUser(c)
	if !ok {
		return
	}
	defer h.devStore.mu.Unlock()
	now := time.Now().UTC()
	var saved MailDraftInfo
	found := false
	for i := range box.drafts {
		if box.drafts[i].ID == id {
			box.drafts[i].Revision++
			box.drafts[i].Subject = in.Compose.Subject
			box.drafts[i].UpdatedAt = now
			copyCompose := in.Compose
			box.drafts[i].Compose = &copyCompose
			saved = box.drafts[i]
			found = true
			break
		}
	}
	if !found {
		copyCompose := in.Compose
		saved = MailDraftInfo{
			ID:        id,
			Revision:  1,
			Subject:   in.Compose.Subject,
			UpdatedAt: now,
			Compose:   &copyCompose,
		}
		box.drafts = append([]MailDraftInfo{saved}, box.drafts...)
	}
	httpx.OK(c, http.StatusOK, saved)
}

func (h *AccountHandler) devDeleteDraft(c *gin.Context, id uuid.UUID) {
	_, box, ok := h.devActiveUser(c)
	if !ok {
		return
	}
	defer h.devStore.mu.Unlock()
	next := make([]MailDraftInfo, 0, len(box.drafts))
	for _, d := range box.drafts {
		if d.ID != id {
			next = append(next, d)
		}
	}
	box.drafts = next
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}

func (h *AccountHandler) devSettingsGet(c *gin.Context) {
	_, box, ok := h.devActiveUser(c)
	if !ok {
		return
	}
	defer h.devStore.mu.Unlock()
	httpx.OK(c, http.StatusOK, box.settings)
}

func (h *AccountHandler) devSettingsSave(c *gin.Context, in MailSettings) {
	_, box, ok := h.devActiveUser(c)
	if !ok {
		return
	}
	defer h.devStore.mu.Unlock()
	box.settings = in
	httpx.OK(c, http.StatusOK, box.settings)
}

func (h *AccountHandler) devContactsList(c *gin.Context) {
	_, box, ok := h.devActiveUser(c)
	if !ok {
		return
	}
	defer h.devStore.mu.Unlock()
	httpx.OK(c, http.StatusOK, box.contacts)
}

func (h *AccountHandler) devCreateFolder(c *gin.Context, name string) {
	_, box, ok := h.devActiveUser(c)
	if !ok {
		return
	}
	defer h.devStore.mu.Unlock()
	trimmed := strings.TrimSpace(name)
	id := "dev-folder-" + uuid.NewString()[:8]
	box.folders = append(box.folders, MailboxInfo{ID: id, Name: trimmed})
	httpx.OK(c, http.StatusCreated, gin.H{"id": id})
}

func (h *AccountHandler) devDeleteFolder(c *gin.Context, id string) {
	_, box, ok := h.devActiveUser(c)
	if !ok {
		return
	}
	defer h.devStore.mu.Unlock()
	next := make([]MailboxInfo, 0, len(box.folders))
	for _, f := range box.folders {
		if f.ID == id && f.Role == nil {
			continue
		}
		next = append(next, f)
	}
	box.folders = next
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}
