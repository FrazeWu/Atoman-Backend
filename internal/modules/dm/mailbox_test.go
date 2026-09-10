package dm

import (
	"context"
	"errors"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
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

func TestConversationPartiesIncludeNamesAndAvatars(t *testing.T) {
	db := testDB(t)
	actor := model.User{UUID: uuid.New(), Username: "actor-user", Email: "actor@example.test", Password: "test", DisplayName: "Actor Name", AvatarURL: "/avatars/actor.png"}
	other := model.User{UUID: uuid.New(), Username: "other-user", Email: "other@example.test", Password: "test", DisplayName: "Other Name", AvatarURL: "/avatars/other.png"}
	owner := model.User{UUID: uuid.New(), Username: "channel-owner", Email: "owner@example.test", Password: "test", DisplayName: "Channel Owner", AvatarURL: "/avatars/owner.png"}
	for _, user := range []*model.User{&actor, &other, &owner} {
		if err := db.Create(user).Error; err != nil {
			t.Fatal(err)
		}
	}
	channelID := uuid.New()
	if err := db.Create(&model.Channel{Base: model.Base{ID: channelID}, UserID: &owner.UUID, Name: "Channel Name", Slug: "channel-name", CoverURL: "/covers/channel.png"}).Error; err != nil {
		t.Fatal(err)
	}
	userConversation := model.DMConversation{ParticipantAType: model.DMPartyUser, ParticipantA: actor.UUID, ParticipantBType: model.DMPartyUser, ParticipantB: other.UUID}
	channelConversation := model.DMConversation{ParticipantAType: model.DMPartyUser, ParticipantA: actor.UUID, ParticipantBType: model.DMPartyChannel, ParticipantB: channelID}
	for _, conversation := range []*model.DMConversation{&userConversation, &channelConversation} {
		if err := db.Create(conversation).Error; err != nil {
			t.Fatal(err)
		}
	}

	service := NewService(NewRepo(db), nil, nil, nil)
	mailboxes, err := service.ListMailboxes(context.Background(), authctx.CurrentUser{ID: actor.UUID, Username: actor.Username})
	if err != nil {
		t.Fatal(err)
	}
	if mailboxes[0].Party.Name != actor.DisplayName || mailboxes[0].Party.AvatarURL != actor.AvatarURL {
		t.Fatalf("user mailbox party = %#v", mailboxes[0].Party)
	}
	if mailboxes[1].Party.Name != "Channel Name" || mailboxes[1].Party.AvatarURL != "/covers/channel.png" {
		t.Fatalf("channel mailbox party = %#v", mailboxes[1].Party)
	}

	page, err := service.ListConversations(context.Background(), authctx.CurrentUser{ID: actor.UUID}, TargetRef{Type: model.DMPartyUser, ID: actor.UUID}, "", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("conversation count = %d", len(page.Items))
	}
	for _, conversation := range page.Items {
		if conversation.ParticipantA.Name != actor.DisplayName || conversation.ParticipantA.AvatarURL != actor.AvatarURL {
			t.Fatalf("participant A = %#v", conversation.ParticipantA)
		}
	}
	if target, err := service.GetTargetConversation(context.Background(), actor.UUID, TargetRef{Type: model.DMPartyUser, ID: other.UUID}); err != nil {
		t.Fatal(err)
	} else if (target.ParticipantA.Name != other.DisplayName || target.ParticipantA.AvatarURL != other.AvatarURL) && (target.ParticipantB.Name != other.DisplayName || target.ParticipantB.AvatarURL != other.AvatarURL) {
		t.Fatalf("target conversation participants = %#v", target)
	}

	channelPage, err := service.ListConversations(context.Background(), authctx.CurrentUser{ID: owner.UUID}, TargetRef{Type: model.DMPartyChannel, ID: channelID}, "", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(channelPage.Items) != 1 || channelPage.Items[0].ParticipantB.Name != "Channel Name" || channelPage.Items[0].ParticipantB.AvatarURL != "/covers/channel.png" {
		t.Fatalf("channel conversation = %#v", channelPage.Items)
	}
}

func TestPartyNameFallsBackToUsername(t *testing.T) {
	db := testDB(t)
	actor := testUser(t, db)
	other := model.User{UUID: uuid.New(), Username: "fallback-user", Email: "fallback@example.test", Password: "test"}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	conversation := model.DMConversation{ParticipantAType: model.DMPartyUser, ParticipantA: actor, ParticipantBType: model.DMPartyUser, ParticipantB: other.UUID}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatal(err)
	}
	page, err := NewService(NewRepo(db), nil, nil, nil).ListConversations(context.Background(), authctx.CurrentUser{ID: actor}, TargetRef{Type: model.DMPartyUser, ID: actor}, "", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ParticipantB.Name != other.Username {
		t.Fatalf("fallback party = %#v", page.Items)
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

func TestConversationListLoadsUnreadAndBlockedStateWithFixedQueryCount(t *testing.T) {
	db := testDB(t)
	actor, other, owner := testUser(t, db), testUser(t, db), testUser(t, db)
	channelID := uuid.New()
	if err := db.Create(&model.Channel{Base: model.Base{ID: channelID}, UserID: &owner, Name: "owned", Slug: "owned-" + channelID.String()}).Error; err != nil {
		t.Fatal(err)
	}
	userConversation := model.DMConversation{ParticipantAType: model.DMPartyUser, ParticipantA: actor, ParticipantBType: model.DMPartyUser, ParticipantB: other, LastMessageAt: ptrTime(time.Now())}
	channelConversation := model.DMConversation{ParticipantAType: model.DMPartyUser, ParticipantA: actor, ParticipantBType: model.DMPartyChannel, ParticipantB: channelID, LastMessageAt: ptrTime(time.Now().Add(-time.Minute))}
	if err := db.Create(&userConversation).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&channelConversation).Error; err != nil {
		t.Fatal(err)
	}
	messages := []model.DMMessage{
		{ConversationID: userConversation.ID, SenderType: model.DMPartyUser, SenderID: other, ActorUserID: other, ClientMessageID: uuid.New(), Content: "user unread"},
		{ConversationID: channelConversation.ID, SenderType: model.DMPartyChannel, SenderID: channelID, ActorUserID: owner, ClientMessageID: uuid.New(), Content: "channel unread"},
	}
	if err := db.Create(&messages).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.UserBlock{BlockerID: actor, BlockedID: other}).Error; err != nil {
		t.Fatal(err)
	}

	queryCount := 0
	callbackName := "test:count_conversation_list_queries"
	countQuery := func(*gorm.DB) { queryCount++ }
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, countQuery); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Row().Before("gorm:row").Register(callbackName, countQuery); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove(callbackName)
		_ = db.Callback().Row().Remove(callbackName)
	})

	page, err := NewService(NewRepo(db), nil, nil, nil).ListConversations(
		context.Background(),
		authctx.CurrentUser{ID: actor},
		TargetRef{Type: model.DMPartyUser, ID: actor},
		"",
		30,
	)
	if err != nil {
		t.Fatal(err)
	}
	if queryCount != 6 {
		t.Fatalf("conversation list queries = %d, want 6", queryCount)
	}
	if len(page.Items) != 2 {
		t.Fatalf("conversation count = %d, want 2", len(page.Items))
	}
	byID := map[uuid.UUID]ConversationDTO{}
	for _, item := range page.Items {
		byID[item.ID] = item
	}
	if item := byID[userConversation.ID]; item.Unread != 1 || !item.Blocked {
		t.Fatalf("user conversation state = %#v", item)
	}
	if item := byID[channelConversation.ID]; item.Unread != 1 || item.Blocked {
		t.Fatalf("channel conversation state = %#v", item)
	}
}

