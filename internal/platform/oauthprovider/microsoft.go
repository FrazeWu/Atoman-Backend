package oauthprovider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const microsoftJWKSURL = "https://login.microsoftonline.com/common/discovery/v2.0/keys"

var microsoftTenantPattern = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)

type MicrosoftConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Tenant       string
}

type MicrosoftProvider struct {
	config       MicrosoftConfig
	authorizeURL string
	tokenURL     string
	httpClient   *http.Client
	verifier     idTokenVerifier
}

func NewMicrosoftProvider(config MicrosoftConfig) (*MicrosoftProvider, error) {
	config.ClientID = strings.TrimSpace(config.ClientID)
	config.ClientSecret = strings.TrimSpace(config.ClientSecret)
	config.RedirectURL = strings.TrimSpace(config.RedirectURL)
	config.Tenant = strings.TrimSpace(config.Tenant)
	if config.Tenant == "" {
		config.Tenant = "common"
	}
	if config.ClientID == "" || config.ClientSecret == "" || config.RedirectURL == "" {
		return nil, errors.New("microsoft oauth configuration is incomplete")
	}
	if !microsoftTenantPattern.MatchString(config.Tenant) {
		return nil, errors.New("microsoft oauth tenant is invalid")
	}
	base := "https://login.microsoftonline.com/" + config.Tenant + "/oauth2/v2.0"
	return &MicrosoftProvider{
		config:       config,
		authorizeURL: base + "/authorize",
		tokenURL:     base + "/token",
		httpClient:   http.DefaultClient,
		verifier:     newRemoteIDTokenVerifier(config.ClientID, microsoftJWKSURL),
	}, nil
}

func (p *MicrosoftProvider) Name() string {
	return "microsoft"
}

func (p *MicrosoftProvider) AuthorizationURL(req AuthorizationRequest) (string, error) {
	query := url.Values{
		"client_id":             {p.config.ClientID},
		"redirect_uri":          {p.config.RedirectURL},
		"response_type":         {"code"},
		"response_mode":         {"query"},
		"scope":                 {"openid profile email"},
		"state":                 {req.State},
		"nonce":                 {req.Nonce},
		"code_challenge":        {req.CodeChallenge},
		"code_challenge_method": {"S256"},
		"prompt":                {"select_account"},
	}
	return p.authorizeURL + "?" + query.Encode(), nil
}

func (p *MicrosoftProvider) Exchange(ctx context.Context, req CallbackRequest) (Profile, error) {
	token, err := exchangeOAuthToken(ctx, p.httpClient, p.tokenURL, url.Values{
		"client_id":     {p.config.ClientID},
		"client_secret": {p.config.ClientSecret},
		"redirect_uri":  {p.config.RedirectURL},
		"grant_type":    {"authorization_code"},
		"code":          {req.Code},
		"code_verifier": {req.CodeVerifier},
	})
	if err != nil {
		return Profile{}, err
	}
	if token.IDToken == "" {
		return Profile{}, errors.New("microsoft token response has no id_token")
	}
	claims, err := p.verifier.Verify(ctx, token.IDToken)
	if err != nil {
		return Profile{}, err
	}
	if strings.TrimSpace(claims.TenantID) == "" {
		return Profile{}, errors.New("microsoft identity has no tenant id")
	}
	expectedIssuer := "https://login.microsoftonline.com/" + claims.TenantID + "/v2.0"
	if claims.Issuer != expectedIssuer {
		return Profile{}, fmt.Errorf("unexpected microsoft issuer %q", claims.Issuer)
	}
	if err := validateOIDCNonce(claims.Nonce, req.NonceHash); err != nil {
		return Profile{}, err
	}
	if strings.TrimSpace(claims.Subject) == "" {
		return Profile{}, errors.New("microsoft identity has no subject")
	}
	email := strings.TrimSpace(claims.Email)
	if email == "" && strings.Contains(claims.PreferredUsername, "@") {
		email = strings.TrimSpace(claims.PreferredUsername)
	}
	return Profile{
		Issuer: claims.Issuer, Subject: claims.Subject,
		Email: strings.ToLower(email), EmailVerified: email != "",
		DisplayName: claims.Name, AvatarURL: claims.Picture,
	}, nil
}
