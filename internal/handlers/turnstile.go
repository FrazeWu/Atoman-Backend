package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const turnstileSiteverifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

type turnstileVerifyResponse struct {
	Success    bool     `json:"success"`
	ErrorCodes []string `json:"error-codes"`
}

func isProductionEnv() bool {
	return os.Getenv("ENV") == "production" || os.Getenv("GIN_MODE") == "release"
}

func verifyTurnstileToken(token string, remoteIP string) error {
	secret := os.Getenv("TURNSTILE_SECRET_KEY")
	if secret == "" {
		if isProductionEnv() {
			return fmt.Errorf("Turnstile is not configured")
		}
		return nil
	}

	if token == "" {
		return fmt.Errorf("Turnstile verification is required")
	}

	form := url.Values{}
	form.Set("secret", secret)
	form.Set("response", token)
	if strings.TrimSpace(remoteIP) != "" {
		form.Set("remoteip", remoteIP)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(turnstileSiteverifyURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("Failed to verify Turnstile token")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("Failed to read Turnstile verification response")
	}

	var result turnstileVerifyResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("Invalid Turnstile verification response")
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices || !result.Success {
		return fmt.Errorf("Turnstile verification failed")
	}

	return nil
}
