//go:build ignore

// Operator-only reply to one verified incoming message from the owned Gmail
// mailbox. Dry-run is the default. A durable one-run receipt precedes Queue;
// an incomplete receipt never causes an automatic second Queue attempt.
package main

import (
	"atoman/internal/app"
	"atoman/internal/config"
	"atoman/internal/model"
	"atoman/internal/modules/mail"
	"atoman/internal/platform/authctx"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"mime"
	stdmail "net/mail"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

const ownedMailbox = "fazong@atoman.org"
const ownedRecipient = "fzwu.scsc@gmail.com"
const replyReportFormat = "atoman-mail-owned-reply-v1"

type replyOptions struct {
	AppEnv, MailEnv, Owner, IncomingID, Subject, Marker           string
	BodyFile, RequestID, ConfirmRecipient, IncomingReport, Report string
	Apply                                                         bool
}

type incomingProof struct {
	Format        string `json:"format"`
	Status        string `json:"status"`
	Address       string `json:"address"`
	From          string `json:"expected_from"`
	Subject       string `json:"subject"`
	EmailID       string `json:"email_id"`
	MessageID     string `json:"message_id"`
	MessageSHA    string `json:"message_sha256"`
	InboxVerified bool   `json:"inbox_verified"`
	BodyVerified  bool   `json:"body_marker_verified"`
	ReadModified  bool   `json:"read_state_modified"`
}

type replyReceipt struct {
	Format               string   `json:"format"`
	Owner                string   `json:"owner_user_id"`
	Address              string   `json:"address"`
	Recipient            string   `json:"recipient"`
	IncomingID           string   `json:"incoming_email_id"`
	IncomingMessageID    string   `json:"incoming_message_id"`
	IncomingSHA          string   `json:"incoming_message_sha256"`
	SourceSubject        string   `json:"source_subject"`
	SourceMarker         string   `json:"source_marker"`
	Subject              string   `json:"subject"`
	References           []string `json:"references"`
	BodySHA              string   `json:"body_sha256"`
	RequestID            string   `json:"request_id"`
	Started              string   `json:"started_at"`
	Updated              string   `json:"updated_at"`
	Complete             bool     `json:"complete"`
	Status               string   `json:"status"`
	OutboxID             string   `json:"outbox_id,omitempty"`
	MessageID            string   `json:"message_id,omitempty"`
	ImportedEmailID      string   `json:"imported_email_id,omitempty"`
	SubmissionID         string   `json:"submission_id,omitempty"`
	SendAfter            string   `json:"send_after,omitempty"`
	ReplyHeadersVerified bool     `json:"reply_headers_verified"`
}

func privateRead(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > limit {
		return nil, errors.New("private bounded regular input file required")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("private input unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("private input exceeds limit")
	}
	return raw, nil
}

func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func normalizedBody(raw []byte) (string, string, error) {
	text := strings.TrimSpace(strings.ReplaceAll(string(raw), "\r\n", "\n"))
	if text == "" || len(text) > 16*1024 || !utf8.ValidString(text) {
		return "", "", errors.New("reply body must be bounded nonempty UTF-8 text")
	}
	for _, r := range text {
		if (r < 32 && r != '\n' && r != '\t') || r == 127 {
			return "", "", errors.New("reply body contains a control character")
		}
	}
	paragraphs := strings.Split(text, "\n\n")
	for i, paragraph := range paragraphs {
		paragraphs[i] = "<p>" + strings.ReplaceAll(html.EscapeString(paragraph), "\n", "<br>") + "</p>"
	}
	return text, strings.Join(paragraphs, "\n"), nil
}

func messageToken(value string) (string, error) {
	if strings.HasPrefix(value, "<") && strings.HasSuffix(value, ">") {
		value = value[1 : len(value)-1]
	}
	if value == "" || len(value) > 898 || !strings.Contains(value, "@") || strings.ContainsAny(value, "<> \t\r\n\x00") {
		return "", errors.New("invalid message reference")
	}
	return value, nil
}

func replyReferences(header stdmail.Header, parent string) ([]string, error) {
	raw := header.Get("References")
	if raw == "" {
		raw = header.Get("In-Reply-To")
	}
	var result []string
	seen := map[string]bool{}
	for _, value := range append(strings.Fields(raw), parent) {
		token, err := messageToken(value)
		if err != nil {
			return nil, err
		}
		if !seen[token] {
			result = append(result, token)
			seen[token] = true
		}
	}
	if len(result) > 30 {
		return nil, errors.New("reply reference chain exceeds service limit")
	}
	return result, nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func saveReceipt(path string, receipt *replyReceipt, initial bool) error {
	receipt.Updated = time.Now().UTC().Format(time.RFC3339)
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return errors.New("unsafe reply receipt directory")
	}
	var f *os.File
	if initial {
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	} else {
		f, err = os.CreateTemp(dir, ".owned-reply-")
	}
	if err != nil {
		return errors.New("reply receipt already exists or cannot be written")
	}
	temp := f.Name()
	if !initial {
		defer os.Remove(temp)
	}
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if !initial {
		if err := os.Rename(temp, path); err != nil {
			return err
		}
	}
	return syncDirectory(path)
}

func matchesInput(receipt replyReceipt, options replyOptions, bodySHA string) bool {
	return receipt.Format == replyReportFormat && receipt.Owner == options.Owner && receipt.Address == ownedMailbox &&
		receipt.Recipient == ownedRecipient && receipt.IncomingID == options.IncomingID && receipt.SourceSubject == options.Subject &&
		receipt.SourceMarker == options.Marker && receipt.RequestID == options.RequestID && receipt.BodySHA == bodySHA
}

func runReply(options replyOptions) error {
	owner, err := uuid.Parse(options.Owner)
	if err != nil || owner == uuid.Nil {
		return errors.New("explicit owner UUID required")
	}
	request, err := uuid.Parse(options.RequestID)
	if err != nil || request == uuid.Nil {
		return errors.New("explicit persistent request UUID required")
	}
	if os.Geteuid() != 0 || options.ConfirmRecipient != ownedRecipient || options.IncomingID == "" || options.Subject == "" || options.Marker == "" || options.BodyFile == "" || options.Report == "" {
		return errors.New("explicit root-owned incoming scope and exact recipient confirmation required")
	}
	if strings.ContainsAny(options.IncomingID+options.Subject+options.Marker, "\r\n\x00") || len(options.Subject) > 890 || len(options.Marker) > 512 || len(options.IncomingID) > 512 {
		return errors.New("invalid incoming scope")
	}
	options.Owner, options.RequestID = owner.String(), request.String()
	rawBody, err := privateRead(options.BodyFile, 16*1024)
	if err != nil {
		return err
	}
	text, rendered, err := normalizedBody(rawBody)
	if err != nil {
		return err
	}
	bodySHA := digest([]byte(text))
	if _, err := os.Lstat(options.Report); err == nil {
		raw, err := privateRead(options.Report, 128*1024)
		if err != nil {
			return err
		}
		var previous replyReceipt
		if json.Unmarshal(raw, &previous) != nil || !matchesInput(previous, options, bodySHA) {
			return errors.New("existing reply receipt belongs to different inputs; no send attempted")
		}
		if !previous.Complete || previous.Status != "sent" || !previous.ReplyHeadersVerified || previous.OutboxID == "" {
			return errors.New("incomplete previous reply receipt; inspect its outbox/request ID, never automatically resend")
		}
		fmt.Println("MAIL_OWNED_REPLY_ALREADY_COMPLETE", previous.OutboxID)
		return nil
	} else if !os.IsNotExist(err) {
		return errors.New("cannot safely inspect reply receipt")
	}
	proofBytes, err := privateRead(options.IncomingReport, 128*1024)
	if err != nil {
		return err
	}
	var proof incomingProof
	if json.Unmarshal(proofBytes, &proof) != nil || proof.Format != "atoman-mail-inbound-acceptance-v1" || proof.Status != "ok" || proof.Address != ownedMailbox || !strings.EqualFold(proof.From, ownedRecipient) || proof.EmailID != options.IncomingID || proof.Subject != options.Subject || !proof.InboxVerified || !proof.BodyVerified || proof.ReadModified {
		return errors.New("incoming acceptance receipt does not match exact requested reply scope")
	}
	parent, err := messageToken(proof.MessageID)
	if err != nil {
		return err
	}
	for _, path := range []string{options.AppEnv, options.MailEnv} {
		raw, err := privateRead(path, 1<<20)
		if err != nil {
			return err
		}
		values, err := godotenv.Unmarshal(string(raw))
		if err != nil {
			return errors.New("protected environment parse failed")
		}
		for key, value := range values {
			if err := os.Setenv(key, value); err != nil {
				return errors.New("protected environment load failed")
			}
		}
	}
	if strings.TrimRight(os.Getenv("ATOMAN_MAIL_STALWART_URL"), "/") != "http://127.0.0.1:18580" {
		return errors.New("fixed loopback mail service required")
	}
	db, err := app.OpenDB(config.DBConfig{Type: os.Getenv("DATABASE_TYPE"), URL: os.Getenv("DATABASE_URL")})
	if err != nil {
		return errors.New("application database unavailable")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return errors.New("application database unavailable")
	}
	defer sqlDB.Close()
	var user model.User
	if db.Where("uuid=? AND role=? AND is_active=?", owner, "admin", true).Take(&user).Error != nil {
		return errors.New("active owner administrator required")
	}
	var account model.MailAccount
	if db.Where("user_id=? AND address=? AND status=?", owner, ownedMailbox, "active").Take(&account).Error != nil || account.StalwartAccountID == nil || *account.StalwartAccountID == "" {
		return errors.New("exact active mailbox ownership required")
	}
	workspace, outbox, err := mail.NewConfiguredMailWorkspace(db)
	if err != nil || workspace == nil || outbox == nil {
		return errors.New("mail operational gates unavailable")
	}
	client, err := mail.NewStalwartJMAPClient("http://127.0.0.1:18580", nil)
	if err != nil {
		return errors.New("mail reader unavailable")
	}
	vault, err := mail.NewMailCredentialVault(os.Getenv("ATOMAN_MAIL_JMAP_ENCRYPTION_KEY_B64"))
	if err != nil {
		return errors.New("mail credential vault unavailable")
	}
	reader := mail.NewMailReadService(db, client, vault, true)
	actor := authctx.CurrentUser{ID: owner, Role: user.Role}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	folders, err := reader.Folders(ctx, actor)
	if err != nil {
		return errors.New("owner mailbox listing failed")
	}
	inbox := ""
	for _, folder := range folders {
		if folder.Role != nil && *folder.Role == "inbox" {
			inbox = folder.ID
		}
	}
	if inbox == "" {
		return errors.New("owner Inbox unavailable")
	}
	detail, err := reader.Detail(ctx, actor, inbox, options.IncomingID)
	if err != nil || detail.ID != options.IncomingID || detail.Subject != options.Subject || detail.BodyTruncated || !strings.Contains(detail.PlainText, options.Marker) {
		return errors.New("exact incoming body, subject, or mailbox verification failed")
	}
	source, _, _, err := workspace.Download(ctx, actor, options.IncomingID, "", true)
	if err != nil || digest(source) != proof.MessageSHA {
		return errors.New("incoming message differs from verified receipt")
	}
	original, err := stdmail.ReadMessage(bytes.NewReader(source))
	if err != nil {
		return errors.New("incoming MIME cannot be parsed")
	}
	senders, err := original.Header.AddressList("From")
	if err != nil || len(senders) != 1 || !strings.EqualFold(senders[0].Address, ownedRecipient) {
		return errors.New("incoming sender is not the confirmed owned recipient")
	}
	if original.Header.Get("Message-ID") != proof.MessageID || original.Header.Get("Subject") != options.Subject {
		return errors.New("incoming MIME identity mismatch")
	}
	if original.Header.Get("Reply-To") != "" {
		targets, err := original.Header.AddressList("Reply-To")
		if err != nil || len(targets) != 1 || !strings.EqualFold(targets[0].Address, ownedRecipient) {
			return errors.New("incoming Reply-To is outside the owned recipient scope")
		}
	}
	references, err := replyReferences(original.Header, parent)
	if err != nil {
		return err
	}
	subject := options.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}
	compose := mail.ComposeMailInput{RequestID: request, To: []string{ownedRecipient}, Subject: subject, Text: text, HTML: rendered, InReplyTo: parent, References: references}
	receipt := replyReceipt{Format: replyReportFormat, Owner: owner.String(), Address: ownedMailbox, Recipient: ownedRecipient, IncomingID: options.IncomingID, IncomingMessageID: proof.MessageID, IncomingSHA: proof.MessageSHA, SourceSubject: options.Subject, SourceMarker: options.Marker, Subject: subject, References: references, BodySHA: bodySHA, RequestID: request.String(), Started: time.Now().UTC().Format(time.RFC3339), Status: "queue_attempt_started"}
	if !options.Apply {
		fmt.Println("MAIL_OWNED_REPLY_DRY_RUN_OK", options.IncomingID, bodySHA)
		return nil
	}
	if err := saveReceipt(options.Report, &receipt, true); err != nil {
		return err
	}
	queued, err := outbox.Queue(ctx, actor, compose)
	if err != nil {
		receipt.Status = "queue_result_unknown"
		_ = saveReceipt(options.Report, &receipt, false)
		return errors.New("queue result requires inspection by request ID; never automatically resend")
	}
	receipt.OutboxID, receipt.Status = queued.ID.String(), queued.Status
	receipt.MessageID = "<" + queued.ID.String() + "@atoman.org>"
	receipt.SendAfter = queued.SendAfter.UTC().Format(time.RFC3339Nano)
	if err := saveReceipt(options.Report, &receipt, false); err != nil {
		return errors.New("queued but receipt update failed; inspect persistent request ID, never resend")
	}
	for attempt := 0; attempt < 90; attempt++ {
		status, err := outbox.Status(ctx, actor, queued.ID)
		if err != nil {
			return errors.New("outbox receipt query failed; never resend")
		}
		if status.Status != receipt.Status {
			receipt.Status = status.Status
			if err := saveReceipt(options.Report, &receipt, false); err != nil {
				return err
			}
		}
		if status.Status == "sent" {
			var sent model.MailPendingSend
			if db.WithContext(ctx).Where("id=? AND user_id=? AND request_id=?", queued.ID, owner, request).Take(&sent).Error != nil {
				return errors.New("accepted outbox metadata unavailable; never resend")
			}
			receipt.ImportedEmailID, receipt.SubmissionID = sent.ImportedEmailID, sent.SubmissionID
			if sent.ImportedEmailID == "" {
				return errors.New("accepted message identifier unavailable; never resend")
			}
			outgoing, _, _, err := workspace.Download(ctx, actor, sent.ImportedEmailID, "", true)
			if err != nil {
				return errors.New("accepted reply MIME unavailable; never resend")
			}
			accepted, err := stdmail.ReadMessage(bytes.NewReader(outgoing))
			if err != nil {
				return errors.New("accepted reply MIME invalid; never resend")
			}
			to, toErr := accepted.Header.AddressList("To")
			actualReferences := strings.Fields(accepted.Header.Get("References"))
			for i, ref := range actualReferences {
				actualReferences[i], err = messageToken(ref)
				if err != nil {
					return errors.New("accepted reply reference invalid; never resend")
				}
			}
			actualParent, parentErr := messageToken(accepted.Header.Get("In-Reply-To"))
			actualSubject, subjectErr := new(mime.WordDecoder).DecodeHeader(accepted.Header.Get("Subject"))
			if toErr != nil || subjectErr != nil || actualSubject != subject || len(to) != 1 || !strings.EqualFold(to[0].Address, ownedRecipient) || accepted.Header.Get("Cc") != "" || accepted.Header.Get("Bcc") != "" || parentErr != nil || actualParent != parent || !reflect.DeepEqual(actualReferences, references) || accepted.Header.Get("Message-ID") != receipt.MessageID {
				return errors.New("accepted reply header mismatch; never resend")
			}
			receipt.Complete, receipt.ReplyHeadersVerified = true, true
			if err := saveReceipt(options.Report, &receipt, false); err != nil {
				return errors.New("accepted reply receipt update failed; never resend")
			}
			fmt.Println("MAIL_OWNED_REPLY_ACCEPTED", receipt.OutboxID, receipt.MessageID)
			return nil
		}
		if status.Status == "uncertain" || status.Status == "blocked" || status.Status == "cancelled" {
			return errors.New("reply stopped or outcome uncertain; inspect existing receipt, never resend")
		}
		select {
		case <-ctx.Done():
			return errors.New("reply receipt wait expired; never resend")
		case <-time.After(time.Second):
		}
	}
	return errors.New("reply still pending; inspect existing receipt, never resend")
}

func main() {
	var options replyOptions
	flag.StringVar(&options.AppEnv, "app-env", "/etc/atoman/backend.env", "protected application environment")
	flag.StringVar(&options.MailEnv, "mail-env", "/etc/atoman-mail/backend.env", "protected mail environment")
	flag.StringVar(&options.Owner, "owner-user-id", "", "exact active administrator UUID owning fazong@atoman.org")
	flag.StringVar(&options.IncomingID, "incoming-message-id", "", "exact Stalwart email ID, not a Gmail API ID")
	flag.StringVar(&options.Subject, "expected-subject", "", "exact verified incoming subject")
	flag.StringVar(&options.Marker, "expected-marker", "", "exact verified incoming body marker")
	flag.StringVar(&options.BodyFile, "body-file", "", "private UTF-8 normal reply text; HTML is generated from the same content")
	flag.StringVar(&options.RequestID, "request-id", "", "persistent explicit UUID for this single reply")
	flag.StringVar(&options.ConfirmRecipient, "confirm-recipient", "", "must explicitly equal fzwu.scsc@gmail.com")
	flag.StringVar(&options.IncomingReport, "incoming-report", "/var/lib/atoman-mail/acceptance-incoming.json", "private successful incoming verification receipt")
	flag.StringVar(&options.Report, "report", "/var/lib/atoman-mail/acceptance-owned-reply.json", "private one-run reply receipt")
	flag.BoolVar(&options.Apply, "apply", false, "queue this single owned reply; otherwise read-only validation")
	flag.Parse()
	if err := runReply(options); err != nil {
		fmt.Println("MAIL_OWNED_REPLY_FAILED", err.Error())
		os.Exit(1)
	}
}
