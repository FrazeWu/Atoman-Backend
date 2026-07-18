package oauthprovider

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	appleAuthorizeURL = "https://appleid.apple.com/auth/authorize"
	appleTokenURL     = "https://appleid.apple.com/auth/token"
	appleJWKSURL      = "https://appleid.apple.com/auth/keys"
)

type AppleConfig struct {
	ClientID    string
	TeamID      string
	KeyID       string
	PrivateKey  string
	RedirectURL string
}

type AppleProvider struct {
	config     AppleConfig
	privateKey *ecdsa.PrivateKey
	tokenURL   string
	httpClient *http.Client
	verifier   idTokenVerifier
	now        func() time.Time
}

func NewAppleProvider(config AppleConfig) (*AppleProvider, error) {
	config.ClientID = strings.TrimSpace(config.ClientID)
	config.TeamID = strings.TrimSpace(config.TeamID)
	config.KeyID = strings.TrimSpace(config.KeyID)
	config.PrivateKey = strings.TrimSpace(strings.ReplaceAll(config.PrivateKey, `\n`, "\n"))
	config.RedirectURL = strings.TrimSpace(config.RedirectURL)
	if config.ClientID == "" || config.TeamID == "" || config.KeyID == "" || config.PrivateKey == "" || config.RedirectURL == "" {
		return nil, errors.New("apple oauth configuration is incomplete")
	}
	privateKey, err := parseApplePrivateKey(config.PrivateKey)
	if err != nil {
		return nil, err
	}
	return &AppleProvider{
		config: config, privateKey: privateKey, tokenURL: appleTokenURL,
		httpClient: http.DefaultClient,
		verifier:   newRemoteIDTokenVerifier(config.ClientID, appleJWKSURL),
		now:        time.Now,
	}, nil
}

func (p *AppleProvider) Name() string {
	return "apple"
}

func (p *AppleProvider) AuthorizationURL(req AuthorizationRequest) (string, error) {
	query := url.Values{
		"client_id":     {p.config.ClientID},
		"redirect_uri":  {p.config.RedirectURL},
		"response_type": {"code"},
		"response_mode": {"form_post"},
		"scope":         {"name email"},
		"state":         {req.State},
		"nonce":         {req.Nonce},
	}
	return appleAuthorizeURL + "?" + query.Encode(), nil
}

func (p *AppleProvider) Exchange(ctx context.Context, req CallbackRequest) (Profile, error) {
	clientSecret, err := p.clientSecret()
	if err != nil {
		return Profile{}, err
	}
	token, err := exchangeOAuthToken(ctx, p.httpClient, p.tokenURL, url.Values{
		"client_id":     {p.config.ClientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {p.config.RedirectURL},
		"grant_type":    {"authorization_code"},
		"code":          {req.Code},
	})
	if err != nil {
		return Profile{}, err
	}
	if token.IDToken == "" {
		return Profile{}, errors.New("apple token response has no id_token")
	}
	claims, err := p.verifier.Verify(ctx, token.IDToken)
	if err != nil {
		return Profile{}, err
	}
	if err := requireIssuer(claims.Issuer, "https://appleid.apple.com"); err != nil {
		return Profile{}, err
	}
	if err := validateOIDCNonce(claims.Nonce, req.NonceHash); err != nil {
		return Profile{}, err
	}
	if strings.TrimSpace(claims.Subject) == "" {
		return Profile{}, errors.New("apple identity has no subject")
	}

	displayName := strings.TrimSpace(claims.Name)
	if displayName == "" && strings.TrimSpace(req.RawUser) != "" {
		var appleUser struct {
			Name struct {
				FirstName string `json:"firstName"`
				LastName  string `json:"lastName"`
			} `json:"name"`
		}
		if err := json.Unmarshal([]byte(req.RawUser), &appleUser); err != nil {
			return Profile{}, errors.New("apple user profile is invalid")
		}
		displayName = strings.TrimSpace(strings.TrimSpace(appleUser.Name.FirstName) + " " + strings.TrimSpace(appleUser.Name.LastName))
	}
	return Profile{
		Issuer: claims.Issuer, Subject: claims.Subject,
		Email: strings.ToLower(strings.TrimSpace(claims.Email)), EmailVerified: claims.EmailVerified,
		DisplayName: displayName, AvatarURL: claims.Picture,
	}, nil
}

func (p *AppleProvider) clientSecret() (string, error) {
	now := p.now().UTC()
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.RegisteredClaims{
		Issuer:    p.config.TeamID,
		Subject:   p.config.ClientID,
		Audience:  jwt.ClaimStrings{"https://appleid.apple.com"},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
	})
	token.Header["kid"] = p.config.KeyID
	return token.SignedString(p.privateKey)
}

func parseApplePrivateKey(raw string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, errors.New("apple private key is not valid PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err == nil {
		key, ok := parsed.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("apple private key is not ECDSA")
		}
		return key, nil
	}
	key, ecErr := x509.ParseECPrivateKey(block.Bytes)
	if ecErr != nil {
		return nil, errors.New("apple private key cannot be parsed")
	}
	return key, nil
}
