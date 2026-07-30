package feed

import (
	"strings"

	"atoman/internal/model"

	"github.com/google/uuid"
)

type AutoSubscriptionResolveRequest struct {
	Input string `json:"input"`
}

type AutoSubscriptionAddRequest struct {
	Input            string     `json:"input"`
	CandidateFeedURL string     `json:"candidate_feed_url"`
	Title            string     `json:"title"`
	GroupID          *uuid.UUID `json:"group_id"`
	Category         string     `json:"category"`
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
	Category     string     `json:"category"`
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
	Category   string
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

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
