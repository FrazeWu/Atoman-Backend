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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

type AutoSubscriptionResolveRequest struct {
	Input string `json:"input"`
}

type AutoSubscriptionAddRequest struct {
	Input            string     `json:"input"`
	CandidateFeedURL string     `json:"candidate_feed_url"`
	Title            string     `json:"title"`
	GroupID          *uuid.UUID `json:"group_id"`
}

type AutoSubscriptionCandidate struct {
	Title        string                  `json:"title"`
	FeedURL      string                  `json:"feed_url"`
	SiteURL      string                  `json:"site_url"`
	Kind         string                  `json:"kind"`
	Score        int                     `json:"score"`
	Reason       string                  `json:"reason"`
	IsDefault    bool                    `json:"is_default"`
	Status       string                  `json:"status"`
	Source       *AutoSubscriptionSource `json:"source,omitempty"`
	Subscription *model.Subscription     `json:"subscription,omitempty"`
}

type AutoSubscriptionSource struct {
	ID           *uuid.UUID `json:"id,omitempty"`
	Provider     string     `json:"provider"`
	SourceType   string     `json:"source_type"`
	Title        string     `json:"title"`
	RssURL       string     `json:"rss_url"`
	SiteURL      string     `json:"site_url"`
	CanonicalURL string     `json:"canonical_url"`
}

type AutoSubscriptionResolveResponse struct {
	Status       string                      `json:"status"`
	Source       *AutoSubscriptionSource     `json:"source"`
	Subscription *model.Subscription         `json:"subscription"`
	Candidates   []AutoSubscriptionCandidate `json:"candidates"`
	Message      string                      `json:"message"`
}

type autoSubscriptionTarget struct {
	Provider   string
	SourceType string
	Title      string
	RssURL     string
	SiteURL    string
	Canonical  string
}

type autoSubscriptionHTTPError struct {
	statusCode int
	message    string
}

func (e autoSubscriptionHTTPError) Error() string {
	return e.message
}

func newAutoSubscriptionHTTPError(statusCode int, message string) error {
	return autoSubscriptionHTTPError{
		statusCode: statusCode,
		message:    message,
	}
}

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
	if directURL, err := url.Parse(canonicalInput); err == nil && isLikelyDirectFeedURL(directURL) {
		target := autoSubscriptionTargetFromDirectFeedURL(canonicalInput, canonicalInput)
		response, err := classifyAutoSubscriptionTarget(db, userID, target)
		if err != nil {
			return newAutoSubscriptionResolveResponse("error"), http.StatusInternalServerError
		}
		return response, http.StatusOK
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

// AutoAddSubscription godoc
// @Summary 自动添加订阅
// @Description 根据原始输入或用户选择的候选 feed URL 创建或复用来源并添加当前用户订阅。
// @Tags feed
// @Accept json
// @Produce json
// @Param input body AutoSubscriptionAddRequest true "自动添加订阅输入"
// @Success 201 {object} SubscriptionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/auto-add [post]
func AutoAddSubscription(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input AutoSubscriptionAddRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid auto-add request body"})
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

		target, err := autoSubscriptionTargetForAdd(db, userID, input)
		if err != nil {
			writeAutoSubscriptionError(c, err)
			return
		}

		subscription, err := createAutoSubscription(db, userID, target, input.Title, input.GroupID)
		if err != nil {
			writeAutoSubscriptionError(c, err)
			return
		}

		if target.SourceType == "external_rss" && subscription.FeedSource != nil {
			syncFeedSource(db, *subscription.FeedSource)
		}

		c.JSON(http.StatusCreated, gin.H{"data": subscription, "message": "ok"})
	}
}

