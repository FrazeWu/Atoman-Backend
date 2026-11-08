package mail

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"
)

// Private, TLS-only, fixed provider catalog. No arbitrary host/port supplied
// by clients; eliminates HTTP/IMAP SSRF into RFC1918, localhost or metadata.
var externalIMAPProviders = map[string]string{
	"gmail":  "imap.gmail.com",
	"qq":     "imap.qq.com",
	"163":    "imap.163.com",
	"icloud": "imap.mail.me.com",
	"yahoo":  "imap.mail.yahoo.com",
}

type ExternalMessageInfo struct {
	UID      uint32    `json:"uid"`
	Subject  string    `json:"subject"`
	From     string    `json:"from"`
	FromName string    `json:"from_name"`
	Date     time.Time `json:"date"`
	Seen     bool      `json:"seen"`
	Size     int64     `json:"size"`
}

type ExternalInboxSnapshot struct {
	UIDValidity uint32                `json:"uid_validity"`
	Messages    []ExternalMessageInfo `json:"messages"`
	HasMore     bool                  `json:"has_more"`
	SyncedAt    time.Time             `json:"synced_at"`
}

type ExternalFolderInfo struct {
	Name      string `json:"name"`
	Delimiter string `json:"delimiter,omitempty"`
	Role      string `json:"role,omitempty"`
}
type ExternalFolderPage struct {
	Folder       string                `json:"folder"`
	UIDValidity  uint32                `json:"uid_validity"`
	Messages     []ExternalMessageInfo `json:"messages"`
	Total        uint32                `json:"total"`
	HasMore      bool                  `json:"has_more"`
	NextPosition uint32                `json:"next_position"`
}
type ExternalIMAPRemote interface {
	ListFolders(ctx context.Context, provider, user, appPassword string) ([]ExternalFolderInfo, error)
	BrowseFolder(ctx context.Context, provider, user, appPassword, folder string, position uint32) (ExternalFolderPage, error)
	Move(ctx context.Context, provider, user, appPassword, source, target string, uidValidity uint32, uid uint32) error
	Sync(ctx context.Context, provider, user, appPassword string) (ExternalInboxSnapshot, error)
	ReadPlain(ctx context.Context, provider, user, appPassword, folder string, uidValidity uint32, uid uint32) (string, error)
	SetSeen(ctx context.Context, provider, user, appPassword, folder string, uidValidity uint32, uid uint32, seen bool) error
}

type liveExternalIMAPRemote struct{}

func externalProviderHost(provider string) (string, bool) {
	host, ok := externalIMAPProviders[provider]
	return host, ok
}

func externalText(raw string, max int) string {
	raw = strings.Map(func(c rune) rune {
		if c < 32 && c != '\n' && c != '\t' {
			return -1
		}
		return c
	}, raw)
	r := []rune(strings.TrimSpace(raw))
	if len(r) > max {
		r = r[:max]
	}
	return string(r)
}

func externalIMAPConnect(ctx context.Context, provider, username, password string) (*imapclient.Client, func(), error) {
	hostname, ok := externalProviderHost(provider)
	if !ok {
		return nil, nil, errors.New("Unknown IMAP provider")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	options := &imapclient.Options{
		Dialer:      &net.Dialer{Timeout: 9 * time.Second},
		TLSConfig:   &tls.Config{MinVersion: tls.VersionTLS12, ServerName: hostname},
		WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader},
		// No protocol debug writer: that could leak the app-password.
	}
	client, err := imapclient.DialTLS(net.JoinHostPort(hostname, "993"), options)
	if err != nil {
		cancel()
		return nil, nil, errors.New("TLS IMAP connection failed")
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Close()
		case <-done:
		}
	}()
	cleanup := func() { close(done); _ = client.Close(); cancel() }
	if err := client.WaitGreeting(); err != nil {
		cleanup()
		return nil, nil, errors.New("Provider IMAP greeting failed")
	}
	if err := client.Login(username, password).Wait(); err != nil {
		cleanup()
		return nil, nil, errors.New("Provider rejected IMAP app password")
	}
	return client, cleanup, nil
}

func externalIMAPAddress(addresses []imap.Address) (display, email string) {
	for _, a := range addresses {
		if a.Mailbox != "" && a.Host != "" {
			return externalText(a.Name, 120), externalText((&a).Addr(), 254)
		}
	}
	return "", ""
}

