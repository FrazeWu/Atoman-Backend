package dm

import (
	"errors"
	"testing"

	"atoman/internal/model"

	"github.com/google/uuid"
)

func TestConversationNormalization(t *testing.T) {
	first, second, channel := uuid.New(), uuid.New(), uuid.New()

	a, b, err := NormalizeParties(TargetRef{Type: model.DMPartyUser, ID: second}, TargetRef{Type: model.DMPartyUser, ID: first})
	if err != nil || a.ID.String() > b.ID.String() || a.Type != model.DMPartyUser || b.Type != model.DMPartyUser {
		t.Fatalf("user conversation was not normalized: %#v %#v %v", a, b, err)
	}

	a, b, err = NormalizeParties(TargetRef{Type: model.DMPartyChannel, ID: channel}, TargetRef{Type: model.DMPartyUser, ID: first})
	if err != nil || a != (TargetRef{Type: model.DMPartyUser, ID: first}) || b != (TargetRef{Type: model.DMPartyChannel, ID: channel}) {
		t.Fatalf("user-channel conversation was not normalized: %#v %#v %v", a, b, err)
	}

	_, _, err = NormalizeParties(TargetRef{Type: model.DMPartyChannel, ID: first}, TargetRef{Type: model.DMPartyChannel, ID: second})
	if !errors.Is(err, ErrConversationForbidden) {
		t.Fatalf("expected channel-channel rejection, got %v", err)
	}
}

func TestPolicyResolveSender(t *testing.T) {
	user, owner, channel := uuid.New(), uuid.New(), uuid.New()
	conversation := model.DMConversation{
		ParticipantAType: model.DMPartyUser,
		ParticipantA:     user,
		ParticipantBType: model.DMPartyChannel,
		ParticipantB:     channel,
	}

	sender, err := ResolveSender(user, conversation, owner)
	if err != nil || sender.SenderType != model.DMPartyUser || sender.SenderID != user || sender.ActorUserID != user {
		t.Fatalf("expected personal sender: %#v %v", sender, err)
	}

	sender, err = ResolveSender(owner, conversation, owner)
	if err != nil || sender.SenderType != model.DMPartyChannel || sender.SenderID != channel || sender.ActorUserID != owner {
		t.Fatalf("expected channel owner sender: %#v %v", sender, err)
	}

	_, err = ResolveSender(uuid.New(), conversation, owner)
	if !errors.Is(err, ErrConversationForbidden) {
		t.Fatalf("expected non-owner rejection, got %v", err)
	}
}