func autoSubscriptionTargetForAdd(db *gorm.DB, userID uuid.UUID, input AutoSubscriptionAddRequest) (autoSubscriptionTarget, error) {
	if candidateFeedURL := strings.TrimSpace(input.CandidateFeedURL); candidateFeedURL != "" {
		u, err := parseAutoSubscriptionURL(candidateFeedURL)
		if err != nil {
			return autoSubscriptionTarget{}, newAutoSubscriptionHTTPError(http.StatusBadRequest, "candidate_feed_url must be an absolute http/https URL")
		}
		rssURL := normalizeCanonicalFeedURL(u.String())
		return autoSubscriptionTarget{
			Provider:   "rss",
			SourceType: "external_rss",
			Title:      firstNonBlank(input.Title, rssURL),
			RssURL:     rssURL,
			SiteURL:    validAutoSubscriptionSiteURL(input.Input),
			Canonical:  normalizeCanonicalFeedURL(rssURL),
		}, nil
	}

	u, err := parseAutoSubscriptionURL(input.Input)
	if err != nil {
		return autoSubscriptionTarget{}, newAutoSubscriptionHTTPError(http.StatusBadRequest, "input must be an absolute http/https URL")
	}

	if target, ok := githubRepositoryTarget(u); ok {
		target.Title = firstNonBlank(input.Title, target.Title)
		return target, nil
	}
	if malformedGithubRepositoryPath(u) {
		return autoSubscriptionTarget{}, newAutoSubscriptionHTTPError(http.StatusBadRequest, "input must be an absolute http/https URL")
	}

	canonicalInput := normalizeCanonicalFeedURL(u.String())
	if directURL, err := url.Parse(canonicalInput); err == nil && isLikelyDirectFeedURL(directURL) {
		return autoSubscriptionTargetFromDirectFeedURL(canonicalInput, firstNonBlank(input.Title, canonicalInput)), nil
	}
	if ok, err := probeAutoSubscriptionDirectFeedURL(u); err != nil {
		return autoSubscriptionTarget{}, err
	} else if ok {
		return autoSubscriptionTargetFromDirectFeedURL(canonicalInput, firstNonBlank(input.Title, canonicalInput)), nil
	}

	response, _, err := resolveDiscoveredSubscriptionInput(db, userID, u)
	if err != nil {
		return autoSubscriptionTarget{}, err
	}
	switch response.Status {
	case "multiple_candidates":
		return autoSubscriptionTarget{}, newAutoSubscriptionHTTPError(http.StatusBadRequest, "candidate_feed_url is required when multiple feed candidates are available")
	case "invalid":
		return autoSubscriptionTarget{}, newAutoSubscriptionHTTPError(http.StatusBadRequest, "input must be an absolute http/https URL")
	case "not_found":
		return autoSubscriptionTarget{}, newAutoSubscriptionHTTPError(http.StatusBadRequest, "no feed candidates found for input")
	case "error":
		return autoSubscriptionTarget{}, newAutoSubscriptionHTTPError(http.StatusInternalServerError, "failed to resolve subscription input")
	}

	if len(response.Candidates) != 1 {
		return autoSubscriptionTarget{}, newAutoSubscriptionHTTPError(http.StatusBadRequest, "no feed candidates found for input")
	}

	candidate := response.Candidates[0]
	if candidate.Source != nil {
		return autoSubscriptionTargetFromSource(*candidate.Source, input.Title), nil
	}

	feedURL := normalizeCanonicalFeedURL(candidate.FeedURL)
	if feedURL == "" {
		return autoSubscriptionTarget{}, newAutoSubscriptionHTTPError(http.StatusBadRequest, "no feed candidates found for input")
	}
	return autoSubscriptionTarget{
		Provider:   "rss",
		SourceType: "external_rss",
		Title:      firstNonBlank(input.Title, candidate.Title, feedURL),
		RssURL:     feedURL,
		SiteURL:    candidate.SiteURL,
		Canonical:  normalizeCanonicalFeedURL(feedURL),
	}, nil
}

func createAutoSubscription(db *gorm.DB, userID uuid.UUID, target autoSubscriptionTarget, title string, groupID *uuid.UUID) (model.Subscription, error) {
	var subscription model.Subscription
	source, err := findOrCreateAutoAddFeedSource(db, target, firstNonBlank(title, target.Title, target.RssURL))
	if err != nil {
		return subscription, err
	}

	err = db.Transaction(func(tx *gorm.DB) error {
		group, err := autoSubscriptionGroup(tx, userID, groupID)
		if err != nil {
			return err
		}

		var existing model.Subscription
		lookup := tx.Where("user_id = ? AND feed_source_id = ?", userID, source.ID).Limit(1).Find(&existing)
		if lookup.Error != nil {
			return lookup.Error
		}
		if lookup.RowsAffected > 0 {
			return newAutoSubscriptionHTTPError(http.StatusConflict, "Already subscribed to this source")
		}

		subscription = model.Subscription{
			UserID:              userID,
			FeedSourceID:        source.ID,
			Title:               firstNonBlank(title, target.Title, source.Title),
			SubscriptionGroupID: &group.ID,
		}
		if err := tx.Create(&subscription).Error; err != nil {
			if isAutoSubscriptionDuplicateSubscriptionError(err) {
				return newAutoSubscriptionHTTPError(http.StatusConflict, "Already subscribed to this source")
			}
			return err
		}
		return tx.Preload("FeedSource").Preload("SubscriptionGroup").First(&subscription, "id = ?", subscription.ID).Error
	})
	return subscription, err
}

