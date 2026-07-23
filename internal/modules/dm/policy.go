package dm

import (
	"fmt"

	"atoman/internal/model"

	"github.com/google/uuid"
)

type TargetRef struct {
	Type string
	ID   uuid.UUID
}

type SenderIdentity struct {
	SenderType  string
	SenderID    uuid.UUID
	ActorUserID uuid.UUID
}

func NormalizeParties(first, second TargetRef) (TargetRef, TargetRef, error) {
	if !validParty(first) || !validParty(second) {
		return TargetRef{}, TargetRef{}, fmt.Errorf("%w: invalid party", ErrConversationForbidden)
	}
	if first.Type == model.DMPartyChannel && second.Type == model.DMPartyChannel {
		return TargetRef{}, TargetRef{}, ErrConversationForbidden
	}
	if first.Type == model.DMPartyChannel {
		first, second = second, first
	}
	if first.Type != model.DMPartyUser {
		return TargetRef{}, TargetRef{}, ErrConversationForbidden
	}
	if second.Type == model.DMPartyUser {
		if first.ID == second.ID {
			return TargetRef{}, TargetRef{}, ErrSelfTarget
		}
		if first.ID.String() > second.ID.String() {
			first, second = second, first
		}
	}
	return first, second, nil
}

func ResolveSender(actorUserID uuid.UUID, conversation model.DMConversation, channelOwnerID uuid.UUID) (SenderIdentity, error) {
	if conversation.ParticipantAType != model.DMPartyUser {
		return SenderIdentity{}, ErrConversationForbidden
	}
	if actorUserID == conversation.ParticipantA {
		return SenderIdentity{SenderType: model.DMPartyUser, SenderID: actorUserID, ActorUserID: actorUserID}, nil
	}
	if conversation.ParticipantBType == model.DMPartyUser && actorUserID == conversation.ParticipantB {
		return SenderIdentity{SenderType: model.DMPartyUser, SenderID: actorUserID, ActorUserID: actorUserID}, nil
	}
	if conversation.ParticipantBType == model.DMPartyChannel && actorUserID == channelOwnerID {
		return SenderIdentity{SenderType: model.DMPartyChannel, SenderID: conversation.ParticipantB, ActorUserID: actorUserID}, nil
	}
	return SenderIdentity{}, ErrConversationForbidden
}

func validParty(party TargetRef) bool {
	return party.ID != uuid.Nil && (party.Type == model.DMPartyUser || party.Type == model.DMPartyChannel)
}
