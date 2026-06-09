package service

import (
	"fmt"
	"strings"
)

const defaultRSSHubBaseURL = "https://rsshub.app"

func BuildRSSHubFeedURL(templateKey string, params map[string]string) (string, error) {
	switch strings.TrimSpace(templateKey) {
	case "github/repo":
		owner := strings.TrimSpace(params["owner"])
		repo := strings.TrimSpace(params["repo"])
		if owner == "" || repo == "" {
			return "", fmt.Errorf("owner and repo are required")
		}
		return fmt.Sprintf("%s/github/repo/%s/%s", defaultRSSHubBaseURL, owner, repo), nil
	default:
		return "", fmt.Errorf("unsupported template_key")
	}
}