func findOrCreateAutoAddFeedSource(db *gorm.DB, target autoSubscriptionTarget, fallbackTitle string) (*model.FeedSource, error) {
	if target.SourceType != "external_rss" {
		return findOrCreateFeedSource(db, target.SourceType, nil, target.RssURL, fallbackTitle, target.Provider)
	}

	provider := firstNonBlank(target.Provider, "rss")
	rssURL := strings.TrimSpace(target.RssURL)
	canonicalURL := normalizeCanonicalFeedURL(firstNonBlank(target.Canonical, rssURL))
	sourceHash := buildFeedSourceHash(target.SourceType, nil, rssURL)

	source, found, err := findReusableAutoAddFeedSource(db, target.SourceType, rssURL, canonicalURL, sourceHash)
	if err != nil {
		return nil, err
	}
	if found {
		if err := updateAutoAddFeedSource(db, source, target, provider, rssURL, canonicalURL, sourceHash, fallbackTitle); err != nil {
			return nil, err
		}
		return source, nil
	}

	source = &model.FeedSource{
		SourceType:      target.SourceType,
		Provider:        provider,
		RssURL:          rssURL,
		CanonicalURL:    canonicalURL,
		SiteURL:         firstNonBlank(target.SiteURL),
		Hash:            sourceHash,
		Title:           firstNonBlank(fallbackTitle, target.Title, rssURL),
		HealthStatus:    "healthy",
		FullTextEnabled: service.DefaultFullTextEnabled(target.SourceType),
	}
	if err := db.Create(source).Error; err != nil {
		if existing, found, lookupErr := findReusableAutoAddFeedSource(db, target.SourceType, rssURL, canonicalURL, sourceHash); lookupErr != nil {
			return nil, lookupErr
		} else if found {
			if updateErr := updateAutoAddFeedSource(db, existing, target, provider, rssURL, canonicalURL, sourceHash, fallbackTitle); updateErr != nil {
				return nil, updateErr
			}
			return existing, nil
		}
		return nil, err
	}
	return source, nil
}

func findReusableAutoAddFeedSource(db *gorm.DB, sourceType, rssURL, canonicalURL, sourceHash string) (*model.FeedSource, bool, error) {
	if canonicalURL != "" {
		var source model.FeedSource
		lookup := db.Where("canonical_url = ?", canonicalURL).Order("created_at ASC").Limit(1).Find(&source)
		if lookup.Error != nil {
			return nil, false, lookup.Error
		}
		if lookup.RowsAffected > 0 {
			return &source, true, nil
		}

		legacyURLs := uniqueNonBlankStrings(rssURL, canonicalURL, canonicalURL+"/")
		if len(legacyURLs) > 0 {
			lookup = db.Where("source_type = ? AND (canonical_url = '' OR canonical_url IS NULL) AND rss_url IN ?", sourceType, legacyURLs).
				Order("created_at ASC").
				Limit(1).
				Find(&source)
			if lookup.Error != nil {
				return nil, false, lookup.Error
			}
			if lookup.RowsAffected > 0 {
				return &source, true, nil
			}
		}
	}

	if sourceHash == "" {
		return nil, false, nil
	}
	var source model.FeedSource
	lookup := db.Where("hash = ?", sourceHash).Order("created_at ASC").Limit(1).Find(&source)
	if lookup.Error != nil {
		return nil, false, lookup.Error
	}
	if lookup.RowsAffected > 0 {
		return &source, true, nil
	}
	return nil, false, nil
}

