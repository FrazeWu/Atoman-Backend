package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	jmapCoreCapability  = "urn:ietf:params:jmap:core"
	jmapMailCapability  = "urn:ietf:params:jmap:mail"
	jmapAdminCapability = "urn:stalwart:jmap"
	maxStalwartResponse = 2 << 20
)

// StalwartCredential never leaves Atoman backend memory. Do not store its
// secret in user-facing Atoman sessions, URLs, logs or error messages.
type StalwartCredential struct {
	typeName string
	username string
	secret   string
}

func StalwartBearer(secret string) StalwartCredential {
	return StalwartCredential{typeName: "Bearer", secret: secret}
}

func StalwartBasic(username, password string) StalwartCredential {
	return StalwartCredential{typeName: "Basic", username: username, secret: password}
}

func (c StalwartCredential) authorization() (string, error) {
	if c.secret == "" || strings.ContainsAny(c.secret, "\r\n") {
		return "", errors.New("Stalwart credential is missing")
	}
	switch c.typeName {
	case "Bearer":
		return "Bearer " + c.secret, nil
	case "Basic":
		if c.username == "" || strings.ContainsAny(c.username, ":\r\n") {
			return "", errors.New("Stalwart account name is invalid")
		}
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.username+":"+c.secret)), nil
	default:
		return "", errors.New("Unsupported Stalwart credential type")
	}
}

// StalwartJMAPClient is server-side only. HTTP is allowed only for loopback
// staging/service bindings. Use TLS for non-loopback endpoints.
type StalwartJMAPClient struct {
	base string
	http *http.Client
}

func NewStalwartJMAPClient(rawURL string, transport *http.Client) (*StalwartJMAPClient, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u == nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		u.Path != "" || u.RawPath != "" || u.Opaque != "" || u.ForceQuery {
		return nil, errors.New("Invalid internal Stalwart endpoint")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return nil, errors.New("Stalwart HTTP endpoint must be loopback only")
		}
	}
	if transport == nil {
		transport = &http.Client{Timeout: 10 * time.Second}
	}
	copyClient := *transport
	if copyClient.Timeout == 0 || copyClient.Timeout > 30*time.Second {
		copyClient.Timeout = 10 * time.Second
	}
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &StalwartJMAPClient{base: strings.TrimRight(u.String(), "/"), http: &copyClient}, nil
}

func (s *StalwartJMAPClient) request(ctx context.Context, cred StalwartCredential, method, path string, body any, target any) error {
	if s == nil || s.http == nil {
		return errors.New("Stalwart client is not configured")
	}
	header, err := cred.authorization()
	if err != nil {
		return err
	}
	var data io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal JMAP request: %w", err)
		}
		data = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+path, data)
	if err != nil {
		return errors.New("Invalid Stalwart request")
	}
	req.Header.Set("Authorization", header)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := s.http.Do(req)
	if err != nil {
		return errors.New("Stalwart network or TLS request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Stalwart HTTP status %d", response.StatusCode)
	}
	limited := io.LimitReader(response.Body, maxStalwartResponse+1)
	blob, err := io.ReadAll(limited)
	if err != nil || len(blob) > maxStalwartResponse {
		return errors.New("Stalwart response unreadable or too large")
	}
	if target != nil {
		if err := json.Unmarshal(blob, target); err != nil {
			return errors.New("Stalwart returned invalid JSON")
		}
	}
	return nil
}

func (s *StalwartJMAPClient) call(ctx context.Context, cred StalwartCredential, admin bool, method string, args any, target any) error {
	using := []string{jmapCoreCapability, jmapMailCapability}
	if admin {
		using = []string{jmapCoreCapability, jmapAdminCapability}
	}
	request := map[string]any{
		"using":       using,
		"methodCalls": []any{[]any{method, args, "c1"}},
	}
	var envelope struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	if err := s.request(ctx, cred, http.MethodPost, "/jmap/", request, &envelope); err != nil {
		return err
	}
	if len(envelope.MethodResponses) != 1 || len(envelope.MethodResponses[0]) != 3 {
		return errors.New("Stalwart returned an unexpected JMAP response")
	}
	var receivedMethod, callID string
	if err := json.Unmarshal(envelope.MethodResponses[0][0], &receivedMethod); err != nil {
		return errors.New("Stalwart JMAP response method is invalid")
	}
	if err := json.Unmarshal(envelope.MethodResponses[0][2], &callID); err != nil || callID != "c1" {
		return errors.New("Stalwart JMAP response correlation failed")
	}
	if receivedMethod == "error" {
		var protocolError struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(envelope.MethodResponses[0][1], &protocolError)
		return fmt.Errorf("Stalwart JMAP error: %s", safeJMAPErrorType(protocolError.Type))
	}
	if receivedMethod != method {
		return errors.New("Stalwart JMAP response type mismatch")
	}
	if target != nil {
		if err := json.Unmarshal(envelope.MethodResponses[0][1], target); err != nil {
			return errors.New("Stalwart JMAP response data is invalid")
		}
	}
	return nil
}

