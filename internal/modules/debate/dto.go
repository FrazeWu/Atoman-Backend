package debate

import (
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
)

type ListDebatesQuery struct {
	Status   string
	Search   string
	Tag      string
	Page     int
	PageSize int
}

type CreateDebateRequest struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Content     string   `json:"content"`
	Tags        []string `json:"tags"`
}

type SaveWikiRequest struct {
	Title          string    `json:"title"`
	Description    string    `json:"description"`
	Content        string    `json:"content"`
	Tags           []string  `json:"tags"`
	EditSummary    string    `json:"edit_summary"`
	BaseRevisionID uuid.UUID `json:"base_revision"`
}

type RevertRevisionRequest struct {
	BaseRevisionID uuid.UUID `json:"base_revision"`
	EditSummary    string    `json:"edit_summary"`
}

type ReconfirmReferenceRequest struct {
	BaseRevisionID uuid.UUID `json:"base_revision"`
	EditSummary    string    `json:"edit_summary"`
}

type ProtectionRequest struct {
	ProtectionLevel string     `json:"protection_level"`
	Reason          string     `json:"reason"`
	ExpiresAt       *time.Time `json:"expires_at"`
}

type DebateReferenceDTO struct {
	ID       uuid.UUID                   `json:"id"`
	Field    string                      `json:"field"`
	Start    int                         `json:"start"`
	End      int                         `json:"end"`
	Raw      string                      `json:"raw"`
	Target   DebateReferenceTargetDTO    `json:"target"`
	Relation *DebateReferenceRelationDTO `json:"relation,omitempty"`

	// Kept for internal Go callers while the JSON contract uses Target and Relation.
	ResourceID uuid.UUID  `json:"-"`
	Title      string     `json:"-"`
	Qualifier  string     `json:"-"`
	State      string     `json:"-"`
	RelationID *uuid.UUID `json:"-"`
}

type DebateReferenceTargetDTO struct {
	Type      string    `json:"type"`
	ID        uuid.UUID `json:"id"`
	Label     string    `json:"label,omitempty"`
	Subtitle  string    `json:"subtitle,omitempty"`
	Module    string    `json:"module,omitempty"`
	Path      string    `json:"path,omitempty"`
	Available bool      `json:"available"`
}

type DebateReferenceRelationDTO struct {
	ID                  uuid.UUID `json:"id"`
	Stance              string    `json:"stance"`
	State               string    `json:"state"`
	ConclusionEventID   uuid.UUID `json:"conclusion_event_id,omitempty"`
	ConclusionDirection string    `json:"conclusion_direction,omitempty"`
}

type DebateDTO struct {
	model.Debate
	References []DebateReferenceDTO `json:"references"`
}

type DebateSnapshot struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Content     string   `json:"content"`
	Tags        []string `json:"tags"`
}

type DebateRevisionDTO struct {
	ID                 uuid.UUID            `json:"id"`
	VersionNumber      int                  `json:"version_number"`
	PreviousRevisionID *uuid.UUID           `json:"previous_revision_id,omitempty"`
	EditorID           uuid.UUID            `json:"editor_id"`
	Editor             *model.User          `json:"editor,omitempty"`
	EditSummary        string               `json:"edit_summary"`
	EditType           string               `json:"edit_type"`
	Status             string               `json:"status"`
	IsCurrent          bool                 `json:"is_current"`
	CreatedAt          time.Time            `json:"created_at"`
	Snapshot           DebateSnapshot       `json:"snapshot"`
	References         []DebateReferenceDTO `json:"references,omitempty"`
}

type RevisionFieldDiff struct {
	Before  any  `json:"before"`
	After   any  `json:"after"`
	Changed bool `json:"changed"`
}

type RevisionDiffDTO struct {
	RevisionID uuid.UUID                    `json:"revision_id"`
	AgainstID  uuid.UUID                    `json:"against_revision_id"`
	Changes    map[string]RevisionFieldDiff `json:"changes"`
}

type DebateGraph struct {
	RootID            uuid.UUID              `json:"root_id"`
	Nodes             []model.Debate         `json:"nodes"`
	Relations         []model.DebateRelation `json:"relations"`
	ExpandableNodeIDs []uuid.UUID            `json:"expandable_node_ids"`
}
