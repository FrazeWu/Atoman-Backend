package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	feedImageProxyMaxResponseBytes = 8 * 1024 * 1024
	feedImageProxyMinimumSecret    = 32
)

var (
	ErrFeedImageProxyDisabled  = errors.New("feed image proxy disabled")
	ErrFeedImageProxySignature = errors.New("invalid feed image proxy signature")
	ErrFeedImageProxyResponse  = errors.New("invalid feed image proxy response")
)

var feedImageProxyHTTPClient = &http.Client{
	Timeout:   12 * time.Second,
	Transport: newFullTextSafeHTTPTransport(),
	CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) >= fullTextMaxRedirects {
			return errors.New(fullTextRedirectLimitMessage)
		}
		return ValidateFullTextTargetURL(request.URL.String())
	},
}

func feedImageProxySecret() string {
	secret := strings.TrimSpace(os.Getenv("FEED_IMAGE_PROXY_SECRET"))
	if len(secret) < feedImageProxyMinimumSecret {
		return ""
	}
	return secret
}

func MaybeProxyFeedImageURL(remoteURL string) string {
	publicURL := strings.TrimSpace(os.Getenv("FEED_IMAGE_PROXY_PUBLIC_URL"))
	secret := feedImageProxySecret()
	if publicURL == "" || secret == "" || remoteURL == "" {
		return remoteURL
	}
	parsed, err := url.Parse(publicURL)
	if err != nil || (parsed.Scheme != "" && parsed.Scheme != "http" && parsed.Scheme != "https") {
		return remoteURL
	}
	if isFeedImageProxyURL(remoteURL, parsed) {
		return remoteURL
	}
	query := parsed.Query()
	query.Set("url", remoteURL)
	query.Set("sig", signFeedImageProxyURL(remoteURL, secret))
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func isFeedImageProxyURL(candidateURL string, proxyURL *url.URL) bool {
	candidate, err := url.Parse(candidateURL)
	if err != nil || candidate.Path != proxyURL.Path {
		return false
	}
	if proxyURL.IsAbs() {
		if candidate.Scheme != proxyURL.Scheme || candidate.Host != proxyURL.Host {
			return false
		}
	} else if candidate.IsAbs() {
		return false
	}
	return candidate.Query().Get("url") != "" && candidate.Query().Get("sig") != ""
}

func FetchFeedImageProxy(remoteURL, signature string) ([]byte, string, error) {
	secret := feedImageProxySecret()
	if strings.TrimSpace(os.Getenv("FEED_IMAGE_PROXY_PUBLIC_URL")) == "" || secret == "" {
		return nil, "", ErrFeedImageProxyDisabled
	}
	remoteURL = strings.TrimSpace(remoteURL)
	expected := signFeedImageProxyURL(remoteURL, secret)
	if expected == "" || !hmac.Equal([]byte(expected), []byte(strings.TrimSpace(signature))) {
		return nil, "", ErrFeedImageProxySignature
	}
	if err := ValidateFullTextTargetURL(remoteURL); err != nil {
		return nil, "", err
	}

	request, err := http.NewRequest(http.MethodGet, remoteURL, nil)
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("Accept", "image/avif,image/webp,image/png,image/jpeg,image/gif;q=0.9,*/*;q=0.1")
	request.Header.Set("User-Agent", "AtomanImageProxy/1.0")
	if parsed, parseErr := url.Parse(remoteURL); parseErr == nil {
		request.Header.Set("Referer", parsed.Scheme+"://"+parsed.Host+"/")
	}

	response, err := feedImageProxyHTTPClient.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, "", fmt.Errorf("%w: status=%d", ErrFeedImageProxyResponse, response.StatusCode)
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if !strings.HasPrefix(contentType, "image/") || contentType == "image/svg+xml" {
		return nil, "", fmt.Errorf("%w: content type", ErrFeedImageProxyResponse)
	}
	body, tooLarge, err := readBoundedFullTextBody(response.Body, feedImageProxyMaxResponseBytes)
	if err != nil {
		return nil, "", err
	}
	if tooLarge {
		return nil, "", fmt.Errorf("%w: response too large", ErrFeedImageProxyResponse)
	}
	return body, contentType, nil
}

func signFeedImageProxyURL(remoteURL, secret string) string {
	if remoteURL == "" || secret == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(remoteURL))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
