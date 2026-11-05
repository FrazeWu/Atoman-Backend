//go:build ignore

// Run explicitly from the reviewed backend checkout. This operator-only canary
// sends exactly one synthetic message to the already provisioned owner's mailbox.
// It never emits credentials and refuses to repeat an incomplete recorded run.
package main

import (
	"atoman/internal/app"
	"atoman/internal/config"
	"atoman/internal/model"
	"atoman/internal/modules/mail"
	"atoman/internal/platform/authctx"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gorm.io/gorm"
)

type report struct {
	RunID         string   `json:"run_id"`
	Address       string   `json:"address"`
	Started       string   `json:"started_at"`
	Finished      string   `json:"finished_at,omitempty"`
	Complete      bool     `json:"complete"`
	Steps         []string `json:"steps"`
	QueueIDs      []string `json:"queue_ids,omitempty"`
	AttachmentSHA string   `json:"attachment_sha256,omitempty"`
}

var state report
var destination string
var errAuthenticationRejected = errors.New("IMAPS authentication rejected")

func checkpoint(step string) error {
	if step != "" {
		state.Steps = append(state.Steps, step)
	}
	raw, e := json.MarshalIndent(state, "", "  ")
	if e != nil {
		return e
	}
	temp := destination + ".new"
	if e = os.WriteFile(temp, raw, 0600); e != nil {
		return e
	}
	if e = os.Rename(temp, destination); e != nil {
		return e
	}
	if step != "" {
		fmt.Println("MAIL_ACCEPTANCE", step)
	}
	return nil
}
func require(ok bool, message string) error {
	if !ok {
		return errors.New(message)
	}
	return nil
}
func imapLogin(address, password string) error {
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, e := tls.DialWithDialer(&dialer, "tcp", "127.0.0.1:19993", &tls.Config{ServerName: "mail.atoman.org", MinVersion: tls.VersionTLS12})
	if e != nil {
		return errors.New("IMAPS transport unavailable")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	reader := bufio.NewReader(conn)
	line, e := reader.ReadString('\n')
	if e != nil || !strings.HasPrefix(line, "* OK") {
		return errors.New("IMAPS greeting unavailable")
	}
	if _, e = fmt.Fprintf(conn, "A1 LOGIN %q %q\r\n", address, password); e != nil {
		return errors.New("IMAPS login request failed")
	}
	for i := 0; i < 40; i++ {
		line, e = reader.ReadString('\n')
		if e != nil {
			return errors.New("IMAPS login response failed")
		}
		if strings.HasPrefix(line, "A1 ") {
			if !strings.HasPrefix(line, "A1 OK") {
				return errAuthenticationRejected
			}
			_, _ = fmt.Fprint(conn, "A2 LOGOUT\r\n")
			return nil
		}
	}
	return errors.New("IMAPS response limit reached")
}
func main() {
	appEnv := flag.String("app-env", "/etc/atoman/backend.env", "protected app environment")
	mailEnv := flag.String("mail-env", "/etc/atoman-mail/backend.env", "protected mail environment")
	address := flag.String("address", "", "exact pre-existing self mailbox")
	userID := flag.String("user", "", "exact site administrator UUID owning the mailbox")
	reportPath := flag.String("report", "/var/lib/atoman-mail/acceptance-webmail.json", "private one-run receipt")
	allow := flag.Bool("confirm-self-send", false, "explicitly authorize one synthetic self delivery")
	flag.Parse()
	if os.Geteuid() != 0 || !*allow || *address == "" || *userID == "" {
		fmt.Println("MAIL_ACCEPTANCE_REQUIRES_EXPLICIT_ROOT_SELF_SCOPE")
		os.Exit(1)
	}
	destination = *reportPath
	if e := run(*appEnv, *mailEnv, *address, *userID); e != nil {
		fmt.Println("MAIL_ACCEPTANCE_FAILED", e.Error())
		os.Exit(1)
	}
}
func run(appEnv, mailEnv, address, userID string) error {
	uid, e := uuid.Parse(userID)
	if e != nil {
		return errors.New("invalid owner UUID")
	}
	if info, e := os.Lstat(destination); e == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe report path")
		}
		raw, e := os.ReadFile(destination)
		if e != nil {
			return e
		}
		var old report
		if json.Unmarshal(raw, &old) == nil && old.Complete && old.Address == address {
			fmt.Println("MAIL_ACCEPTANCE_ALREADY_COMPLETE", old.RunID)
			return nil
		}
		return errors.New("a previous run exists; inspect its receipt before any new self delivery")
	} else if !os.IsNotExist(e) {
		return e
	}
	for _, path := range []string{appEnv, mailEnv} {
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("environment must be a private regular file")
		}
		values, e := godotenv.Read(path)
		if e != nil {
			return errors.New("environment parse failed")
		}
		for k, v := range values {
			if e = os.Setenv(k, v); e != nil {
				return e
			}
		}
	}
	db, e := app.OpenDB(config.DBConfig{Type: os.Getenv("DATABASE_TYPE"), URL: os.Getenv("DATABASE_URL")})
	if e != nil {
		return errors.New("application database unavailable")
	}
	conn, e := db.DB()
	if e != nil {
		return e
	}
	defer conn.Close()
	var user model.User
	if db.Where("uuid=? AND role=? AND is_active=?", uid, "admin", true).Take(&user).Error != nil {
		return errors.New("active administrator owner required")
	}
	var account model.MailAccount
	if db.Where("user_id=? AND address=? AND status=?", uid, address, "active").Take(&account).Error != nil {
		return errors.New("exact active mailbox ownership required")
	}
	workspace, outbox, e := mail.NewConfiguredMailWorkspace(db)
	if e != nil {
		return errors.New("mail operational gates unavailable")
	}
	client, e := mail.NewStalwartJMAPClient(os.Getenv("ATOMAN_MAIL_STALWART_URL"), nil)
	if e != nil {
		return e
	}
	vault, e := mail.NewMailCredentialVault(os.Getenv("ATOMAN_MAIL_JMAP_ENCRYPTION_KEY_B64"))
	if e != nil {
		return e
	}
	reader := mail.NewMailReadService(db, client, vault, true)
	actor := authctx.CurrentUser{ID: uid, Role: user.Role}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if e = os.MkdirAll(filepath.Dir(destination), 0700); e != nil {
		return e
	}
	state = report{RunID: uuid.NewString(), Address: address, Started: time.Now().UTC().Format(time.RFC3339), Steps: []string{}}
	if e = checkpoint("scoped_owner_verified"); e != nil {
		return e
	}
	folders, e := reader.Folders(ctx, actor)
	if e != nil {
		return e
	}
	inbox := ""
	for _, f := range folders {
		if f.Role != nil && *f.Role == "inbox" {
			inbox = f.ID
		}
	}
	if inbox == "" {
		return errors.New("inbox missing")
	}
	attachment := []byte("Atoman synthetic production attachment\n" + state.RunID + "\n中文附件完整性校验。\n")
	sum := sha256.Sum256(attachment)
	state.AttachmentSHA = hex.EncodeToString(sum[:])
	upload, e := workspace.Upload(ctx, actor, "atoman-verification.txt", "text/plain; charset=utf-8", bytes.NewReader(attachment))
	if e != nil {
		return e
	}
	draftID := uuid.New()
	compose := mail.DraftCompose{To: []string{address}, Subject: "Atoman self verification " + state.RunID, Text: "Atoman self delivery and attachment verification. " + state.RunID, HTML: "<p>Atoman synthetic self delivery.</p>", Attachments: []model.MailUpload{upload}}
	draft, e := workspace.SaveDraft(ctx, actor, draftID, mail.SaveMailDraftInput{Revision: 0, Compose: compose})
	if e != nil {
		return e
	}
	compose.Text += "\nDraft revision persisted."
	draft, e = workspace.SaveDraft(ctx, actor, draftID, mail.SaveMailDraftInput{Revision: draft.Revision, Compose: compose})
	if e != nil {
		return e
	}
	fetched, e := workspace.Draft(ctx, actor, draftID)
	if e != nil {
		return e
	}
	if e = require(fetched.Revision == 2 && fetched.Compose != nil && fetched.Compose.Text == compose.Text && len(fetched.Compose.Attachments) == 1, "draft persistence mismatch"); e != nil {
		return e
	}
	_, e = workspace.SaveDraft(ctx, actor, draftID, mail.SaveMailDraftInput{Revision: 1, Compose: compose})
	if e == nil {
		return errors.New("stale draft revision was accepted")
	}
	if e = checkpoint("draft_revision_and_attachment_persisted"); e != nil {
		return e
	}
	input := mail.ComposeMailInput{RequestID: uuid.New(), DraftID: &draftID, DraftRevision: draft.Revision, To: compose.To, Subject: compose.Subject, Text: compose.Text, HTML: compose.HTML, AttachmentIDs: []uuid.UUID{upload.ID}}
	queued, e := outbox.Queue(ctx, actor, input)
	if e != nil {
		return e
	}
	state.QueueIDs = append(state.QueueIDs, queued.ID.String())
	if e = checkpoint("undo_test_queued"); e != nil {
		return e
	}
	undone, e := outbox.Cancel(ctx, actor, queued.ID)
	if e != nil {
		return e
	}
	if e = require(undone.Status == "cancelled", "server cancellation did not commit"); e != nil {
		return e
	}
	if _, e = workspace.Draft(ctx, actor, draftID); e != nil {
		return errors.New("cancelled send lost its draft")
	}
	duplicate, e := outbox.Queue(ctx, actor, input)
	if e != nil || duplicate.ID != queued.ID || duplicate.Status != "cancelled" {
		return errors.New("idempotency replay mismatch")
	}
	if e = checkpoint("five_second_server_cancel_and_retry_verified"); e != nil {
		return e
	}
	input.RequestID = uuid.New()
	queued, e = outbox.Queue(ctx, actor, input)
	if e != nil {
		return e
	}
	state.QueueIDs = append(state.QueueIDs, queued.ID.String())
	if e = checkpoint("single_self_delivery_queued"); e != nil {
		return e
	}
	accepted := false
	for i := 0; i < 90; i++ {
		status, e := outbox.Status(ctx, actor, queued.ID)
		if e != nil {
			return e
		}
		if status.Status == "sent" {
			accepted = true
			break
		}
		if status.Status == "uncertain" || status.Status == "blocked" {
			return errors.New("submission requires reconciliation; never auto-resend")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if !accepted {
		return errors.New("supervised outbox did not confirm acceptance")
	}
	var received mail.EmailSummary
	for i := 0; i < 45; i++ {
		result, e := workspace.Search(ctx, actor, inbox, state.RunID, "", 0, 50)
		if e != nil {
			return e
		}
		for _, msg := range result.Items {
			if msg.Subject == compose.Subject {
				received = msg
				break
			}
		}
		if received.ID != "" {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if received.ID == "" {
		return errors.New("self delivery not present in Inbox")
	}
	if e = checkpoint("supervised_worker_accepted_and_inbox_received"); e != nil {
		return e
	}
	details, e := reader.Detail(ctx, actor, inbox, received.ID)
	if e != nil {
		return e
	}
	if !strings.Contains(details.PlainText, state.RunID) || len(details.Attachments) != 1 {
		return errors.New("delivered body or attachment missing")
	}
	bytesRead, _, _, e := workspace.Download(ctx, actor, received.ID, details.Attachments[0].PartID, false)
	if e != nil {
		return e
	}
	if sha256.Sum256(bytesRead) != sum {
		return errors.New("attachment integrity mismatch")
	}
	_, _, _, e = workspace.Download(ctx, actor, received.ID, "", true)
	if e != nil {
		return e
	}
	if e = checkpoint("body_raw_and_attachment_integrity_verified"); e != nil {
		return e
	}
	action, e := workspace.Actions(ctx, actor, mail.MailActionInput{IDs: []string{received.ID}, Action: "unread"})
	if e != nil || len(action.Updated) != 1 {
		return errors.New("read state mutation failed")
	}
	if _, e = reader.Detail(ctx, actor, inbox, received.ID); e != nil {
		return e
	}
	result, e := workspace.Search(ctx, actor, inbox, state.RunID, "", 0, 50)
	if e != nil {
		return e
	}
	for _, m := range result.Items {
		if m.ID == received.ID && m.IsRead {
			return errors.New("detail read marked mail seen implicitly")
		}
	}
	if e = reader.MarkSeen(ctx, actor, inbox, received.ID); e != nil {
		return e
	}
	action, e = workspace.Actions(ctx, actor, mail.MailActionInput{IDs: []string{received.ID}, Action: "archive"})
	if e != nil || action.UndoToken == "" {
		return errors.New("archive reversal receipt unavailable")
	}
	token, e := uuid.Parse(action.UndoToken)
	if e != nil {
		return e
	}
	reverted, e := workspace.UndoAction(ctx, actor, token)
	if e != nil || len(reverted.Updated) != 1 {
		return errors.New("archive undo failed")
	}
	if e = checkpoint("explicit_seen_and_bulk_undo_verified"); e != nil {
		return e
	}
	folder, e := workspace.CreateFolder(ctx, actor, "Verification "+state.RunID[:8])
	if e != nil {
		return e
	}
	if e = workspace.DeleteFolder(ctx, actor, folder); e != nil {
		return e
	}
	if _, e = workspace.Settings(ctx, actor); e != nil {
		return e
	}
	device, e := workspace.CreateAppPassword(ctx, actor, "Deployment verification "+state.RunID[:8])
	if e != nil {
		return e
	}
	defer func() { _ = workspace.DeleteAppPassword(context.Background(), actor, device.ID) }()
	if e = imapLogin(address, device.Password); e != nil {
		return e
	}
	if e = workspace.DeleteAppPassword(ctx, actor, device.ID); e != nil {
		return e
	}
	revoked := false
	for i := 0; i < 5; i++ {
		if errors.Is(imapLogin(address, device.Password), errAuthenticationRejected) {
			revoked = true
			break
		}
		time.Sleep(time.Second)
	}
	if !revoked {
		return errors.New("revoked app password still accepted")
	}
	if e = checkpoint("app_password_imaps_and_revocation_verified"); e != nil {
		return e
	}
	var old model.MailDraft
	if e = db.Where("id=? AND user_id=?", draftID, uid).Take(&old).Error; !errors.Is(e, gorm.ErrRecordNotFound) {
		return errors.New("accepted send retained its unchanged draft")
	}
	if e = workspace.DeleteUpload(ctx, actor, upload.ID); e != nil {
		return e
	}
	state.Complete = true
	state.Finished = time.Now().UTC().Format(time.RFC3339)
	return checkpoint("complete")
}
