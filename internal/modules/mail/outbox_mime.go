package mail

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	stdmail "net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ComposeMailInput is deliberately plain text only in this stage. Attachments
// require a separate bounded upload+scan path, not arbitrary browser blobs.
type ComposeMailInput struct {
	RequestID uuid.UUID `json:"request_id" binding:"required"`
	To        []string  `json:"to" binding:"required"`
	Cc        []string  `json:"cc"`
	Bcc       []string  `json:"bcc"`
	Subject   string    `json:"subject"`
	Text      string    `json:"text" binding:"required"`
}

type outboxPayload struct {
	Sender     string   `json:"sender"`
	Recipients []string `json:"recipients"`
	MIME       []byte   `json:"mime"`
}

func (c ComposeMailInput) fingerprint() (string, error) {
	data, e := json.Marshal(struct {
		To, Cc, Bcc   []string
		Subject, Text string
	}{c.To, c.Cc, c.Bcc, c.Subject, c.Text})
	if e != nil {
		return "", errors.New("Invalid composition")
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func validEnvelopeAddresses(addrs []string) ([]string, error) {
	out := make([]string, 0, len(addrs))
	for _, raw := range addrs {
		if len(raw) > 254 || raw == "" || raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, "\r\n\x00") || !utf8.ValidString(raw) {
			return nil, errors.New("Invalid email address")
		}
		parsed, e := stdmail.ParseAddress(raw)
		if e != nil || parsed.Address != raw {
			return nil, errors.New("Invalid email address")
		}
		for _, r := range raw {
			if r > 127 {
				return nil, errors.New("Internationalized address not yet enabled")
			}
		}
		out = append(out, raw)
	}
	return out, nil
}

func buildOutboxPayload(from string, id uuid.UUID, c ComposeMailInput, when time.Time) (outboxPayload, error) {
	if c.RequestID == uuid.Nil || id == uuid.Nil || len(c.To) == 0 || len(c.To)+len(c.Cc)+len(c.Bcc) > 50 || len(c.Subject) > 998 || len(c.Text) == 0 || len(c.Text) > 256*1024 || !utf8.ValidString(c.Subject) || !utf8.ValidString(c.Text) || strings.ContainsAny(c.Subject, "\r\n\x00") {
		return outboxPayload{}, errors.New("Invalid mail compose fields")
	}
	sender, e := validEnvelopeAddresses([]string{from})
	if e != nil {
		return outboxPayload{}, e
	}
	to, e := validEnvelopeAddresses(c.To)
	if e != nil {
		return outboxPayload{}, e
	}
	cc, e := validEnvelopeAddresses(c.Cc)
	if e != nil {
		return outboxPayload{}, e
	}
	bcc, e := validEnvelopeAddresses(c.Bcc)
	if e != nil {
		return outboxPayload{}, e
	}
	recipients := append(append(append([]string{}, to...), cc...), bcc...)
	// Preserve Bcc for the private SMTP envelope only; never write a Bcc header.
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\n", sender[0], strings.Join(to, ", "))
	if len(cc) > 0 {
		fmt.Fprintf(&b, "Cc: %s\r\n", strings.Join(cc, ", "))
	}
	fmt.Fprintf(&b, "Subject: %s\r\nDate: %s\r\nMessage-ID: <%s@atoman.org>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n", mime.QEncoding.Encode("utf-8", c.Subject), when.UTC().Format(time.RFC1123Z), id.String())
	content := strings.ReplaceAll(strings.ReplaceAll(c.Text, "\r\n", "\n"), "\r", "\n")
	b.WriteString(strings.ReplaceAll(content, "\n", "\r\n"))
	b.WriteString("\r\n")
	return outboxPayload{Sender: sender[0], Recipients: recipients, MIME: b.Bytes()}, nil
}
