package dm

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
)

type PartyDTO struct {
	Type      string    `json:"type"`
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	AvatarURL string    `json:"avatar_url"`
}

type MailboxDTO struct {
	Party  PartyDTO `json:"party"`
	Unread int64    `json:"unread"`
}

func (m MailboxDTO) Key() string { return m.Party.Type + ":" + m.Party.ID.String() }

type ConversationDTO struct {
	ID                 uuid.UUID  `json:"id"`
	ParticipantA       PartyDTO   `json:"participant_a"`
	ParticipantB       PartyDTO   `json:"participant_b"`
	LastMessageAt      *time.Time `json:"last_message_at,omitempty"`
	LastMessagePreview string     `json:"last_message_preview"`
	Unread             int64      `json:"unread"`
	Blocked            bool       `json:"blocked"`
}

type Cursor struct {
	At   time.Time `json:"at"`
	ID   uuid.UUID `json:"id"`
	Null bool      `json:"null,omitempty"`
}

type PageDTO[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// HTTP response envelopes are explicit so generated OpenAPI never degrades to responses: {}.
type MailboxesResponse struct {
	Data []MailboxDTO `json:"data"`
}
type ConversationsResponse struct {
	Data ConversationPageDTO `json:"data"`
}
type ConversationPageDTO struct {
	Items      []ConversationDTO `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}
type MessagesResponse struct {
	Data MessagePageDTO `json:"data"`
}
type MessagePageDTO struct {
	Items      []MessageDTO `json:"items"`
	NextCursor string       `json:"next_cursor,omitempty"`
}
type ConversationResponse struct {
	Data ConversationDTO `json:"data"`
}
type MessageResponse struct {
	Data MessageDTO `json:"data"`
}
type ReadResponse struct {
	Data ReadResultDTO `json:"data"`
}
type ImageResponse struct {
	Data ImageDTO `json:"data"`
}
type ReportReceiptResponse struct {
	Data ReportReceiptDTO `json:"data"`
}
type ReportsResponse struct {
	Data ReportPageDTO `json:"data"`
}
type ReportPageDTO struct {
	Items      []ReportDTO `json:"items"`
	NextCursor string      `json:"next_cursor,omitempty"`
}
type ReportResponse struct {
	Data ReportDTO `json:"data"`
}
type PermissionResponse struct {
	Data PermissionDTO `json:"data"`
}
type ErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type ReadResultDTO struct {
	ConversationUnread int64 `json:"conversation_unread"`
	MailboxUnread      int64 `json:"mailbox_unread"`
	DMUnread           int64 `json:"dm_unread"`
	TotalUnread        int64 `json:"total_unread"`
}

type MessageDTO struct {
	ID              uuid.UUID  `json:"id"`
	ConversationID  uuid.UUID  `json:"conversation_id"`
	SenderType      string     `json:"sender_type"`
	SenderID        uuid.UUID  `json:"sender_id"`
	ClientMessageID uuid.UUID  `json:"client_message_id"`
	Content         string     `json:"content"`
	ImageID         *uuid.UUID `json:"image_id,omitempty"`
	ImageURL        string     `json:"image_url,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

type SendInput struct {
	Content         string     `json:"content"`
	ClientMessageID uuid.UUID  `json:"client_message_id"`
	ImageID         *uuid.UUID `json:"image_id,omitempty"`
	ImageURL        string     `json:"image_url,omitempty"`
}

type ReportInput struct {
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

type PermissionDTO struct {
	Permission string `json:"permission"`
}

type ReviewReportInput struct {
	Status string `json:"status"`
}

type ReportDTO struct {
	ID                  string     `json:"id"`
	MessageID           string     `json:"message_id"`
	ReporterUserID      string     `json:"reporter_user_id"`
	ReportedActorUserID string     `json:"reported_actor_user_id"`
	Reason              string     `json:"reason"`
	Detail              string     `json:"detail"`
	SnapshotContent     string     `json:"snapshot_content"`
	HasSnapshotImage    bool       `json:"has_snapshot_image"`
	ConversationContext string     `json:"conversation_context"`
	Status              string     `json:"status"`
	ReviewedByUserID    *string    `json:"reviewed_by_user_id,omitempty"`
	ReviewedAt          *time.Time `json:"reviewed_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
}

// ReportReceiptDTO deliberately excludes the report identifier. It is an audit ID.
type ReportReceiptDTO struct {
	Status string `json:"status"`
}

func encodeCursor(cursor Cursor) string {
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeCursor(value string) (Cursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return Cursor{}, apperr.BadRequest("validation.invalid_request", "cursor is invalid")
	}
	var cursor Cursor
	if err := json.Unmarshal(data, &cursor); err != nil || (!cursor.Null && cursor.At.IsZero()) || cursor.ID == uuid.Nil {
		return Cursor{}, apperr.BadRequest("validation.invalid_request", "cursor is invalid")
	}
	return cursor, nil
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return 30
	}
	if limit > 100 {
		return 100
	}
	return limit
}
