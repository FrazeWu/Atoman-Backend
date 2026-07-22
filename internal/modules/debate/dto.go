package debate

import (
	"atoman/internal/model"
	"atoman/internal/modules/comment"
	"atoman/internal/modules/reference"

	"github.com/google/uuid"
)

type ListDebatesQuery struct {
	Status   string
	Search   string
	Tag      string
	Page     int
	PageSize int
}

type DebateDTO struct {
	model.Debate
	References []reference.ResolvedReference `json:"references"`
}

type ArgumentDTO struct {
	model.DebateArgumentDTO
	ContentReferences []reference.ResolvedReference `json:"content_references"`
}

type CreateDebateRequest struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Content     string   `json:"content"`
	Tags        []string `json:"tags"`
}

type CreateArgumentRequest struct {
	DebateID      uuid.UUID              `json:"debate_id"`
	ParentID      *uuid.UUID             `json:"parent_id"`
	Content       string                 `json:"content"`
	ArgumentType  string                 `json:"argument_type"`
	SourceURL     string                 `json:"source_url"`
	SourceTitle   string                 `json:"source_title"`
	SourceExcerpt string                 `json:"source_excerpt"`
	Mentions      []comment.MentionInput `json:"mentions"`
	AttachmentIDs []uuid.UUID            `json:"attachment_ids"`
}

type ReferenceRequest struct {
	ReferenceID uuid.UUID `json:"reference_id"`
}

type DebateReferenceRequest struct {
	DebateID uuid.UUID `json:"debate_id"`
}

type CreateRelationRequest struct {
	SourceDebateID uuid.UUID `json:"source_debate_id"`
	TargetDebateID uuid.UUID `json:"target_debate_id"`
	Stance         string    `json:"stance"`
}

type DebateGraph struct {
	RootID    uuid.UUID              `json:"root_id"`
	Nodes     []model.Debate         `json:"nodes"`
	Relations []model.DebateRelation `json:"relations"`
}
