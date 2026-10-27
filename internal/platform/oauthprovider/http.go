package oauthprovider

import (
	"net/http"
	"time"
)

const oauthHTTPTimeout = 10 * time.Second

// Bound provider and JWKS requests even when the caller has no deadline.
func newOAuthHTTPClient() *http.Client {
	return &http.Client{Timeout: oauthHTTPTimeout}
}
