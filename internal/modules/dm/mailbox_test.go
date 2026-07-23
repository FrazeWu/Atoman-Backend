package dm

import (
	"context"
	"errors"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
)

func TestMailboxesIncludeUserAndOwnedChannels(t *testing.T) {
	db := testDB(t)
	actor, other := testUser(t, db), testUser(t, db)
	channel := uuid.New()
	if err := db.Create(&model.Channel{Base: model.Base{ID: channel}, UserID: &actor, Name: "owned", Slug: "owned"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.DMConversation{ParticipantAType: model.DMPartyUser, ParticipantA: other, ParticipantBType: model.DMPartyChannel, ParticipantB: channel}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepo(db), nil, nil, nil)
	mailboxes, err := service.ListMailboxes(context.Background(), authctx.CurrentUser{ID: actor})
	if err != nil {
		t.Fatal(err)
	}
	if len(mailboxes) != 2 || mailboxes[0].Key() != "user:"+actor.String() || mailboxes[1].Key() != "channel:"+channel.String() {
		t.Fatalf("unexpected mailboxes: %#v", mailboxes)
	}
}

func TestConversationCursorAndMailboxAccess(t *testing.T) {
	db := testDB(t)
	actor, other, third := testUser(t, db), testUser(t, db), testUser(t, db)
	service := NewService(NewRepo(db), nil, nil, nil)
	base := time.Now().Add(-time.Hour)
	for i := range 3 {
		conversation := model.DMConversation{ParticipantAType: model.DMPartyUser, ParticipantA: actor, ParticipantBType: model.DMPartyUser, ParticipantB: uuid.New(), LastMessageAt: ptrTime(base.Add(time.Duration(i) * time.Minute))}
		if err := db.Create(&conversation).Error; err != nil {
			t.Fatal(err)
		}
	}
	page, err := service.ListConversations(context.Background(), authctx.CurrentUser{ID: actor}, TargetRef{Type: model.DMPartyUser, ID: actor}, "", 2)
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("unexpected first page: %#v %v", page, err)
	}
	next, err := service.ListConversations(context.Background(), authctx.CurrentUser{ID: actor}, TargetRef{Type: model.DMPartyUser, ID: actor}, page.NextCursor, 2)
	if err != nil || len(next.Items) != 1 || next.Items[0].ID == page.Items[0].ID || next.Items[0].ID == page.Items[1].ID {
		t.Fatalf("unexpected second page: %#v %v", next, err)
	}
	_, err = service.ListConversations(context.Background(), authctx.CurrentUser{ID: other}, TargetRef{Type: model.DMPartyUser, ID: actor}, "", 30)
	if !errors.Is(err, ErrConversationForbidden) {
		t.Fatalf("expected forbidden mailbox, got %v", err)
	}
	_ = third
}

func TestMessageCursorReturnsNewestWindowInChronologicalOrder(t *testing.T) {
	db := testDB(t)
	actor, other := testUser(t, db), testUser(t, db)
	conversation := model.DMConversation{ParticipantAType: model.DMPartyUser, ParticipantA: actor, ParticipantBType: model.DMPartyUser, ParticipantB: other}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	for i := range 4 {
		message := model.DMMessage{ConversationID: conversation.ID, SenderType: model.DMPartyUser, SenderID: other, ActorUserID: other, ClientMessageID: uuid.New(), Content: "message", Base: model.Base{CreatedAt: base.Add(time.Duration(i) * time.Minute)}}
		if err := db.Create(&message).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(NewRepo(db), nil, nil, nil)
	page, err := service.ListMessages(context.Background(), authctx.CurrentUser{ID: actor}, conversation.ID, "", 2)
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" || !page.Items[0].CreatedAt.Before(page.Items[1].CreatedAt) {
		t.Fatalf("unexpected latest page: %#v %v", page, err)
	}
	older, err := service.ListMessages(context.Background(), authctx.CurrentUser{ID: actor}, conversation.ID, page.NextCursor, 2)
	if err != nil || len(older.Items) != 2 || !older.Items[0].CreatedAt.Before(older.Items[1].CreatedAt) || older.Items[1].ID == page.Items[0].ID {
		t.Fatalf("unexpected older page: %#v %v", older, err)
	}
}

func TestMarkReadCountsOnlyCurrentMailbox(t *testing.T) {
	db := testDB(t)
	actor, sender, owner := testUser(t, db), testUser(t, db), testUser(t, db)
	channel := uuid.New()
	if err := db.Create(&model.Channel{Base: model.Base{ID: channel}, UserID: &owner, Name: "owned-read", Slug: "owned-read"}).Error; err != nil {
		t.Fatal(err)
	}
	conversation := model.DMConversation{ParticipantAType: model.DMPartyUser, ParticipantA: actor, ParticipantBType: model.DMPartyChannel, ParticipantB: channel}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatal(err)
	}
	for _, message := range []model.DMMessage{
		{ConversationID: conversation.ID, SenderType: model.DMPartyChannel, SenderID: channel, ActorUserID: owner, ClientMessageID: uuid.New(), Content: "channel"},
		{ConversationID: conversation.ID, SenderType: model.DMPartyUser, SenderID: sender, ActorUserID: sender, ClientMessageID: uuid.New(), Content: "user"},
	} {
		if err := db.Create(&message).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(NewRepo(db), nil, nil, nil)
	result, err := service.MarkRead(context.Background(), authctx.CurrentUser{ID: actor}, conversation.ID)
	if err != nil || result.ConversationUnread != 0 || result.MailboxUnread != 0 || result.DMUnread != 0 || result.TotalUnread != 0 {
		t.Fatalf("unexpected read result: %#v %v", result, err)
	}
	var unreadUser int64
	if err := db.Model(&model.DMMessage{}).Where("sender_type = ? AND read_at IS NULL", model.DMPartyUser).Count(&unreadUser).Error; err != nil || unreadUser != 1 {
		t.Fatalf("user message should remain unread: %d %v", unreadUser, err)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