func (liveExternalIMAPRemote) Sync(ctx context.Context, provider, username, appPassword string) (ExternalInboxSnapshot, error) {
	client, closeClient, err := externalIMAPConnect(ctx, provider, username, appPassword)
	if err != nil {
		return ExternalInboxSnapshot{}, err
	}
	defer closeClient()
	selected, err := client.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return ExternalInboxSnapshot{}, errors.New("Cannot select provider Inbox read-only")
	}
	result := ExternalInboxSnapshot{
		UIDValidity: selected.UIDValidity,
		Messages:    make([]ExternalMessageInfo, 0, 60),
		HasMore:     selected.NumMessages > 60,
		SyncedAt:    time.Now().UTC(),
	}
	if selected.NumMessages == 0 {
		return result, nil
	}
	var ids imap.SeqSet
	start := uint32(1)
	if selected.NumMessages > 60 {
		start = selected.NumMessages - 59
	}
	ids.AddRange(start, selected.NumMessages)
	list, err := client.Fetch(ids, &imap.FetchOptions{
		Envelope: true, Flags: true, UID: true, RFC822Size: true, InternalDate: true,
	}).Collect()
	if err != nil {
		return ExternalInboxSnapshot{}, errors.New("Cannot list provider messages")
	}
	for _, raw := range list {
		if raw == nil || raw.UID == 0 {
			continue
		}
		m := ExternalMessageInfo{UID: uint32(raw.UID), Size: raw.RFC822Size, Date: raw.InternalDate}
		for _, flag := range raw.Flags {
			if flag == imap.FlagSeen {
				m.Seen = true
			}
		}
		if raw.Envelope != nil {
			m.Subject = externalText(raw.Envelope.Subject, 300)
			m.FromName, m.From = externalIMAPAddress(raw.Envelope.From)
			if !raw.Envelope.Date.IsZero() {
				m.Date = raw.Envelope.Date
			}
		}
		result.Messages = append(result.Messages, m)
	}
	// IMAP sequence numbers are oldest-first. Keep newest last 60, reverse
	// so the UI can merge with the native Inbox by descending time.
	for i, j := 0, len(result.Messages)-1; i < j; i, j = i+1, j-1 {
		result.Messages[i], result.Messages[j] = result.Messages[j], result.Messages[i]
	}
	return result, nil
}

// Plaintext-only preview: never return provider-supplied HTML to our UI.
func decodeExternalText(h netmail.Header, body io.Reader) (string, error) {
	switch strings.ToLower(strings.TrimSpace(h.Get("Content-Transfer-Encoding"))) {
	case "base64":
		body = base64.NewDecoder(base64.StdEncoding, body)
	case "quoted-printable":
		body = quotedprintable.NewReader(body)
	}
	data, err := io.ReadAll(io.LimitReader(body, 64<<10))
	if err != nil {
		return "", err
	}
	return externalText(string(data), 32000), nil
}

func externalPlainFromMIME(raw []byte) string {
	message, err := netmail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		return "无法解析这封邮件，请使用原服务商查看。"
	}
	ct := message.Header.Get("Content-Type")
	media, params, err := mime.ParseMediaType(ct)
	if err != nil {
		media = "text/plain"
	}
	if strings.EqualFold(media, "text/plain") {
		text, err := decodeExternalText(message.Header, message.Body)
		if err == nil {
			return text
		}
	}
	if strings.HasPrefix(strings.ToLower(media), "multipart/") {
		reader := multipart.NewReader(message.Body, params["boundary"])
		for n := 0; n < 20; n++ {
			part, err := reader.NextPart()
			if err != nil {
				break
			}
			partType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
			disposition, _, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			if partType == "text/plain" && disposition != "attachment" {
				text, e := decodeExternalText(netmail.Header(part.Header), part)
				_ = part.Close()
				if e == nil {
					return text
				}
			}
			_ = part.Close()
		}
	}
	return "这封邮件包含 HTML、图片或其他 MIME 内容。为安全起见，外部邮箱只读预览暂不加载这些内容，请在原服务商查看。"
}

