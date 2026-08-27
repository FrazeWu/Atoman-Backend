package feed

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"atoman/internal/model"
	"atoman/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ResolveSubscriptionInput godoc
// @Summary 自动检测订阅来源
// @Description 检测输入 URL 是否对应已订阅来源、已有来源、新来源或多个候选，不创建订阅。
// @Tags feed
// @Accept json
// @Produce json
// @Param input body AutoSubscriptionResolveRequest true "订阅来源输入"
// @Success 200 {object} AutoSubscriptionResolveResponse
// @Failure 400 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/resolve [post]
func ResolveSubscriptionInput(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input AutoSubscriptionResolveRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid resolve request body"})
			return
		}

		userIDVal, ok := c.Get("user_id")
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Login required"})
			return
		}
		userID, ok := userIDVal.(uuid.UUID)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Login required"})
			return
		}

		response, statusCode := resolveSubscriptionInputForUser(db, userID, input.Input)
		c.JSON(statusCode, response)
	}
}

func resolveSubscriptionInputForUser(db *gorm.DB, userID uuid.UUID, rawInput string) (AutoSubscriptionResolveResponse, int) {
	u, err := parseAutoSubscriptionURL(rawInput)
	if err != nil {
		return newAutoSubscriptionResolveResponse("invalid"), http.StatusOK
	}

	if target, ok := githubRepositoryTarget(u); ok {
		response, err := classifyAutoSubscriptionTarget(db, userID, target)
		if err != nil {
			return newAutoSubscriptionResolveResponse("error"), http.StatusInternalServerError
		}
		return response, http.StatusOK
	}

	canonicalInput := normalizeCanonicalFeedURL(u.String())
	if db != nil {
		directTarget := autoSubscriptionTargetFromDirectFeedURL(canonicalInput, canonicalInput)
		if _, found, err := findExistingAutoSubscriptionSource(db, directTarget); err != nil {
			return newAutoSubscriptionResolveResponse("error"), http.StatusInternalServerError
		} else if found {
			response, err := classifyAutoSubscriptionTarget(db, userID, directTarget)
			if err != nil {
				return newAutoSubscriptionResolveResponse("error"), http.StatusInternalServerError
			}
			return response, http.StatusOK
		}
	}
	if ok, err := probeAutoSubscriptionDirectFeedURL(u); err != nil {
		return newAutoSubscriptionResolveResponse("error"), http.StatusInternalServerError
	} else if ok {
		target := autoSubscriptionTargetFromDirectFeedURL(canonicalInput, canonicalInput)
		response, err := classifyAutoSubscriptionTarget(db, userID, target)
		if err != nil {
			return newAutoSubscriptionResolveResponse("error"), http.StatusInternalServerError
		}
		return response, http.StatusOK
	}

	response, statusCode, err := resolveDiscoveredSubscriptionInput(db, userID, u)
	if err != nil {
		return newAutoSubscriptionResolveResponse("error"), statusCode
	}
	return response, statusCode
}

func parseAutoSubscriptionURL(rawInput string) (*url.URL, error) {
	u, err := url.ParseRequestURI(strings.TrimSpace(rawInput))
	if err != nil || u == nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || strings.TrimSpace(u.Hostname()) == "" {
		return nil, errors.New("url must be an absolute http/https URL")
	}
	return u, nil
}

func githubRepositoryTarget(u *url.URL) (autoSubscriptionTarget, bool) {
	if u == nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "github.com") {
		return autoSubscriptionTarget{}, false
	}

	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return autoSubscriptionTarget{}, false
	}
	owner, err := url.PathUnescape(parts[0])
	if err != nil || !validGithubPathSegment(owner) {
		return autoSubscriptionTarget{}, false
	}
	repo, err := url.PathUnescape(parts[1])
	if err != nil || !validGithubPathSegment(repo) {
		return autoSubscriptionTarget{}, false
	}
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)

	feedURL, err := service.BuildRSSHubFeedURL("github/repo", map[string]string{
		"owner": owner,
		"repo":  repo,
	})
	if err != nil {
		return autoSubscriptionTarget{}, false
	}

	siteURL := "https://github.com/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
	return autoSubscriptionTarget{
		Provider:   "rsshub",
		SourceType: "external_rss",
		Title:      owner + "/" + repo,
		RssURL:     feedURL,
		SiteURL:    siteURL,
		Canonical:  normalizeCanonicalFeedURL(feedURL),
	}, true
}

