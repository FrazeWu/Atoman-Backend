package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	stdmail "net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
)

// Hardcoded, provider-scoped transport prevents user-controlled SMTP SSRF.
// Port 587 is STARTTLS; port 465 is implicit TLS. Never downgrade to plain.
type externalSMTPHost struct {
	Host     string
	Port     int
	StartTLS bool
}

var externalSMTPHosts = map[string]externalSMTPHost{
	"gmail":  {"smtp.gmail.com", 465, false},
	"qq":     {"smtp.qq.com", 465, false},
	"163":    {"smtp.163.com", 465, false},
	"icloud": {"smtp.mail.me.com", 587, true},
	"yahoo":  {"smtp.mail.yahoo.com", 465, false},
}

type ExternalSMTPRemote interface {
	Send(ctx context.Context, provider, username, appPassword string, p outboxPayload) error
}
type liveExternalSMTPRemote struct{}

// The credential record was created only after successful owner IMAP auth.
// Re-validate the encrypted address (not a user supplied display name) before
// it can be selected as an SMTP envelope identity.
func openExternalSMTPSecrets(vault *MailOutboxVault, row model.MailExternalAccount) (externalSecrets, error) {
	if vault == nil || row.UserID == uuid.Nil || row.ID == uuid.Nil {
		return externalSecrets{}, errors.New("External SMTP vault is unavailable")
	}
	if _, ok := externalSMTPHosts[row.Provider]; !ok {
		return externalSecrets{}, errors.New("Unsupported external SMTP provider")
	}
	raw, err := vault.Open(row.UserID, row.ID, row.CredentialCiphertext)
	if err != nil {
		return externalSecrets{}, errors.New("Cannot open external SMTP credential")
	}
	var value externalSecrets
	if json.Unmarshal(raw, &value) != nil {
		return externalSecrets{}, errors.New("Invalid external SMTP credential")
	}
	valid, err := validateExternalConnect(ExternalConnectInput{Provider: row.Provider, Address: value.Address, AppPassword: value.AppPassword})
	if err != nil || valid.Address != value.Address || valid.AppPassword != value.AppPassword || valid.Provider != row.Provider {
		return externalSecrets{}, errors.New("Invalid external SMTP sender")
	}
	return value, nil
}

// submitExternalSMTP is invoked only after the durable five-second undo-send
// worker claimed this pending job. Any error is an uncertain result, not a
// signal to silently retry a possibly delivered message.
func (s *StalwartOutboxSubmitter) submitExternalSMTP(ctx context.Context, userID uuid.UUID, accountID string, pendingID uuid.UUID, p outboxPayload) error {
	if s.externalVault == nil || s.externalRemote == nil || p.ExternalAccountID == nil || *p.ExternalAccountID == uuid.Nil {
		return errors.New("External sending is disabled")
	}
	var pending model.MailPendingSend
	if err := s.db.WithContext(ctx).Where("id=? AND user_id=? AND account_id=? AND external_account_id=? AND status=?", pendingID, userID, accountID, *p.ExternalAccountID, "dispatching").Take(&pending).Error; err != nil {
		return errors.New("External pending mail not claimed")
	}
	var row model.MailExternalAccount
	if err := s.db.WithContext(ctx).Where("id=? AND user_id=?", *p.ExternalAccountID, userID).Take(&row).Error; err != nil {
		return errors.New("External sender no longer connected")
	}
	creds, err := openExternalSMTPSecrets(s.externalVault, row)
	if err != nil {
		return err
	}
	if p.Sender != creds.Address || len(p.MIME) == 0 || len(p.MIME) > maxMailSnapshotBytes || len(p.Recipients) == 0 || len(p.Recipients) > 50 {
		return errors.New("External sender or MIME is invalid")
	}
	if _, err = validEnvelopeAddresses(p.Recipients); err != nil {
		return errors.New("Invalid external recipient envelope")
	}
	parsed, err := stdmail.ReadMessage(bytes.NewReader(p.MIME))
	if err != nil {
		return errors.New("Invalid encrypted sender MIME")
	}
	from, err := stdmail.ParseAddress(parsed.Header.Get("From"))
	if err != nil || !strings.EqualFold(from.Address, creds.Address) {
		return errors.New("External sender header mismatch")
	}
	// No administrator credential and no user-supplied SMTP host is used.
	return s.externalRemote.Send(ctx, row.Provider, creds.Address, creds.AppPassword, p)
}

func (liveExternalSMTPRemote) Send(ctx context.Context, provider, username, appPassword string, p outboxPayload) error {
	endpoint, ok := externalSMTPHosts[provider]
	if !ok {
		return errors.New("Unsupported SMTP provider")
	}
	valid, err := validateExternalConnect(ExternalConnectInput{Provider: provider, Address: username, AppPassword: appPassword})
	if err != nil || valid.Address != username || p.Sender != username {
		return errors.New("Invalid SMTP authentication identity")
	}
	if _, err := validEnvelopeAddresses(p.Recipients); err != nil || len(p.Recipients) == 0 {
		return errors.New("Invalid SMTP recipients")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	endpointAddress := net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port))
	dialer := &net.Dialer{Timeout: 12 * time.Second, KeepAlive: -1}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: endpoint.Host}
	var conn net.Conn
	if endpoint.StartTLS {
		conn, err = dialer.DialContext(ctx, "tcp", endpointAddress)
	} else {
		conn, err = tls.DialWithDialer(dialer, "tcp", endpointAddress, tlsConfig)
	}
	if err != nil {
		return errors.New("SMTP TLS connection failed")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(85 * time.Second))
	client, err := smtp.NewClient(conn, endpoint.Host)
	if err != nil {
		return errors.New("SMTP protocol greeting failed")
	}
	defer client.Close()
	if err = client.Hello("atoman.org"); err != nil {
		return errors.New("SMTP greeting failed")
	}
	if endpoint.StartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("Provider does not offer mandatory STARTTLS")
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return errors.New("SMTP STARTTLS failed")
		}
	}
	if ok, _ := client.Extension("AUTH"); !ok {
		return errors.New("Provider SMTP authentication unavailable")
	}
	if err = client.Auth(smtp.PlainAuth("", username, appPassword, endpoint.Host)); err != nil {
		return errors.New("Provider rejected SMTP credentials")
	}
	if err = client.Mail(p.Sender); err != nil {
		return errors.New("Provider rejected sender envelope")
	}
	for _, recipient := range p.Recipients {
		if err = client.Rcpt(recipient); err != nil {
			return errors.New("Provider rejected recipient envelope")
		}
	}
	writer, err := client.Data()
	if err != nil {
		return errors.New("SMTP DATA not accepted")
	}
	if _, err = io.Copy(writer, bytes.NewReader(p.MIME)); err != nil {
		return errors.New("SMTP body delivery outcome unknown")
	}
	if err = writer.Close(); err != nil {
		return errors.New("SMTP final acceptance outcome unknown")
	}
	// A positive final DATA response establishes acceptance. QUIT failure does
	// not downgrade an accepted mail to retryable or uncertain.
	_ = client.Quit()
	return nil
}
