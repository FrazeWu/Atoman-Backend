package debate

import "github.com/google/uuid"

type ListDebatesQuery struct {
	Status   string
	Search   string
	Page     int
	PageSize int
}

type CreateDebateRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Content     string `json:"content"`
	Tags        []string `json:"tags"`
}

type CreateArgumentRequest struct {
	DebateID      uuid.UUID  `json:"debate_id"`
	ParentID      *uuid.UUID `json:"parent_id"`
	Content       string     `json:"content"`
	ArgumentType  string     `json:"argument_type"`
	SourceURL     string     `json:"source_url"`
	SourceTitle   string     `json:"source_title"`
	SourceExcerpt string     `json:"source_excerpt"`
}

type ReferenceRequest struct {
	ReferenceID uuid.UUID `json:"reference_id"`
}

type DebateReferenceRequest struct {
	DebateID uuid.UUID `json:"debate_id"`
}
