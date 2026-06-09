package music

import "github.com/google/uuid"

type Source struct {
	Type  string `json:"type"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

type SubmitEditRequest struct {
	Type       string         `json:"type"`
	EntityType string         `json:"entity_type"`
	EntityID   *uuid.UUID     `json:"entity_id"`
	Payload    map[string]any `json:"payload"`
	Changes    map[string]any `json:"changes"`
	Reason     string         `json:"reason"`
	Sources    []Source       `json:"sources"`
}

type VoteRequest struct {
	Vote    string `json:"vote"`
	Comment string `json:"comment"`
}

type DecisionRequest struct {
	Reason string `json:"reason"`
}
