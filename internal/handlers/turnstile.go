package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const turnstileSiteverifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

type turnstileVerifyRequest struct {
	Secret   string `json:"secret"`
	Response string `json:"response"`
	RemoteIP string `json:"remoteip,omitempty"`
}

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

	payload, err := json.Marshal(turnstileVerifyRequest{
		Secret:   secret,
		Response: token,
		RemoteIP: remoteIP,
	})
	if err != nil {
		return fmt.Errorf("Failed to prepare Turnstile verification")
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(turnstileSiteverifyURL, "application/json", bytes.NewReader(payload))
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
