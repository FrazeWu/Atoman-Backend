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
}

type Cursor struct {
	At time.Time `json:"at"`
	ID uuid.UUID `json:"id"`
}

type PageDTO[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
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
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.At.IsZero() || cursor.ID == uuid.Nil {
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
