package dm

import (
	"atoman/internal/collab"

	"github.com/google/uuid"
)

// UserHubPublisher keeps delivery outside database transactions.
type UserHubPublisher struct{ Hub *collab.UserHub }

func (p UserHubPublisher) Push(userID uuid.UUID, event string, payload any) {
	if p.Hub != nil {
		p.Hub.Push(userID, event, payload)
	}
}

type MessageCreatedEventDTO struct {
	Message      MessageDTO      `json:"message"`
	Conversation ConversationDTO `json:"conversation"`
	Mailbox      MailboxDTO      `json:"mailbox"`
	DMUnread     int64           `json:"dm_unread"`
	TotalUnread  int64           `json:"total_unread"`
}

type MessageReadEventDTO struct {
	ConversationID string     `json:"conversation_id"`
	ReadAt         string     `json:"read_at"`
	Mailbox        MailboxDTO `json:"mailbox"`
	DMUnread       int64      `json:"dm_unread"`
	TotalUnread    int64      `json:"total_unread"`
}

type MailboxUpdatedEventDTO struct {
	Mailbox     MailboxDTO `json:"mailbox"`
	DMUnread    int64      `json:"dm_unread"`
	TotalUnread int64      `json:"total_unread"`
}
