//go:build ignore

// Read-only operator verification of one explicitly identified inbound canary.
// It never sends mail, changes read state, or prints credentials.
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
	stdmail "net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

func verify(address, owner, from, subject, marker, expectedSHA, output string) error {
	if os.Geteuid() != 0 || address == "" || from == "" || subject == "" || marker == "" || !regexp.MustCompile("^[a-f0-9]{64}$").MatchString(expectedSHA) {
		return errors.New("explicit root-owned canary scope is required")
	}
	uid, err := uuid.Parse(owner)
	if err != nil {
		return errors.New("invalid owner UUID")
	}
	if _, err := os.Lstat(output); err == nil || !os.IsNotExist(err) {
		return errors.New("report already exists or cannot be safely inspected")
	}
	for _, path := range []string{"/etc/atoman/backend.env", "/etc/atoman-mail/backend.env"} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("private environment file required")
		}
		values, err := godotenv.Read(path)
		if err != nil {
			return errors.New("protected environment parse failed")
		}
		for k, v := range values {
			if err := os.Setenv(k, v); err != nil {
				return err
			}
		}
	}
	db, err := app.OpenDB(config.DBConfig{Type: os.Getenv("DATABASE_TYPE"), URL: os.Getenv("DATABASE_URL")})
	if err != nil {
		return errors.New("application database unavailable")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	var user model.User
	if db.Where("uuid=? AND role=? AND is_active=?", uid, "admin", true).Take(&user).Error != nil {
		return errors.New("active owner administrator required")
	}
	var account model.MailAccount
	if db.Where("user_id=? AND address=? AND status=?", uid, address, "active").Take(&account).Error != nil {
		return errors.New("exact active mailbox ownership required")
	}
	workspace, _, err := mail.NewConfiguredMailWorkspace(db)
	if err != nil {
		return errors.New("mail operational gates unavailable")
	}
	client, err := mail.NewStalwartJMAPClient(os.Getenv("ATOMAN_MAIL_STALWART_URL"), nil)
	if err != nil {
		return err
	}
	vault, err := mail.NewMailCredentialVault(os.Getenv("ATOMAN_MAIL_JMAP_ENCRYPTION_KEY_B64"))
	if err != nil {
		return err
	}
	reader := mail.NewMailReadService(db, client, vault, true)
	actor := authctx.CurrentUser{ID: uid, Role: user.Role}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	folders, err := reader.Folders(ctx, actor)
	if err != nil {
		return err
	}
	inbox := ""
	for _, folder := range folders {
		if folder.Role != nil && *folder.Role == "inbox" {
			inbox = folder.ID
		}
	}
	if inbox == "" {
		return errors.New("inbox missing")
	}
	var received mail.EmailSummary
	for attempt := 0; attempt < 120; attempt++ {
		result, err := workspace.Search(ctx, actor, inbox, marker, "", 0, 50)
		if err != nil {
			return err
		}
		for _, item := range result.Items {
			if item.Subject == subject {
				received = item
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
		return errors.New("identified external canary absent from Inbox")
	}
	detail, err := reader.Detail(ctx, actor, inbox, received.ID)
	if err != nil {
		return err
	}
	if !strings.Contains(detail.PlainText, marker) || len(detail.Attachments) != 1 {
		return errors.New("inbound body or attachment mismatch")
	}
	attachment, _, _, err := workspace.Download(ctx, actor, received.ID, detail.Attachments[0].PartID, false)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(attachment)
	if hex.EncodeToString(sum[:]) != expectedSHA {
		return errors.New("inbound attachment checksum mismatch")
	}
	raw, _, _, err := workspace.Download(ctx, actor, received.ID, "", true)
	if err != nil {
		return err
	}
	message, err := stdmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return errors.New("inbound MIME cannot be parsed")
	}
	senders, err := message.Header.AddressList("From")
	if err != nil || len(senders) != 1 || !strings.EqualFold(senders[0].Address, from) {
		return errors.New("inbound sender differs from the explicitly identified canary")
	}
	if message.Header.Get("Subject") != subject || message.Header.Get("Message-ID") == "" || message.Header.Get("Received") == "" {
		return errors.New("inbound transport trace incomplete")
	}
	rawSHA := sha256.Sum256(raw)
	authResults := strings.Join(message.Header["Authentication-Results"], "\n")
	result := map[string]any{
		"format": "atoman-mail-inbound-acceptance-v1", "status": "ok", "verified_at": time.Now().UTC().Format(time.RFC3339),
		"address": address, "expected_from": from, "subject": subject, "message_id": message.Header.Get("Message-ID"),
		"email_id": received.ID, "inbox_verified": true, "body_marker_verified": true,
		"attachment_sha256": expectedSHA, "attachment_bytes": len(attachment),
		"message_sha256": hex.EncodeToString(rawSHA[:]), "authentication_results": authResults,
		"read_state_modified": false, "credentials_emitted": false,
	}
	payload, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = file.Write(append(payload, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	fmt.Println("MAIL_EXTERNAL_INBOUND_VERIFIED", address, expectedSHA)
	return nil
}
func main() {
	address := flag.String("address", "", "exact active owned mailbox")
	owner := flag.String("user", "", "exact owning administrator UUID")
	from := flag.String("expected-from", "", "verified external canary sender")
	subject := flag.String("subject", "", "exact unique canary subject")
	marker := flag.String("body-marker", "", "unique canary marker")
	sha := flag.String("attachment-sha256", "", "expected UTF-8 attachment SHA256")
	output := flag.String("report", "/var/lib/atoman-mail/acceptance-incoming.json", "private one-run receipt")
	flag.Parse()
	if err := verify(*address, *owner, *from, *subject, *marker, *sha, *output); err != nil {
		fmt.Println("MAIL_EXTERNAL_INBOUND_FAILED", err.Error())
		os.Exit(1)
	}
}
