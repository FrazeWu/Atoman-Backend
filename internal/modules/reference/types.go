package reference

import (
	"errors"

	"github.com/google/uuid"
)

type Kind string

const (
	KindUser       Kind = "user"
	KindResource   Kind = "resource"
	TargetTypeUser      = "user"
	AudiencePublic      = "public"
)

var (
	ErrInvalidSyntax     = errors.New("invalid reference syntax")
	ErrTargetUnavailable = errors.New("reference target unavailable")
)

var SupportedResourceTypes = []string{
	"post",
	"thread",
	"debate",
	"feed",
	"article",
	"artist",
	"album",
	"song",
	"playlist",
	"podcast",
	"episode",
	"video",
	"person",
	"event",
	"channel",
	"collection",
	"comment",
}

var supportedResourceTypeSet = func() map[string]struct{} {
	result := make(map[string]struct{}, len(SupportedResourceTypes))
	for _, targetType := range SupportedResourceTypes {
		result[targetType] = struct{}{}
	}
	return result
}()

func IsSupportedResourceType(value string) bool {
	_, ok := supportedResourceTypeSet[value]
	return ok
}

type ParsedReference struct {
	Kind       Kind
	TargetType string
	Identifier string
	Start      int
	End        int
}

type Viewer struct {
	UserID uuid.UUID
}

type Target struct {
	Type      string    `json:"type"`
	ID        uuid.UUID `json:"id"`
	Label     string    `json:"label"`
	Subtitle  string    `json:"subtitle,omitempty"`
	Module    string    `json:"module"`
	Path      string    `json:"path"`
	Available bool      `json:"available"`
}

type ResolvedReference struct {
	Kind       Kind      `json:"kind"`
	TargetType string    `json:"target_type"`
	TargetID   uuid.UUID `json:"target_id,omitempty"`
	Field      string    `json:"field,omitempty"`
	Start      int       `json:"start"`
	End        int       `json:"end"`
	Label      string    `json:"label,omitempty"`
	Subtitle   string    `json:"subtitle,omitempty"`
	Module     string    `json:"module,omitempty"`
	Path       string    `json:"path,omitempty"`
	Available  bool      `json:"available"`
}

type SearchResponse struct {
	Data []Target `json:"data"`
}

type ResolveResponse struct {
	Data []ResolvedReference `json:"data"`
}

type ErrorBody struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Details interface{} `json:"details,omitempty"`
}

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type Source struct {
	Type                         string
	ID                           uuid.UUID
	ActorID                      uuid.UUID
	Audience                     string
	MentionNotificationType      string
	NotificationSourceType       string
	SuppressMentionNotifications bool
	Meta                         map[string]interface{}
}

type Field struct {
	Name    string
	Content string
}
