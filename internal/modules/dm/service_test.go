package dm

import (
	"context"
	"errors"
	"testing"

	"atoman/internal/model"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestServiceIdentity(t *testing.T) {
	db := testDB(t)
	actor, recipient, owner, channel := testUser(t, db), testUser(t, db), testUser(t, db), uuid.New()
	if err := db.Create(&model.Channel{Base: model.Base{ID: channel}, UserID: &owner, Name: "channel-" + channel.String(), Slug: "channel-" + channel.String()}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepo(db), nil, nil, nil)

	message, err := service.SendToTarget(context.Background(), actor, TargetRef{Type: model.DMPartyUser, ID: recipient}, SendInput{Content: "hello", ClientMessageID: uuid.New()})
	if err != nil || message.SenderType != model.DMPartyUser || message.SenderID != actor {
		t.Fatalf("unexpected user message: %#v %v", message, err)
	}

	channelMessage, err := service.SendToTarget(context.Background(), actor, TargetRef{Type: model.DMPartyChannel, ID: channel}, SendInput{Content: "hello channel", ClientMessageID: uuid.New()})
	if err != nil || channelMessage.SenderType != model.DMPartyUser || channelMessage.SenderID != actor {
		t.Fatalf("target sends must use personal identity: %#v %v", channelMessage, err)
	}

	ownerReply, err := service.SendInConversation(context.Background(), owner, channelMessage.ConversationID, SendInput{Content: "reply", ClientMessageID: uuid.New()})
	if err != nil || ownerReply.SenderType != model.DMPartyChannel || ownerReply.SenderID != channel {
		t.Fatalf("unexpected channel reply: %#v %v", ownerReply, err)
	}
	if _, ok := any(ownerReply).(interface{ GetActorUserID() uuid.UUID }); ok {
		t.Fatal("message DTO must not expose actor user id")
	}

	_, err = service.SendInConversation(context.Background(), recipient, channelMessage.ConversationID, SendInput{Content: "forbidden", ClientMessageID: uuid.New()})
	if !errors.Is(err, ErrConversationForbidden) {
		t.Fatalf("expected non-owner rejection, got %v", err)
	}
}

func TestSendIdempotency(t *testing.T) {
	db := testDB(t)
	actor, recipient := testUser(t, db), testUser(t, db)
	publisher := &recordingPublisher{}
	service := NewService(NewRepo(db), nil, publisher, nil)
	input := SendInput{Content: "same", ClientMessageID: uuid.New()}

	first, err := service.SendToTarget(context.Background(), actor, TargetRef{Type: model.DMPartyUser, ID: recipient}, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.SendToTarget(context.Background(), actor, TargetRef{Type: model.DMPartyUser, ID: recipient}, input)
	if err != nil || first.ID != second.ID {
		t.Fatalf("expected idempotent result: %#v %#v %v", first, second, err)
	}
	if len(publisher.events) != 4 {
		t.Fatalf("expected events only for the first create, got %d", len(publisher.events))
	}
	created, ok := publisher.events[0].payload.(MessageCreatedEventDTO)
	if !ok || created.Message.ID != first.ID || created.Conversation.ID != first.ConversationID || created.Mailbox.Party.ID == uuid.Nil {
		t.Fatalf("unexpected created event payload: %#v", publisher.events[0].payload)
	}
	var count int64
	if err := db.Model(&model.DMMessage{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("expected one stored message, got %d: %v", count, err)
	}
}

type recordedDMEvent struct {
	userID  uuid.UUID
	event   string
	payload any
}
type recordingPublisher struct{ events []recordedDMEvent }

func (p *recordingPublisher) Push(userID uuid.UUID, event string, payload any) {
	p.events = append(p.events, recordedDMEvent{userID: userID, event: event, payload: payload})
}

func TestServiceRejectsSelfAndOwnedChannelTargets(t *testing.T) {
	db := testDB(t)
	actor, channelID := testUser(t, db), uuid.New()
	if err := db.Create(&model.Channel{Base: model.Base{ID: channelID}, UserID: &actor, Name: "owned-" + channelID.String(), Slug: "owned-" + channelID.String()}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepo(db), nil, nil, nil)

	_, err := service.SendToTarget(context.Background(), actor, TargetRef{Type: model.DMPartyUser, ID: actor}, SendInput{Content: "self", ClientMessageID: uuid.New()})
	if !errors.Is(err, ErrSelfTarget) {
		t.Fatalf("expected self target rejection, got %v", err)
	}
	_, err = service.SendToTarget(context.Background(), actor, TargetRef{Type: model.DMPartyChannel, ID: channelID}, SendInput{Content: "own channel", ClientMessageID: uuid.New()})
	if !errors.Is(err, ErrSelfTarget) {
		t.Fatalf("expected own channel rejection, got %v", err)
	}
}

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/dm.sqlite"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Channel{}, &model.UserSettings{}, &model.DMChannelSettings{}, &model.UserBlock{}, &model.Follow{}, &model.DMConversation{}, &model.DMMessage{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func testUser(t *testing.T, db *gorm.DB) uuid.UUID {
	t.Helper()
	user := model.User{UUID: uuid.New(), Username: uuid.NewString(), Email: uuid.NewString() + "@example.test", Password: "test"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	return user.UUID
}
