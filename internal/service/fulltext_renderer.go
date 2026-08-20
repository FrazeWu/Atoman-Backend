package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const fullTextRendererMaxResponseBytes = 5 * 1024 * 1024

var fullTextRendererHTTPClient = &http.Client{Timeout: 20 * time.Second}

type fullTextRendererRequest struct {
	URL string `json:"url"`
}

type fullTextRendererResponse struct {
	HTML string `json:"html"`
}

func fetchRenderedFullText(targetURL string) ([]byte, bool, error) {
	endpoint := strings.TrimSpace(os.Getenv("FULLTEXT_RENDERER_URL"))
	if endpoint == "" {
		return nil, false, nil
	}
	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil || !parsedEndpoint.IsAbs() || (parsedEndpoint.Scheme != "http" && parsedEndpoint.Scheme != "https") {
		return nil, true, errors.New("invalid full text renderer endpoint")
	}
	if err := ValidateFullTextTargetURL(targetURL); err != nil {
		return nil, true, err
	}

	payload, err := json.Marshal(fullTextRendererRequest{URL: targetURL})
	if err != nil {
		return nil, true, err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, true, err
	}
	req.Header.Set("Accept", "application/json, text/html")
	req.Header.Set("Content-Type", "application/json")
	if token := strings.TrimSpace(os.Getenv("FULLTEXT_RENDERER_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	response, err := fullTextRendererHTTPClient.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
		return nil, true, fmt.Errorf("renderer status=%d", response.StatusCode)
	}
	body, tooLarge, err := readBoundedFullTextBody(response.Body, fullTextRendererMaxResponseBytes)
	if err != nil {
		return nil, true, err
	}
	if tooLarge {
		return nil, true, errors.New("renderer response too large")
	}

	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "application/json") {
		var decoded fullTextRendererResponse
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, true, err
		}
		body = []byte(decoded.HTML)
	}
	if strings.TrimSpace(string(body)) == "" {
		return nil, true, errors.New("renderer returned empty html")
	}
	return body, true, nil
}

func readBoundedFullTextBody(body io.Reader, maxBytes int64) ([]byte, bool, error) {
	limited := io.LimitReader(body, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > maxBytes {
		return nil, true, nil
	}
	return data, false, nil
}
