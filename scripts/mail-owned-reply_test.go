//go:build ignore

package main

import (
	"net/mail"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/net/html"
)

func TestOwnedReplyBodyContentAndReferences(t *testing.T) {
	original := "Received <attachment> & confirmed.\r\nNext line.\r\n\r\nVerification: normal-reply"
	text, rendered, err := normalizedBody([]byte(original))
	if err != nil {
		t.Fatal(err)
	}
	tokens := html.NewTokenizer(strings.NewReader(rendered))
	var visible strings.Builder
	for {
		kind := tokens.Next()
		if kind == html.ErrorToken {
			break
		}
		if kind == html.TextToken {
			visible.Write(tokens.Text())
		} else {
			visible.WriteByte(' ')
		}
	}
	if strings.Join(strings.Fields(visible.String()), " ") != strings.Join(strings.Fields(text), " ") {
		t.Fatal("HTML and plain text content differ")
	}
	if strings.Contains(rendered, "<attachment>") {
		t.Fatal("body text was interpreted as markup")
	}
	refs, err := replyReferences(mail.Header{"References": []string{"<first@example.test> <prior@example.test>"}}, "<parent@example.test>")
	if err != nil || !reflect.DeepEqual(refs, []string{"first@example.test", "prior@example.test", "parent@example.test"}) {
		t.Fatalf("reply chain changed: %v %v", refs, err)
	}
	if _, err := replyReferences(mail.Header{}, "parent@example.test\r\nBcc: bad@example.test"); err == nil {
		t.Fatal("header injection accepted")
	}
}

func TestOwnedReplyReceiptRejectsSecondReservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reply.json")
	receipt := replyReceipt{Format: replyReportFormat, Status: "queue_attempt_started"}
	if err := saveReceipt(path, &receipt, true); err != nil {
		t.Fatal(err)
	}
	if err := saveReceipt(path, &receipt, true); err == nil {
		t.Fatal("second Queue reservation was allowed")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("reply receipt not private")
	}
}

func TestOwnedReplyExistingReceiptNeverNeedsEnvironmentOrQueue(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("operator requires root; pure body and receipt tests remain portable")
	}
	dir := t.TempDir()
	body := filepath.Join(dir, "body.txt")
	if err := os.WriteFile(body, []byte("Normal owned reply."), 0600); err != nil {
		t.Fatal(err)
	}
	options := replyOptions{Owner: uuid.NewString(), IncomingID: "specific-message", Subject: "Incoming subject", Marker: "source-marker", BodyFile: body, RequestID: uuid.NewString(), ConfirmRecipient: ownedRecipient, AppEnv: filepath.Join(dir, "absent-env"), IncomingReport: filepath.Join(dir, "absent-incoming"), Report: filepath.Join(dir, "reply.json"), Apply: true}
	receipt := replyReceipt{Format: replyReportFormat, Owner: options.Owner, Address: ownedMailbox, Recipient: ownedRecipient, IncomingID: options.IncomingID, SourceSubject: options.Subject, SourceMarker: options.Marker, RequestID: options.RequestID, BodySHA: digest([]byte("Normal owned reply.")), OutboxID: uuid.NewString(), Complete: true, Status: "sent", ReplyHeadersVerified: true}
	if err := saveReceipt(options.Report, &receipt, true); err != nil {
		t.Fatal(err)
	}
	if err := runReply(options); err != nil {
		t.Fatalf("completed receipt tried to read missing environment or queue: %v", err)
	}
	receipt.Complete = false
	receipt.Status = "dispatching"
	if err := saveReceipt(options.Report, &receipt, false); err != nil {
		t.Fatal(err)
	}
	if err := runReply(options); err == nil || !strings.Contains(err.Error(), "incomplete previous reply") {
		t.Fatalf("uncertain prior run did not fail closed: %v", err)
	}
	options.ConfirmRecipient = "different@example.test"
	if err := runReply(options); err == nil || !strings.Contains(err.Error(), "recipient confirmation") {
		t.Fatalf("unconfirmed recipient accepted: %v", err)
	}
}