func (liveExternalIMAPRemote) ReadPlain(ctx context.Context, provider, username, appPassword, folder string, validity, uid uint32) (string, error) {
	if uid == 0 {
		return "", errors.New("Invalid provider message UID")
	}
	client, closeClient, err := externalIMAPConnect(ctx, provider, username, appPassword)
	if err != nil {
		return "", err
	}
	defer closeClient()
	selected, err := client.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil || selected.UIDValidity != validity {
		return "", errors.New("Provider Inbox changed; synchronize again")
	}
	set := imap.UIDSetNum(imap.UID(uid))
	sizeRows, err := client.Fetch(set, &imap.FetchOptions{UID: true, RFC822Size: true}).Collect()
	if err != nil || len(sizeRows) != 1 || uint32(sizeRows[0].UID) != uid {
		return "", errors.New("Provider message is no longer available")
	}
	if sizeRows[0].RFC822Size > 2<<20 {
		return "邮件超过安全预览大小，请前往原邮箱阅读。", nil
	}
	section := &imap.FetchItemBodySection{Peek: true, Partial: &imap.SectionPartial{Offset: 0, Size: 256 << 10}}
	rows, err := client.Fetch(set, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil || len(rows) != 1 || uint32(rows[0].UID) != uid {
		return "", errors.New("Provider message could not be read")
	}
	body := rows[0].FindBodySection(section)
	if len(body) > 512<<10 {
		return "", errors.New("Provider exceeded preview byte limit")
	}
	return externalPlainFromMIME(body), nil
}

// SetSeen changes only the Seen flag of the explicitly selected UID. UIDVALIDITY
// is checked under the same IMAP session before any remote write is attempted.
func (liveExternalIMAPRemote) SetSeen(ctx context.Context, provider, username, appPassword, folder string, validity, uid uint32, seen bool) error {
	if uid == 0 || validity == 0 {
		return errors.New("Invalid provider UID")
	}
	client, closeClient, err := externalIMAPConnect(ctx, provider, username, appPassword)
	if err != nil {
		return err
	}
	defer closeClient()
	selected, err := client.Select(folder, nil).Wait()
	if err != nil || selected.UIDValidity != validity {
		return errors.New("Provider Inbox changed; synchronize again")
	}
	set := imap.UIDSetNum(imap.UID(uid))
	rows, err := client.Fetch(set, &imap.FetchOptions{UID: true, Flags: true}).Collect()
	if err != nil || len(rows) != 1 || uint32(rows[0].UID) != uid {
		return errors.New("Provider message is no longer available")
	}
	op := imap.StoreFlagsDel
	if seen {
		op = imap.StoreFlagsAdd
	}
	if _, err = client.Store(set, &imap.StoreFlags{Op: op, Flags: []imap.Flag{imap.FlagSeen}}, nil).Collect(); err != nil {
		return errors.New("Provider rejected flag update")
	}
	rows, err = client.Fetch(set, &imap.FetchOptions{UID: true, Flags: true}).Collect()
	if err != nil || len(rows) != 1 || uint32(rows[0].UID) != uid {
		return errors.New("Cannot confirm provider flag update")
	}
	actual := false
	for _, flag := range rows[0].Flags {
		if flag == imap.FlagSeen {
			actual = true
		}
	}
	if actual != seen {
		return errors.New("Provider flag update not confirmed")
	}
	return nil
}

// Folder lists are discovered from the provider, not accepted as network targets.
// LIST responses are bounded to protect a small VPS against huge mailbox trees.
func (liveExternalIMAPRemote) ListFolders(ctx context.Context, provider, username, password string) ([]ExternalFolderInfo, error) {
	client, closeClient, err := externalIMAPConnect(ctx, provider, username, password)
	if err != nil {
		return nil, err
	}
	defer closeClient()
	rows, err := client.List("", "*", nil).Collect()
	if err != nil {
		return nil, errors.New("Cannot list provider folders")
	}
	if len(rows) > 200 {
		return nil, errors.New("Too many provider folders")
	}
	result := make([]ExternalFolderInfo, 0, len(rows))
	for _, row := range rows {
		if row == nil || !validExternalFolder(row.Mailbox) {
			continue
		}
		selectable := true
		role := ""
		for _, a := range row.Attrs {
			if a == imap.MailboxAttrNoSelect || a == imap.MailboxAttrNonExistent {
				selectable = false
			}
			switch a {
			case imap.MailboxAttrSent:
				role = "sent"
			case imap.MailboxAttrDrafts:
				role = "drafts"
			case imap.MailboxAttrTrash:
				role = "trash"
			case imap.MailboxAttrJunk:
				role = "junk"
			case imap.MailboxAttrArchive:
				role = "archive"
			}
		}
		if !selectable {
			continue
		}
		if strings.EqualFold(row.Mailbox, "INBOX") {
			role = "inbox"
		}
		delim := ""
		if row.Delim != 0 {
			delim = string(row.Delim)
		}
		result = append(result, ExternalFolderInfo{Name: row.Mailbox, Delimiter: delim, Role: role})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Role == "inbox" {
			return true
		}
		if result[j].Role == "inbox" {
			return false
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result, nil
}

func validExternalFolder(folder string) bool {
	return folder != "" && len([]rune(folder)) <= 256 && !strings.ContainsAny(folder, "\x00\r\n")
}

// Reads only 50 metadata rows per page. Sequence pages may shift if other
// clients expunge mail; UID+UIDVALIDITY are required for every subsequent action.
func (liveExternalIMAPRemote) BrowseFolder(ctx context.Context, provider, username, password, folder string, position uint32) (ExternalFolderPage, error) {
	if !validExternalFolder(folder) || position > 100000 {
		return ExternalFolderPage{}, errors.New("Invalid provider folder or offset")
	}
	client, closeClient, err := externalIMAPConnect(ctx, provider, username, password)
	if err != nil {
		return ExternalFolderPage{}, err
	}
	defer closeClient()
	selected, err := client.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil || selected.UIDValidity == 0 {
		return ExternalFolderPage{}, errors.New("Cannot select provider folder")
	}
	page := ExternalFolderPage{Folder: folder, UIDValidity: selected.UIDValidity, Messages: []ExternalMessageInfo{}, Total: selected.NumMessages}
	if position >= selected.NumMessages {
		page.NextPosition = selected.NumMessages
		return page, nil
	}
	end := selected.NumMessages - position
	start := uint32(1)
	if end > 50 {
		start = end - 49
	}
	ids := imap.SeqSet{}
	ids.AddRange(start, end)
	rows, err := client.Fetch(ids, &imap.FetchOptions{Envelope: true, Flags: true, UID: true, RFC822Size: true, InternalDate: true}).Collect()
	if err != nil {
		return ExternalFolderPage{}, errors.New("Cannot fetch provider folder messages")
	}
	for _, raw := range rows {
		if raw == nil || raw.UID == 0 {
			continue
		}
		m := ExternalMessageInfo{UID: uint32(raw.UID), Size: raw.RFC822Size, Date: raw.InternalDate}
		for _, flag := range raw.Flags {
			if flag == imap.FlagSeen {
				m.Seen = true
			}
		}
		if raw.Envelope != nil {
			m.Subject = externalText(raw.Envelope.Subject, 300)
			m.FromName, m.From = externalIMAPAddress(raw.Envelope.From)
			if !raw.Envelope.Date.IsZero() {
				m.Date = raw.Envelope.Date
			}
		}
		page.Messages = append(page.Messages, m)
	}
	for i, j := 0, len(page.Messages)-1; i < j; i, j = i+1, j-1 {
		page.Messages[i], page.Messages[j] = page.Messages[j], page.Messages[i]
	}
	page.NextPosition = position + (end - start + 1)
	page.HasMore = page.NextPosition < page.Total
	return page, nil
}

// Move requires RFC 6851 MOVE. go-imap's automatic COPY+STORE+EXPUNGE
// fallback can expunge other users' previously deleted messages on servers
// without UIDPLUS, so deliberately refuse that fallback.
func (liveExternalIMAPRemote) Move(ctx context.Context, provider, username, password, source, target string, validity, uid uint32) error {
	if !validExternalFolder(source) || !validExternalFolder(target) || source == target || validity == 0 || uid == 0 {
		return errors.New("Invalid source, target or UID")
	}
	client, closeClient, err := externalIMAPConnect(ctx, provider, username, password)
	if err != nil {
		return err
	}
	defer closeClient()
	if !client.Caps().Has(imap.CapMove) {
		return errors.New("Provider does not support safe MOVE")
	}
	folders, err := client.List("", "*", nil).Collect()
	if err != nil || len(folders) > 200 {
		return errors.New("Cannot inspect provider folders")
	}
	allowed := false
	for _, f := range folders {
		if f == nil || f.Mailbox != target {
			continue
		}
		allowed = true
		for _, a := range f.Attrs {
			if a == imap.MailboxAttrNoSelect || a == imap.MailboxAttrNonExistent {
				allowed = false
			}
		}
	}
	if !allowed {
		return errors.New("Target folder is not selectable")
	}
	selected, err := client.Select(source, &imap.SelectOptions{ReadOnly: false}).Wait()
	if err != nil || selected.UIDValidity != validity {
		return errors.New("Source folder changed")
	}
	set := imap.UIDSetNum(imap.UID(uid))
	existing, err := client.Fetch(set, &imap.FetchOptions{UID: true}).Collect()
	if err != nil || len(existing) != 1 || uint32(existing[0].UID) != uid {
		return errors.New("Source message is missing")
	}
	if _, err = client.Move(set, target).Wait(); err != nil {
		return errors.New("Provider move not confirmed")
	}
	return nil
}
