package dm

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
)

func TestSendPolicy(t *testing.T) {
	tests := []struct {
		name       string
		targetType string
		permission string
		follow     bool
		wantErr    error
	}{
		{name: "user default permits first message", targetType: model.DMPartyUser},
		{name: "user following only rejects stranger", targetType: model.DMPartyUser, permission: model.DMPermissionFollowingOnly, wantErr: ErrPermissionDenied},
		{name: "user following only permits followed sender", targetType: model.DMPartyUser, permission: model.DMPermissionFollowingOnly, follow: true},
		{name: "user anyone permits consecutive messages", targetType: model.DMPartyUser, permission: model.DMPermissionAnyone},
		{name: "channel default permits first message", targetType: model.DMPartyChannel},
		{name: "channel anyone permits consecutive messages", targetType: model.DMPartyChannel, permission: model.DMPermissionAnyone},
		{name: "closed channel rejects", targetType: model.DMPartyChannel, permission: model.DMPermissionClosed, wantErr: ErrPermissionDenied},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := testDB(t)
			actor, targetUser := testUser(t, db), testUser(t, db)
			target := TargetRef{Type: tt.targetType, ID: targetUser}
			if tt.targetType == model.DMPartyChannel {
				channelID := uuid.New()
				if err := db.Create(&model.Channel{Base: model.Base{ID: channelID}, UserID: &targetUser, Name: "channel-" + channelID.String(), Slug: "channel-" + channelID.String()}).Error; err != nil {
					t.Fatal(err)
				}
				target.ID = channelID
				if tt.permission != "" {
					if err := db.Create(&model.DMChannelSettings{ChannelID: channelID, Permission: tt.permission}).Error; err != nil {
						t.Fatal(err)
					}
				}
			} else if tt.permission != "" {
				if err := db.Create(&model.UserSettings{UserID: targetUser, DMPermission: tt.permission}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if tt.follow {
				if err := db.Create(&model.Follow{FollowerID: targetUser, FollowingID: actor}).Error; err != nil {
					t.Fatal(err)
				}
			}

			service := NewService(NewRepo(db), nil, nil, nil)
			_, err := service.SendToTarget(context.Background(), actor, target, SendInput{Content: "first", ClientMessageID: uuid.New()})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("first send error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			_, err = service.SendToTarget(context.Background(), actor, target, SendInput{Content: "second", ClientMessageID: uuid.New()})
			if tt.permission == model.DMPermissionAnyone || tt.permission == model.DMPermissionFollowingOnly {
				if err != nil {
					t.Fatalf("consecutive send error = %v", err)
				}
			} else if !errors.Is(err, ErrWaitingReply) {
				t.Fatalf("consecutive send error = %v, want %v", err, ErrWaitingReply)
			}
		})
	}
}

func TestBlockPolicyAndConversationBlock(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%t", reverse), func(t *testing.T) {
			db := testDB(t)
			actor, recipient := testUser(t, db), testUser(t, db)
			service := NewService(NewRepo(db), nil, nil, nil)
			message, err := service.SendToTarget(context.Background(), actor, TargetRef{Type: model.DMPartyUser, ID: recipient}, SendInput{Content: "first", ClientMessageID: uuid.New()})
			if err != nil {
				t.Fatal(err)
			}
			blocker, blocked := actor, recipient
			if reverse {
				blocker, blocked = recipient, actor
			}
			if err := db.Create(&model.UserBlock{BlockerID: blocker, BlockedID: blocked}).Error; err != nil {
				t.Fatal(err)
			}
			_, err = service.SendInConversation(context.Background(), actor, message.ConversationID, SendInput{Content: "blocked", ClientMessageID: uuid.New()})
			if !errors.Is(err, ErrBlocked) {
				t.Fatalf("send error = %v, want %v", err, ErrBlocked)
			}
		})
	}

	db := testDB(t)
	owner, sender := testUser(t, db), testUser(t, db)
	channelID := uuid.New()
	if err := db.Create(&model.Channel{Base: model.Base{ID: channelID}, UserID: &owner, Name: "channel-" + channelID.String(), Slug: "channel-" + channelID.String()}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepo(db), nil, nil, nil)
	message, err := service.SendToTarget(context.Background(), sender, TargetRef{Type: model.DMPartyChannel, ID: channelID}, SendInput{Content: "first", ClientMessageID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.BlockConversation(context.Background(), authctx.CurrentUser{ID: sender}, message.ConversationID); err != nil {
		t.Fatal(err)
	}
	var block model.UserBlock
	if err := db.First(&block, "blocker_id = ? AND blocked_id = ?", sender, owner).Error; err != nil {
		t.Fatalf("expected sender to block channel owner: %v", err)
	}
	if _, err := service.UnblockConversation(context.Background(), authctx.CurrentUser{ID: sender}, message.ConversationID); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&block, "blocker_id = ? AND blocked_id = ?", sender, owner).Error; err == nil {
		t.Fatal("expected block to be removed")
	}
	if err := db.Create(&model.UserBlock{BlockerID: owner, BlockedID: sender}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.SendInConversation(context.Background(), sender, message.ConversationID, SendInput{Content: "blocked by owner", ClientMessageID: uuid.New()}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("channel owner block error = %v, want %v", err, ErrBlocked)
	}
}

func TestOneBeforeReplyAllowsSenderAfterReply(t *testing.T) {
	db := testDB(t)
	actor, recipient := testUser(t, db), testUser(t, db)
	service := NewService(NewRepo(db), nil, nil, nil)
	first, err := service.SendToTarget(context.Background(), actor, TargetRef{Type: model.DMPartyUser, ID: recipient}, SendInput{Content: "first", ClientMessageID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SendInConversation(context.Background(), recipient, first.ConversationID, SendInput{Content: "reply", ClientMessageID: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SendInConversation(context.Background(), actor, first.ConversationID, SendInput{Content: "after reply", ClientMessageID: uuid.New()}); err != nil {
		t.Fatalf("send after reply: %v", err)
	}
}

func TestRateLimits(t *testing.T) {
	db := testDB(t)
	actor, recipient := testUser(t, db), testUser(t, db)
	if err := db.Create(&model.UserSettings{UserID: recipient, DMPermission: model.DMPermissionAnyone}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepo(db), nil, nil, nil)
	conversation, err := NewRepo(db).FindOrCreateConversation(TargetRef{Type: model.DMPartyUser, ID: actor}, TargetRef{Type: model.DMPartyUser, ID: recipient})
	if err != nil {
		t.Fatal(err)
	}
	for range 30 {
		if err := db.Create(&model.DMMessage{ConversationID: conversation.ID, SenderType: model.DMPartyUser, SenderID: actor, ActorUserID: actor, ClientMessageID: uuid.New(), Content: "recent", Base: model.Base{CreatedAt: time.Now()}}).Error; err != nil {
			t.Fatal(err)
		}
	}
	_, err = service.SendInConversation(context.Background(), actor, conversation.ID, SendInput{Content: "31", ClientMessageID: uuid.New()})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("minute limit error = %v, want %v", err, ErrRateLimited)
	}
	previous := model.DMMessage{ConversationID: conversation.ID, SenderType: model.DMPartyUser, SenderID: actor, ActorUserID: actor, ClientMessageID: uuid.New(), Content: "idempotent", Base: model.Base{CreatedAt: time.Now()}}
	if err := db.Create(&previous).Error; err != nil {
		t.Fatal(err)
	}
	message, err := service.SendInConversation(context.Background(), actor, conversation.ID, SendInput{Content: "retry", ClientMessageID: previous.ClientMessageID})
	if err != nil || message.ID != previous.ID {
		t.Fatalf("idempotent send = %#v, %v", message, err)
	}

	db = testDB(t)
	actor = testUser(t, db)
	for range 10 {
		target := testUser(t, db)
		conversation, err := NewRepo(db).FindOrCreateConversation(TargetRef{Type: model.DMPartyUser, ID: actor}, TargetRef{Type: model.DMPartyUser, ID: target})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.DMMessage{ConversationID: conversation.ID, SenderType: model.DMPartyUser, SenderID: actor, ActorUserID: actor, ClientMessageID: uuid.New(), Content: "new target"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	service = NewService(NewRepo(db), nil, nil, nil)
	_, err = service.SendToTarget(context.Background(), actor, TargetRef{Type: model.DMPartyUser, ID: testUser(t, db)}, SendInput{Content: "eleventh", ClientMessageID: uuid.New()})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("new target limit error = %v, want %v", err, ErrRateLimited)
	}
}