func safeJMAPErrorType(raw string) string {
	if raw == "" || len(raw) > 80 {
		return "unknown"
	}
	for _, r := range raw {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return "unknown"
		}
	}
	return raw
}

// JMAPSession contains no credentials, and is fetched as the intended user,
// not by a browser directly connecting to the Stalwart administrator API.
type JMAPSession struct {
	Username        string            `json:"username"`
	PrimaryAccounts map[string]string `json:"primaryAccounts"`
	Accounts        map[string]struct {
		Name string `json:"name"`
	} `json:"accounts"`
	Capabilities map[string]json.RawMessage `json:"capabilities"`
}

func (s *StalwartJMAPClient) Session(ctx context.Context, cred StalwartCredential) (JMAPSession, error) {
	var out JMAPSession
	if err := s.request(ctx, cred, http.MethodGet, "/jmap/session", nil, &out); err != nil {
		return out, err
	}
	if out.PrimaryAccounts[jmapMailCapability] == "" {
		return JMAPSession{}, errors.New("Stalwart session has no primary mail account")
	}
	return out, nil
}

// MailboxInfo and EmailSummary contain safe structured metadata only; they
// never expose raw HTML, attachment blobs, or admin-owned account data.
type MailboxInfo struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	ParentID     *string `json:"parentId"`
	Role         *string `json:"role"`
	TotalEmails  uint64  `json:"totalEmails"`
	UnreadEmails uint64  `json:"unreadEmails"`
}

type EmailSummary struct {
	MailboxIDs    map[string]bool `json:"mailboxIds"`
	ID            string          `json:"id"`
	ThreadID      string          `json:"threadId"`
	Subject       string          `json:"subject"`
	ReceivedAt    string          `json:"receivedAt"`
	Preview       string          `json:"preview"`
	HasAttachment bool            `json:"hasAttachment"`
	From          []MailAddress   `json:"from"`
	To            []MailAddress   `json:"to"`
	Keywords      map[string]bool `json:"keywords"`
	IsRead        bool            `json:"isRead"`
	IsStarred     bool            `json:"isStarred"`
}

func (s *StalwartJMAPClient) Mailboxes(ctx context.Context, userKey StalwartCredential, accountID string) ([]MailboxInfo, error) {
	if accountID == "" {
		return nil, errors.New("Missing Stalwart account id")
	}
	var response struct {
		List     []MailboxInfo `json:"list"`
		NotFound []string      `json:"notFound"`
	}
	args := map[string]any{"accountId": accountID, "ids": nil, "properties": []string{"id", "name", "parentId", "role", "totalEmails", "unreadEmails"}}
	if err := s.call(ctx, userKey, false, "Mailbox/get", args, &response); err != nil {
		return nil, err
	}
	if len(response.NotFound) != 0 {
		return nil, errors.New("Stalwart mailboxes were not accessible")
	}
	return response.List, nil
}

func (s *StalwartJMAPClient) ListEmails(ctx context.Context, userKey StalwartCredential, accountID, mailboxID string, limit int) ([]EmailSummary, error) {
	if accountID == "" || mailboxID == "" || limit < 1 || limit > 100 {
		return nil, errors.New("Invalid mail search scope or limit")
	}
	var query struct {
		IDs []string `json:"ids"`
	}
	args := map[string]any{
		"accountId": accountID,
		"filter":    map[string]any{"inMailbox": mailboxID},
		"sort":      []map[string]any{{"property": "receivedAt", "isAscending": false}},
		"limit":     limit,
	}
	if err := s.call(ctx, userKey, false, "Email/query", args, &query); err != nil {
		return nil, err
	}
	if len(query.IDs) == 0 {
		return []EmailSummary{}, nil
	}
	if len(query.IDs) > limit {
		return nil, errors.New("Stalwart returned more email ids than requested")
	}
	var got struct {
		List     []EmailSummary `json:"list"`
		NotFound []string       `json:"notFound"`
	}
	getArgs := map[string]any{"accountId": accountID, "ids": query.IDs, "properties": []string{"id", "threadId", "subject", "receivedAt", "preview", "hasAttachment", "from", "to", "keywords", "mailboxIds"}}
	if err := s.call(ctx, userKey, false, "Email/get", getArgs, &got); err != nil {
		return nil, err
	}
	if len(got.NotFound) > 0 {
		return nil, errors.New("Stalwart changed during email fetch; retry state sync")
	}
	for i := range got.List {
		got.List[i].IsRead = got.List[i].Keywords["$seen"]
		got.List[i].IsStarred = got.List[i].Keywords["$flagged"]
	}
	return got.List, nil
}

type StalwartCreatedUser struct {
	AccountID string
}

