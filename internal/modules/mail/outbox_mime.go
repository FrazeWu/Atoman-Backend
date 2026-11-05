package mail

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	stdmail "net/mail"
	"net/textproto"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ComposeMailInput accepts only server-owned attachment identifiers. The server
// resolves and authenticates all bytes before creating the immutable snapshot.
type ComposeMailInput struct {
	DraftID       *uuid.UUID  `json:"draft_id,omitempty"`
	DraftRevision uint64      `json:"draft_revision,omitempty"`
	RequestID     uuid.UUID   `json:"request_id" binding:"required"`
	To            []string    `json:"to"`
	Cc            []string    `json:"cc"`
	Bcc           []string    `json:"bcc"`
	Subject       string      `json:"subject"`
	Text          string      `json:"text"`
	HTML          string      `json:"html,omitempty"`
	AttachmentIDs []uuid.UUID `json:"attachment_ids,omitempty"`
	InReplyTo     string      `json:"in_reply_to,omitempty"`
	References    []string    `json:"references,omitempty"`
	attachments   []composeAttachment
}

type outboxPayload struct {
	Sender     string   `json:"sender"`
	Recipients []string `json:"recipients"`
	MIME       []byte   `json:"mime"`
}

func (c ComposeMailInput) fingerprint() (string, error) {
	data, e := json.Marshal(c)
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
	if c.Text == "" && c.HTML != "" {
		c.Text = mailHTMLText(sanitizeMailHTML(c.HTML))
	}
	if c.RequestID == uuid.Nil || id == uuid.Nil || len(c.To)+len(c.Cc)+len(c.Bcc) == 0 || len(c.To)+len(c.Cc)+len(c.Bcc) > 50 || len(c.Subject) > 900 || (len(c.Text) == 0 && len(c.attachments) == 0) || len(c.Text) > 256*1024 || len(c.HTML) > 256*1024 || !utf8.ValidString(c.Subject) || !utf8.ValidString(c.Text) || strings.ContainsAny(c.Subject, "\r\n\x00") {
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
	fmt.Fprintf(&b, "From: %s\r\n", sender[0])
	writeMailListHeader(&b, "To", to, ",")
	if len(cc) > 0 {
		writeMailListHeader(&b, "Cc", cc, ",")
	}
	if !validReplyHeader(c.InReplyTo) || len(c.References) > 30 {
		return outboxPayload{}, errors.New("Invalid reply headers")
	}
	for _, ref := range c.References {
		if ref == "" || !validReplyHeader(ref) {
			return outboxPayload{}, errors.New("Invalid references")
		}
	}
	if c.InReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: <%s>\r\n", strings.Trim(c.InReplyTo, "<>"))
	}
	if len(c.References) > 0 {
		refs := make([]string, 0, len(c.References))
		for _, r := range c.References {
			refs = append(refs, "<"+strings.Trim(r, "<>")+">")
		}
		writeMailListHeader(&b, "References", refs, "")
	}
	fmt.Fprintf(&b, "Subject: %s\r\nDate: %s\r\nMessage-ID: <%s@atoman.org>\r\nMIME-Version: 1.0\r\n", mime.QEncoding.Encode("utf-8", c.Subject), when.UTC().Format(time.RFC1123Z), id.String())
	mixed := multipart.NewWriter(&b)
	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", mixed.Boundary())
	writePart := func(writer *multipart.Writer, ct string, data []byte, filename string) error {
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", ct)
		h.Set("Content-Transfer-Encoding", "base64")
		if filename != "" {
			h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
		}
		w, e := writer.CreatePart(h)
		if e != nil {
			return e
		}
		encoded := base64.StdEncoding.EncodeToString(data)
		for len(encoded) > 76 {
			if _, e = w.Write([]byte(encoded[:76] + "\r\n")); e != nil {
				return e
			}
			encoded = encoded[76:]
		}
		_, e = w.Write([]byte(encoded + "\r\n"))
		return e
	}
	if c.HTML != "" {
		var alternative bytes.Buffer
		alt := multipart.NewWriter(&alternative)
		if e := writePart(alt, "text/plain; charset=UTF-8", []byte(c.Text), ""); e != nil {
			return outboxPayload{}, e
		}
		if e := writePart(alt, "text/html; charset=UTF-8", []byte(sanitizeMailHTML(c.HTML)), ""); e != nil {
			return outboxPayload{}, e
		}
		if e := alt.Close(); e != nil {
			return outboxPayload{}, e
		}
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": alt.Boundary()}))
		w, e := mixed.CreatePart(h)
		if e != nil {
			return outboxPayload{}, e
		}
		if _, e = w.Write(alternative.Bytes()); e != nil {
			return outboxPayload{}, e
		}
	} else if e := writePart(mixed, "text/plain; charset=UTF-8", []byte(c.Text), ""); e != nil {
		return outboxPayload{}, e
	}
	for _, a := range c.attachments {
		if e := writePart(mixed, a.Type, a.Data, a.Name); e != nil {
			return outboxPayload{}, e
		}
	}
	if e := mixed.Close(); e != nil {
		return outboxPayload{}, e
	}
	if b.Len() > maxMailMIMEBytes {
		return outboxPayload{}, errors.New("Message too large")
	}
	return outboxPayload{Sender: sender[0], Recipients: recipients, MIME: b.Bytes()}, nil
}

type composeAttachment struct {
	Name, Type string
	Data       []byte
}

func validReplyHeader(s string) bool {
	if len(s) > 900 || strings.ContainsAny(s, "\r\n\x00 \t") {
		return false
	}
	if s == "" {
		return true
	}
	s = strings.Trim(s, "<>")
	return strings.Contains(s, "@") && !strings.ContainsAny(s, "<>")
}

func writeMailListHeader(b *bytes.Buffer, name string, values []string, separator string) {
	b.WriteString(name + ": ")
	column := len(name) + 2
	for i, v := range values {
		if i > 0 {
			b.WriteString(separator)
			column += len(separator)
			if column+1+len(v) > 78 {
				b.WriteString("\r\n ")
				column = 1
			} else {
				b.WriteByte(' ')
				column++
			}
		}
		b.WriteString(v)
		column += len(v)
	}
	b.WriteString("\r\n")
}
