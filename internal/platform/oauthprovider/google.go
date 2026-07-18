package oauthprovider

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

const (
	googleAuthorizeURL = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL     = "https://oauth2.googleapis.com/token"
	googleJWKSURL      = "https://www.googleapis.com/oauth2/v3/certs"
)

type GoogleConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

type GoogleProvider struct {
	config     GoogleConfig
	tokenURL   string
	httpClient *http.Client
	verifier   idTokenVerifier
}

func NewGoogleProvider(config GoogleConfig) (*GoogleProvider, error) {
	config.ClientID = strings.TrimSpace(config.ClientID)
	config.ClientSecret = strings.TrimSpace(config.ClientSecret)
	config.RedirectURL = strings.TrimSpace(config.RedirectURL)
	if config.ClientID == "" || config.ClientSecret == "" || config.RedirectURL == "" {
		return nil, errors.New("google oauth configuration is incomplete")
	}
	return &GoogleProvider{
		config:     config,
		tokenURL:   googleTokenURL,
		httpClient: http.DefaultClient,
		verifier:   newRemoteIDTokenVerifier(config.ClientID, googleJWKSURL),
	}, nil
}

func (p *GoogleProvider) Name() string {
	return "google"
}

func (p *GoogleProvider) AuthorizationURL(req AuthorizationRequest) (string, error) {
	query := url.Values{
		"client_id":             {p.config.ClientID},
		"redirect_uri":          {p.config.RedirectURL},
		"response_type":         {"code"},
		"scope":                 {"openid email profile"},
		"state":                 {req.State},
		"nonce":                 {req.Nonce},
		"code_challenge":        {req.CodeChallenge},
		"code_challenge_method": {"S256"},
		"prompt":                {"select_account"},
	}
	return googleAuthorizeURL + "?" + query.Encode(), nil
}

func (p *GoogleProvider) Exchange(ctx context.Context, req CallbackRequest) (Profile, error) {
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
		return Profile{}, errors.New("google token response has no id_token")
	}
	claims, err := p.verifier.Verify(ctx, token.IDToken)
	if err != nil {
		return Profile{}, err
	}
	if err := requireIssuer(claims.Issuer, "https://accounts.google.com", "accounts.google.com"); err != nil {
		return Profile{}, err
	}
	if err := validateOIDCNonce(claims.Nonce, req.NonceHash); err != nil {
		return Profile{}, err
	}
	if strings.TrimSpace(claims.Subject) == "" {
		return Profile{}, errors.New("google identity has no subject")
	}
	return Profile{
		Issuer: claims.Issuer, Subject: claims.Subject, Email: claims.Email,
		EmailVerified: claims.EmailVerified, DisplayName: claims.Name, AvatarURL: claims.Picture,
	}, nil
}