func TestConversationCursorPaginatesNullLastMessageAtWithoutDuplicates(t *testing.T) {
	db := testDB(t)
	actor := testUser(t, db)
	for range 5 {
		conversation := model.DMConversation{ParticipantAType: model.DMPartyUser, ParticipantA: actor, ParticipantBType: model.DMPartyUser, ParticipantB: uuid.New()}
		if err := db.Create(&conversation).Error; err != nil {
			t.Fatal(err)
		}
	}

	service := NewService(NewRepo(db), nil, nil, nil)
	cursor := ""
	seen := make(map[uuid.UUID]struct{})
	for pageNumber := 0; pageNumber < 3; pageNumber++ {
		page, err := service.ListConversations(context.Background(), authctx.CurrentUser{ID: actor}, TargetRef{Type: model.DMPartyUser, ID: actor}, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 0 {
			t.Fatalf("page %d unexpectedly empty", pageNumber)
		}
		for _, conversation := range page.Items {
			if _, exists := seen[conversation.ID]; exists {
				t.Fatalf("conversation %s appeared more than once", conversation.ID)
			}
			seen[conversation.ID] = struct{}{}
		}
		cursor = page.NextCursor
	}
	if len(seen) != 5 || cursor != "" {
		t.Fatalf("expected all five null-timestamp conversations exactly once, got %d with cursor %q", len(seen), cursor)
	}
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