func validGithubPathSegment(segment string) bool {
	trimmed := strings.TrimSpace(segment)
	if trimmed == "" || strings.ContainsAny(trimmed, `/\?#`) {
		return false
	}
	for _, r := range trimmed {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

func malformedGithubRepositoryPath(u *url.URL) bool {
	if u == nil || !strings.EqualFold(u.Hostname(), "github.com") {
		return false
	}

	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		decoded, err := url.PathUnescape(part)
		if err != nil || !validGithubPathSegment(decoded) {
			return true
		}
	}
	return false
}

func classifyAutoSubscriptionTarget(db *gorm.DB, userID uuid.UUID, target autoSubscriptionTarget) (AutoSubscriptionResolveResponse, error) {
	source, found, err := findExistingAutoSubscriptionSource(db, target)
	if err != nil {
		return AutoSubscriptionResolveResponse{}, err
	}
	if !found {
		status := "new_source"
		response := newAutoSubscriptionResolveResponse(status)
		response.Source = sourceDTOFromTarget(target)
		return response, nil
	}

	subscription, subscribed, err := findUserSubscriptionForSource(db, userID, source.ID)
	if err != nil {
		return AutoSubscriptionResolveResponse{}, err
	}
	if subscribed {
		status := "already_subscribed"
		response := newAutoSubscriptionResolveResponse(status)
		response.Source = sourceDTOFromModel(source)
		response.Subscription = &subscription
		return response, nil
	}

	status := "existing_source"
	response := newAutoSubscriptionResolveResponse(status)
	response.Source = sourceDTOFromModel(source)
	return response, nil
}

func findExistingAutoSubscriptionSource(db *gorm.DB, target autoSubscriptionTarget) (model.FeedSource, bool, error) {
	canonical := normalizeCanonicalFeedURL(target.Canonical)
	if canonical != "" {
		var source model.FeedSource
		lookup := db.Where("canonical_url = ?", canonical).Order("created_at ASC").Limit(1).Find(&source)
		if lookup.Error != nil {
			return model.FeedSource{}, false, lookup.Error
		}
		if lookup.RowsAffected > 0 {
			return source, true, nil
		}
	}

	sourceHash := buildFeedSourceHash(target.SourceType, nil, target.RssURL)
	if sourceHash == "" {
		return model.FeedSource{}, false, nil
	}

	var source model.FeedSource
	lookup := db.Where("hash = ?", sourceHash).Order("created_at ASC").Limit(1).Find(&source)
	if lookup.Error != nil {
		return model.FeedSource{}, false, lookup.Error
	}
	if lookup.RowsAffected > 0 {
		return source, true, nil
	}
	return model.FeedSource{}, false, nil
}

func findUserSubscriptionForSource(db *gorm.DB, userID uuid.UUID, sourceID uuid.UUID) (model.Subscription, bool, error) {
	var subscription model.Subscription
	lookup := db.Where("user_id = ? AND feed_source_id = ?", userID, sourceID).Limit(1).Find(&subscription)
	if lookup.Error != nil {
		return model.Subscription{}, false, lookup.Error
	}
	if lookup.RowsAffected > 0 {
		return subscription, true, nil
	}
	return model.Subscription{}, false, nil
}

func sourceDTOFromTarget(target autoSubscriptionTarget) *AutoSubscriptionSource {
	return &AutoSubscriptionSource{
		Provider:     target.Provider,
		SourceType:   target.SourceType,
		Category:     defaultFeedSourceCategory(target.Category),
		Title:        target.Title,
		RssURL:       target.RssURL,
		SiteURL:      target.SiteURL,
		CanonicalURL: normalizeCanonicalFeedURL(target.Canonical),
	}
}

func sourceDTOFromModel(source model.FeedSource) *AutoSubscriptionSource {
	return &AutoSubscriptionSource{
		ID:           &source.ID,
		Provider:     source.Provider,
		SourceType:   source.SourceType,
		Category:     defaultFeedSourceCategory(source.Category),
		Title:        source.Title,
		RssURL:       source.RssURL,
		SiteURL:      source.SiteURL,
		CanonicalURL: source.CanonicalURL,
	}
}

func autoSubscriptionTargetFromDirectFeedURL(rssURL string, title string) autoSubscriptionTarget {
	canonical := normalizeCanonicalFeedURL(rssURL)
	return autoSubscriptionTarget{
		Provider:   "rss",
		SourceType: "external_rss",
		Title:      firstNonBlank(title, canonical),
		RssURL:     canonical,
		SiteURL:    canonical,
		Canonical:  canonical,
	}
}

func probeAutoSubscriptionDirectFeedURL(u *url.URL) (bool, error) {
	if err := validateFeedDiscoveryFetchURL(u); err != nil {
		return false, nil
	}

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/rss+xml,application/atom+xml,application/xml,text/xml;q=0.9,*/*;q=0.1")
	req.Header.Set("User-Agent", "AtomanFeedDiscoveryBot/1.0")

	resp, err := feedDiscoveryHTTPClient.Do(req)
	if err != nil {
		return false, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return false, nil
	}
	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if isAutoSubscriptionFeedContentType(contentType) {
		return true, nil
	}
	if contentType != "" && strings.Contains(contentType, "text/html") {
		return false, nil
	}

	limited := io.LimitReader(resp.Body, 4096)
	data, err := io.ReadAll(limited)
	if err != nil {
		return false, err
	}
	return looksLikeAutoSubscriptionFeedDocument(string(data)), nil
}

func isAutoSubscriptionFeedContentType(contentType string) bool {
	return strings.Contains(contentType, "application/rss+xml") ||
		strings.Contains(contentType, "application/atom+xml") ||
		strings.Contains(contentType, "application/feed+json") ||
		strings.Contains(contentType, "application/rdf+xml") ||
		strings.Contains(contentType, "text/xml") ||
		strings.Contains(contentType, "application/xml")
}

func looksLikeAutoSubscriptionFeedDocument(body string) bool {
	trimmed := strings.TrimSpace(body)
	if strings.HasPrefix(trimmed, "\ufeff") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "\ufeff"))
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "<?xml") {
		if end := strings.Index(lower, "?>"); end >= 0 {
			lower = strings.TrimSpace(lower[end+2:])
		}
	}
	return strings.HasPrefix(lower, "<rss") ||
		strings.HasPrefix(lower, "<feed") ||
		strings.HasPrefix(lower, "<rdf:rdf")
}

func resolveDiscoveredSubscriptionInput(db *gorm.DB, userID uuid.UUID, u *url.URL) (AutoSubscriptionResolveResponse, int, error) {
	if err := validateFeedDiscoveryFetchURL(u); err != nil {
		return newAutoSubscriptionResolveResponse("invalid"), http.StatusOK, nil
	}

	rawURL := u.String()
	html, err := fetchFeedDiscoveryHTML(rawURL)
	if err != nil {
		return newAutoSubscriptionResolveResponse("not_found"), http.StatusOK, nil
	}

	discovered := service.ExtractFeedCandidatesFromHTML(rawURL, html)
	if len(discovered) == 0 {
		return newAutoSubscriptionResolveResponse("not_found"), http.StatusOK, nil
	}

	candidates := make([]AutoSubscriptionCandidate, 0, len(discovered))
	for _, discoveredCandidate := range discovered {
		target := autoSubscriptionTarget{
			Provider:   "rss",
			SourceType: "external_rss",
			Title:      discoveredCandidate.Title,
			RssURL:     normalizeCanonicalFeedURL(discoveredCandidate.FeedURL),
			SiteURL:    discoveredCandidate.SiteURL,
			Canonical:  normalizeCanonicalFeedURL(discoveredCandidate.FeedURL),
		}
		classified, err := classifyAutoSubscriptionTarget(db, userID, target)
		if err != nil {
			return AutoSubscriptionResolveResponse{}, http.StatusInternalServerError, err
		}
		candidates = append(candidates, AutoSubscriptionCandidate{
			Title:        discoveredCandidate.Title,
			FeedURL:      discoveredCandidate.FeedURL,
			SiteURL:      discoveredCandidate.SiteURL,
			Kind:         discoveredCandidate.Kind,
			Score:        discoveredCandidate.Score,
			Reason:       discoveredCandidate.Reason,
			IsDefault:    discoveredCandidate.IsDefault,
			Status:       classified.Status,
			Source:       classified.Source,
			Subscription: classified.Subscription,
		})
	}

	if len(candidates) == 1 {
		status := candidates[0].Status
		response := newAutoSubscriptionResolveResponse(status)
		response.Source = candidates[0].Source
		response.Subscription = candidates[0].Subscription
		response.Candidates = candidates
		return response, http.StatusOK, nil
	}

	status := "multiple_candidates"
	response := newAutoSubscriptionResolveResponse(status)
	response.Candidates = candidates
	return response, http.StatusOK, nil
}

func newAutoSubscriptionResolveResponse(status string) AutoSubscriptionResolveResponse {
	return AutoSubscriptionResolveResponse{
		Status:     status,
		Candidates: []AutoSubscriptionCandidate{},
		Message:    messageForAutoSubscriptionStatus(status),
	}
}

func messageForAutoSubscriptionStatus(status string) string {
	switch status {
	case "already_subscribed":
		return "你已订阅该来源"
	case "existing_source":
		return "该来源已存在，可直接订阅"
	case "new_source":
		return "可添加为新的订阅源"
	case "multiple_candidates":
		return "发现多个可订阅来源"
	case "not_found":
		return "未找到可订阅来源"
	case "invalid":
		return "请输入有效的订阅链接"
	case "error":
		return "解析订阅源失败"
	default:
		return "订阅链接已解析"
	}
}
