package oauthprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	githubAuthorizeURL = "https://github.com/login/oauth/authorize"
	githubTokenURL     = "https://github.com/login/oauth/access_token"
	githubAPIURL       = "https://api.github.com"
)

type GitHubConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

type GitHubProvider struct {
	config     GitHubConfig
	tokenURL   string
	apiURL     string
	httpClient *http.Client
}

func NewGitHubProvider(config GitHubConfig) (*GitHubProvider, error) {
	config.ClientID = strings.TrimSpace(config.ClientID)
	config.ClientSecret = strings.TrimSpace(config.ClientSecret)
	config.RedirectURL = strings.TrimSpace(config.RedirectURL)
	if config.ClientID == "" || config.ClientSecret == "" || config.RedirectURL == "" {
		return nil, errors.New("github oauth configuration is incomplete")
	}
	return &GitHubProvider{
		config: config, tokenURL: githubTokenURL, apiURL: githubAPIURL, httpClient: http.DefaultClient,
	}, nil
}

func (p *GitHubProvider) Name() string {
	return "github"
}

func (p *GitHubProvider) AuthorizationURL(req AuthorizationRequest) (string, error) {
	query := url.Values{
		"client_id":             {p.config.ClientID},
		"redirect_uri":          {p.config.RedirectURL},
		"scope":                 {"read:user user:email"},
		"state":                 {req.State},
		"code_challenge":        {req.CodeChallenge},
		"code_challenge_method": {"S256"},
	}
	return githubAuthorizeURL + "?" + query.Encode(), nil
}

func (p *GitHubProvider) Exchange(ctx context.Context, req CallbackRequest) (Profile, error) {
	token, err := exchangeOAuthToken(ctx, p.httpClient, p.tokenURL, url.Values{
		"client_id":     {p.config.ClientID},
		"client_secret": {p.config.ClientSecret},
		"redirect_uri":  {p.config.RedirectURL},
		"code":          {req.Code},
		"code_verifier": {req.CodeVerifier},
	})
	if err != nil {
		return Profile{}, err
	}
	if token.AccessToken == "" {
		return Profile{}, errors.New("github token response has no access_token")
	}

	var user struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := p.getJSON(ctx, token.AccessToken, "/user", &user); err != nil {
		return Profile{}, err
	}
	if user.ID <= 0 {
		return Profile{}, errors.New("github identity has no id")
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := p.getJSON(ctx, token.AccessToken, "/user/emails", &emails); err != nil {
		return Profile{}, err
	}
	email := ""
	for _, candidate := range emails {
		if candidate.Verified && candidate.Primary {
			email = candidate.Email
			break
		}
		if candidate.Verified && email == "" {
			email = candidate.Email
		}
	}
	displayName := strings.TrimSpace(user.Name)
	if displayName == "" {
		displayName = user.Login
	}
	return Profile{
		Issuer: "https://github.com", Subject: strconv.FormatInt(user.ID, 10),
		Email: strings.ToLower(strings.TrimSpace(email)), EmailVerified: email != "",
		DisplayName: displayName, AvatarURL: user.AvatarURL,
	}, nil
}

func (p *GitHubProvider) getJSON(ctx context.Context, accessToken string, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.apiURL, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return fmt.Errorf("github api returned status %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target)
}