func updateAutoAddFeedSource(db *gorm.DB, source *model.FeedSource, target autoSubscriptionTarget, provider, rssURL, canonicalURL, sourceHash, fallbackTitle string) error {
	updates := map[string]any{}
	if strings.TrimSpace(source.SourceType) == "" {
		updates["source_type"] = target.SourceType
		source.SourceType = target.SourceType
	}
	if strings.TrimSpace(source.Provider) == "" || (provider == "rsshub" && strings.TrimSpace(source.Provider) == "rss") {
		updates["provider"] = provider
		source.Provider = provider
	}
	if strings.TrimSpace(source.CanonicalURL) == "" && canonicalURL != "" {
		updates["canonical_url"] = canonicalURL
		source.CanonicalURL = canonicalURL
	}
	if strings.TrimSpace(source.RssURL) == "" && rssURL != "" {
		updates["rss_url"] = rssURL
		source.RssURL = rssURL
	}
	if strings.TrimSpace(source.Hash) == "" && sourceHash != "" {
		updates["hash"] = sourceHash
		source.Hash = sourceHash
	}
	if strings.TrimSpace(source.SiteURL) == "" && strings.TrimSpace(target.SiteURL) != "" {
		updates["site_url"] = target.SiteURL
		source.SiteURL = target.SiteURL
	}
	if strings.TrimSpace(source.Title) == "" {
		title := firstNonBlank(fallbackTitle, target.Title, rssURL)
		if title != "" {
			updates["title"] = title
			source.Title = title
		}
	}
	if len(updates) == 0 {
		return nil
	}
	if err := db.Model(source).Updates(updates).Error; err != nil {
		return err
	}
	return db.Where("id = ?", source.ID).First(source).Error
}

func uniqueNonBlankStrings(values ...string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func isAutoSubscriptionDuplicateSubscriptionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}

	var sqliteErr interface {
		error
		Code() int
	}
	if errors.As(err, &sqliteErr) && sqliteErr.Code() == 2067 {
		return strings.Contains(strings.ToLower(sqliteErr.Error()), "subscriptions.user_id") &&
			strings.Contains(strings.ToLower(sqliteErr.Error()), "subscriptions.feed_source_id")
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		constraint := strings.ToLower(pgErr.ConstraintName)
		detail := strings.ToLower(pgErr.Detail)
		return constraint == "idx_subscriptions_user_source" ||
			(strings.Contains(detail, "user_id") && strings.Contains(detail, "feed_source_id"))
	}

	var pqErr *pq.Error
	if errors.As(err, &pqErr) && string(pqErr.Code) == "23505" {
		constraint := strings.ToLower(pqErr.Constraint)
		detail := strings.ToLower(pqErr.Detail)
		return constraint == "idx_subscriptions_user_source" ||
			(strings.Contains(detail, "user_id") && strings.Contains(detail, "feed_source_id"))
	}

	message := strings.ToLower(err.Error())
	return strings.Contains(message, "idx_subscriptions_user_source") ||
		(strings.Contains(message, "unique constraint failed") &&
			strings.Contains(message, "subscriptions.user_id") &&
			strings.Contains(message, "subscriptions.feed_source_id")) ||
		(strings.Contains(message, "duplicate key") &&
			strings.Contains(message, "user_id") &&
			strings.Contains(message, "feed_source_id"))
}

func autoSubscriptionGroup(tx *gorm.DB, userID uuid.UUID, groupID *uuid.UUID) (*model.SubscriptionGroup, error) {
	if groupID == nil {
		return getOrCreateDefaultSubscriptionGroup(tx, userID)
	}

	var group model.SubscriptionGroup
	lookup := tx.Where("id = ? AND user_id = ?", *groupID, userID).Limit(1).Find(&group)
	if lookup.Error != nil {
		return nil, lookup.Error
	}
	if lookup.RowsAffected == 0 {
		return nil, newAutoSubscriptionHTTPError(http.StatusBadRequest, "Subscription group not found")
	}
	return &group, nil
}

func autoSubscriptionTargetFromSource(source AutoSubscriptionSource, title string) autoSubscriptionTarget {
	rssURL := normalizeCanonicalFeedURL(source.RssURL)
	return autoSubscriptionTarget{
		Provider:   firstNonBlank(source.Provider, "rss"),
		SourceType: firstNonBlank(source.SourceType, "external_rss"),
		Title:      firstNonBlank(title, source.Title, rssURL),
		RssURL:     rssURL,
		SiteURL:    source.SiteURL,
		Canonical:  normalizeCanonicalFeedURL(firstNonBlank(source.CanonicalURL, rssURL)),
	}
}

func validAutoSubscriptionSiteURL(rawInput string) string {
	siteURL := strings.TrimSpace(rawInput)
	if siteURL == "" {
		return ""
	}
	if _, err := parseAutoSubscriptionURL(siteURL); err != nil {
		return ""
	}
	return siteURL
}

func writeAutoSubscriptionError(c *gin.Context, err error) {
	var httpErr autoSubscriptionHTTPError
	if errors.As(err, &httpErr) {
		c.JSON(httpErr.statusCode, gin.H{"error": httpErr.message})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to auto-add subscription"})
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
