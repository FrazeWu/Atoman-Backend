package service

import (
	"fmt"
	"net/url"
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
	case "youtube/channel", "youtube/user", "youtube/playlist", "bilibili/user/video":
		id := strings.TrimSpace(params["id"])
		if id == "" {
			id = strings.TrimSpace(params["username"])
		}
		if id == "" {
			id = strings.TrimSpace(params["list"])
		}
		if id == "" {
			id = strings.TrimSpace(params["uid"])
		}
		if id == "" {
			return "", fmt.Errorf("id is required")
		}
		return fmt.Sprintf("%s/%s/%s", defaultRSSHubBaseURL, strings.Trim(templateKey, "/"), url.PathEscape(id)), nil
	default:
		return "", fmt.Errorf("unsupported template_key")
	}
}
