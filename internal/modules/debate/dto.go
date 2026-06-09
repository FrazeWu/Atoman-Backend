package debate

import "github.com/google/uuid"

type CreateDebateRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

type CreateArgumentRequest struct {
	DebateID     uuid.UUID `json:"debate_id"`
	Content      string    `json:"content"`
	ArgumentType string    `json:"argument_type"`
}
