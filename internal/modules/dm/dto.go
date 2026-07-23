package dm

import (
	"time"

	"github.com/google/uuid"
)

type PartyDTO struct {
	Type      string    `json:"type"`
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	AvatarURL string    `json:"avatar_url"`
}

type MailboxDTO struct {
	Party PartyDTO `json:"party"`
}

func (m MailboxDTO) Key() string { return m.Party.Type + ":" + m.Party.ID.String() }

type ConversationDTO struct {
	ID                 uuid.UUID  `json:"id"`
	ParticipantA       PartyDTO   `json:"participant_a"`
	ParticipantB       PartyDTO   `json:"participant_b"`
	LastMessageAt      *time.Time `json:"last_message_at,omitempty"`
	LastMessagePreview string     `json:"last_message_preview"`
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
