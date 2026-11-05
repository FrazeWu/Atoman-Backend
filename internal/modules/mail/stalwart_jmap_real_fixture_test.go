package mail

import (
	"context"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestStalwartJMAPRealFixture runs only against a disposable, loopback-bound
// Stalwart + PostgreSQL + S3 fixture. It must not use production credentials.
func TestStalwartJMAPRealFixture(t *testing.T) {
	if os.Getenv("ATOMAN_MAIL_STAGING_JMAP") != "1" {
		t.Skip("requires disposable local Stalwart staging fixture")
	}
	endpoint := os.Getenv("ATOMAN_MAIL_STAGING_JMAP_URL")
	adminName := os.Getenv("ATOMAN_MAIL_STAGING_ADMIN_USERNAME")
	adminPassword := os.Getenv("ATOMAN_MAIL_STAGING_ADMIN_PASSWORD")
	if endpoint != "http://127.0.0.1:18582" || adminName == "" || adminPassword == "" {
		t.Fatal("staging JMAP fixture is not configured for the expected loopback endpoint")
	}
	client, err := NewStalwartJMAPClient(endpoint, nil)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	admin := StalwartBasic(adminName, adminPassword)
	const stageDomain = "atoman-stage.test"
	const stageLocalPart = "stage-reader"

	var createdDomain struct {
		Created map[string]struct {
			ID string `json:"id"`
		} `json:"created"`
		NotCreated map[string]struct {
			Type string `json:"type"`
		} `json:"notCreated"`
	}
	err = client.call(ctx, admin, true, "x:Domain/set", map[string]any{
		"create": map[string]any{"d1": map[string]any{"name": stageDomain, "enable": true}},
	}, &createdDomain)
	require.NoError(t, err)
	require.Empty(t, createdDomain.NotCreated)
	domainID := createdDomain.Created["d1"].ID
	require.NotEmpty(t, domainID)

	foundDomain, err := client.FindDomainID(ctx, admin, stageDomain)
	require.NoError(t, err)
	require.Equal(t, domainID, foundDomain)

	createdUser, err := client.CreateUser(ctx, admin, domainID, stageLocalPart, uuid.New(), 512<<20)
	require.NoError(t, err)
	require.NotEmpty(t, createdUser.AccountID)

	keyID, key, err := client.CreateUserAPIKey(ctx, admin, createdUser.AccountID)
	require.NoError(t, err)
	require.NotEmpty(t, keyID)
	require.NotEmpty(t, key)
	userKey := StalwartBearer(key)

	session, err := client.Session(ctx, userKey)
	require.NoError(t, err)
	require.Equal(t, createdUser.AccountID, session.PrimaryAccounts[jmapMailCapability])

	// Delivery stays inside the localhost staging SMTP listener.
	mail := fmt.Sprintf("From: probe@external.test\r\nTo: %s@%s\r\nSubject: Atoman JMAP synthetic mailbox probe\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nAtoman staging JMAP body test.\r\n", stageLocalPart, stageDomain)
	conn, err := net.DialTimeout("tcp", "127.0.0.1:19025", 6*time.Second)
	require.NoError(t, err)
	smtpClient, err := smtp.NewClient(conn, "127.0.0.1")
	require.NoError(t, err)
	defer smtpClient.Close()
	require.NoError(t, smtpClient.Hello("staging.example.test"))
	require.NoError(t, smtpClient.Mail("probe@external.test"))
	require.NoError(t, smtpClient.Rcpt(stageLocalPart+"@"+stageDomain))
	writer, err := smtpClient.Data()
	require.NoError(t, err)
	_, err = writer.Write([]byte(mail))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, smtpClient.Quit())

	var emails []EmailSummary
	var deliveredRole string
	for attempt := 0; attempt < 20; attempt++ {
		folders, err := client.Mailboxes(ctx, userKey, createdUser.AccountID)
		require.NoError(t, err)
		for _, folder := range folders {
			if folder.Role != nil && (*folder.Role == "inbox" || *folder.Role == "junk") {
				found, err := client.ListEmails(ctx, userKey, createdUser.AccountID, folder.ID, 10)
				require.NoError(t, err)
				for _, candidate := range found {
					if candidate.Subject == "Atoman JMAP synthetic mailbox probe" {
						emails = append(emails, candidate)
						deliveredRole = *folder.Role
					}
				}
			}
		}
		if len(emails) > 0 {
			break
		}
		time.Sleep(time.Second)
	}
	require.Len(t, emails, 1)
	require.Equal(t, "Atoman JMAP synthetic mailbox probe", emails[0].Subject)
	require.Contains(t, []string{"inbox", "junk"}, deliveredRole)
	// Exercise actual Stalwart v1 Email/get bodyValues and read-state patch.
	var targetID string
	folders, err := client.Mailboxes(ctx, userKey, createdUser.AccountID)
	require.NoError(t, err)
	for _, folder := range folders {
		if folder.Role != nil && *folder.Role == deliveredRole {
			found, err := client.ListEmails(ctx, userKey, createdUser.AccountID, folder.ID, 10)
			require.NoError(t, err)
			for _, candidate := range found {
				if candidate.Subject == "Atoman JMAP synthetic mailbox probe" {
					targetID = folder.ID
					break
				}
			}
		}
	}
	require.NotEmpty(t, targetID)
	detail, err := client.ReadEmailDetail(ctx, userKey, createdUser.AccountID, targetID, emails[0].ID)
	require.NoError(t, err)
	require.Contains(t, detail.PlainText, "Atoman staging JMAP body test")
	require.Equal(t, "probe@external.test", detail.From[0].Email)
	require.NoError(t, client.MarkEmailSeen(ctx, userKey, createdUser.AccountID, targetID, emails[0].ID))
	confirmed, err := client.ReadEmailDetail(ctx, userKey, createdUser.AccountID, targetID, emails[0].ID)
	require.NoError(t, err)
	require.True(t, confirmed.IsRead)
	t.Log("STALWART_JMAP_TEXT_BODY_AND_EXPAND_ONLY_READ_STATE_VERIFIED")
	t.Logf("STALWART_JMAP_DELIVERY_MAILBOX_ROLE=%s", deliveredRole)
	if os.Getenv("ATOMAN_MAIL_STAGE9_SUBMIT") == "1" {
		subject := "Atoman Stage9 local JMAP outbound synthetic"
		sender := stageLocalPart + "@" + stageDomain
		message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: Thu, 05 Nov 2026 03:00:00 +0000\r\nMessage-ID: <%s@atoman-stage.test>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nStage 9 synthetic JMAP internal delivery only.\r\n", sender, sender, subject, uuid.NewString())
		payload := outboxPayload{Sender: sender, Recipients: []string{sender}, MIME: []byte(message)}
		require.NoError(t, client.SubmitRFC822(ctx, userKey, createdUser.AccountID, payload))
		delivered := false
		for attempt := 0; attempt < 17 && !delivered; attempt++ {
			listed, err := client.Mailboxes(ctx, userKey, createdUser.AccountID)
			require.NoError(t, err)
			for _, folder := range listed {
				if folder.Role == nil || (*folder.Role != "inbox" && *folder.Role != "junk") {
					continue
				}
				contents, err := client.ListEmails(ctx, userKey, createdUser.AccountID, folder.ID, 30)
				require.NoError(t, err)
				for _, candidate := range contents {
					if candidate.Subject == subject {
						delivered = true
						break
					}
				}
			}
			if !delivered {
				time.Sleep(time.Second)
			}
		}
		require.True(t, delivered, "the isolated Stalwart recipient must receive its own synthetic submitted message")
		t.Log("STALWART_JMAP_SUBMISSION_AND_INTERNAL_DELIVERY_VERIFIED")
	}
	t.Log("STALWART_JMAP_DOMAIN_ACCOUNT_KEY_MAILBOX_EMAIL_VERIFIED")
}
