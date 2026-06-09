package subscription

import "github.com/google/uuid"

type CreateSubscriptionRequest struct {
	TargetType string     `json:"target_type"`
	TargetID   *uuid.UUID `json:"target_id"`
	RSSURL     string     `json:"rss_url"`
	Title      string     `json:"title"`
}

type CreateSubscriptionGroupRequest struct {
	Name string `json:"name"`
}