// CreateUser uses an empty primary-credentials map: Atoman site passwords must
// never be copied into Stalwart. Provisioning requires separate, server-side
// credential vault logic before any account may become active.
func (s *StalwartJMAPClient) CreateUser(ctx context.Context, admin StalwartCredential, domainID, localPart string, atomanUserID uuid.UUID, diskQuotaBytes uint64) (StalwartCreatedUser, error) {
	if atomanUserID == uuid.Nil || domainID == "" || diskQuotaBytes < (1<<20) || diskQuotaBytes > (100<<30) {
		return StalwartCreatedUser{}, errors.New("Invalid Stalwart user provisioning parameters")
	}
	local, err := validateLocalPart(localPart)
	if err != nil || local != localPart {
		return StalwartCreatedUser{}, errors.New("Invalid reserved mailbox local part")
	}
	user := map[string]any{
		"@type": "User", "name": local, "domainId": domainID,
		"description": "Atoman user " + atomanUserID.String(),
		"credentials": map[string]any{}, "aliases": map[string]any{},
		"memberGroupIds": map[string]any{}, "roles": map[string]string{"@type": "User"},
		"permissions":      map[string]string{"@type": "Inherit"},
		"quotas":           map[string]uint64{"maxDiskQuota": diskQuotaBytes},
		"encryptionAtRest": map[string]string{"@type": "Disabled"},
	}
	var response struct {
		Created map[string]struct {
			ID string `json:"id"`
		} `json:"created"`
		NotCreated map[string]struct {
			Type string `json:"type"`
		} `json:"notCreated"`
	}
	if err := s.call(ctx, admin, true, "x:Account/set", map[string]any{"create": map[string]any{"u1": user}}, &response); err != nil {
		return StalwartCreatedUser{}, err
	}
	if response.NotCreated["u1"].Type != "" || response.Created["u1"].ID == "" {
		return StalwartCreatedUser{}, fmt.Errorf("Stalwart account not created: %s", safeJMAPErrorType(response.NotCreated["u1"].Type))
	}
	return StalwartCreatedUser{AccountID: response.Created["u1"].ID}, nil
}

// CreateUserAPIKey returns a secret that is only visible once. Callers MUST
// store it in an encrypted, server-only vault before treating the mailbox as
// active. This adapter deliberately has no HTTP route or persistence path.
func (s *StalwartJMAPClient) CreateUserAPIKey(ctx context.Context, admin StalwartCredential, accountID string) (keyID, oneTimeSecret string, err error) {
	if accountID == "" {
		return "", "", errors.New("Missing target Stalwart account")
	}
	args := map[string]any{"accountId": accountID, "create": map[string]any{"k1": map[string]any{
		"description": "Atoman Webmail Backend", "permissions": map[string]string{"@type": "Inherit"}, "allowedIps": map[string]any{},
	}}}
	var response struct {
		Created map[string]struct {
			ID     string `json:"id"`
			Secret string `json:"secret"`
		} `json:"created"`
		NotCreated map[string]struct {
			Type string `json:"type"`
		} `json:"notCreated"`
	}
	if err := s.call(ctx, admin, true, "x:ApiKey/set", args, &response); err != nil {
		return "", "", err
	}
	created := response.Created["k1"]
	if created.ID == "" || created.Secret == "" || response.NotCreated["k1"].Type != "" {
		return "", "", errors.New("Stalwart user API key was not created")
	}
	return created.ID, created.Secret, nil
}

// FindDomainID never auto-creates production DNS domains; the operator must
// first configure an authorized mail domain in Stalwart.
func (s *StalwartJMAPClient) FindDomainID(ctx context.Context, admin StalwartCredential, name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", errors.New("Missing Stalwart domain")
	}
	var queried struct {
		IDs []string `json:"ids"`
	}
	if err := s.call(ctx, admin, true, "x:Domain/query", map[string]any{"filter": map[string]string{"name": name}}, &queried); err != nil {
		return "", err
	}
	if len(queried.IDs) == 0 {
		return "", errors.New("Stalwart mail domain has not been configured")
	}
	var details struct {
		List []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"list"`
	}
	if err := s.call(ctx, admin, true, "x:Domain/get", map[string]any{"ids": queried.IDs}, &details); err != nil {
		return "", err
	}
	for _, domain := range details.List {
		if strings.EqualFold(domain.Name, name) {
			return domain.ID, nil
		}
	}
	return "", errors.New("Stalwart domain query did not match requested address")
}

// RevokeUserAPIKey is a compensating action when encrypted key persistence
// fails. Failure must keep the account non-active for manual reconciliation.
func (s *StalwartJMAPClient) RevokeUserAPIKey(ctx context.Context, admin StalwartCredential, accountID, keyID string) error {
	if accountID == "" || keyID == "" {
		return errors.New("Missing Stalwart account or key id")
	}
	args := map[string]any{"accountId": accountID, "destroy": []string{keyID}}
	var response struct {
		Destroyed    []string       `json:"destroyed"`
		NotDestroyed map[string]any `json:"notDestroyed"`
	}
	if err := s.call(ctx, admin, true, "x:ApiKey/set", args, &response); err != nil {
		return err
	}
	for _, id := range response.Destroyed {
		if id == keyID {
			return nil
		}
	}
	return errors.New("Stalwart API key revocation was not confirmed")
}
