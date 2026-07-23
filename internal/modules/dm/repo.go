package dm

import (
	"errors"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repo struct{ db *gorm.DB }

type ResolvedTarget struct {
	Ref         TargetRef
	OwnerUserID uuid.UUID
}

type ConversationAccess struct {
	Conversation   model.DMConversation
	ChannelOwnerID uuid.UUID
}

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

func (r *Repo) Transaction(fn func(*Repo) error) error {
	return r.db.Transaction(func(tx *gorm.DB) error { return fn(NewRepo(tx)) })
}

func (r *Repo) ResolveTarget(target TargetRef) (ResolvedTarget, error) {
	switch target.Type {
	case model.DMPartyUser:
		var user model.User
		if err := r.db.First(&user, "uuid = ?", target.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ResolvedTarget{}, ErrTargetNotFound
			}
			return ResolvedTarget{}, err
		}
		return ResolvedTarget{Ref: target, OwnerUserID: user.UUID}, nil
	case model.DMPartyChannel:
		var channel model.Channel
		if err := r.db.First(&channel, "id = ?", target.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ResolvedTarget{}, ErrTargetNotFound
			}
			return ResolvedTarget{}, err
		}
		if channel.UserID == nil {
			return ResolvedTarget{}, ErrTargetNotFound
		}
		return ResolvedTarget{Ref: target, OwnerUserID: *channel.UserID}, nil
	default:
		return ResolvedTarget{}, ErrTargetNotFound
	}
}

func (r *Repo) FindConversation(first, second TargetRef) (model.DMConversation, error) {
	first, second, err := NormalizeParties(first, second)
	if err != nil {
		return model.DMConversation{}, err
	}
	var conversation model.DMConversation
	err = r.db.Where("participant_a_type = ? AND participant_a = ? AND participant_b_type = ? AND participant_b = ?", first.Type, first.ID, second.Type, second.ID).First(&conversation).Error
	return conversation, err
}

func (r *Repo) FindOrCreateConversation(first, second TargetRef) (model.DMConversation, error) {
	if conversation, err := r.FindConversation(first, second); err == nil {
		return conversation, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.DMConversation{}, err
	}
	first, second, err := NormalizeParties(first, second)
	if err != nil {
		return model.DMConversation{}, err
	}
	conversation := model.DMConversation{ParticipantAType: first.Type, ParticipantA: first.ID, ParticipantBType: second.Type, ParticipantB: second.ID}
	result := r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&conversation)
	if result.Error != nil {
		return model.DMConversation{}, result.Error
	}
	if result.RowsAffected == 0 {
		return r.FindConversation(first, second)
	}
	return conversation, nil
}

func (r *Repo) GetConversationForActor(conversationID, actorUserID uuid.UUID) (ConversationAccess, error) {
	var conversation model.DMConversation
	query := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&conversation, "id = ?", conversationID)
	if query.Error != nil {
		if errors.Is(query.Error, gorm.ErrRecordNotFound) {
			return ConversationAccess{}, ErrConversationForbidden
		}
		return ConversationAccess{}, query.Error
	}
	access := ConversationAccess{Conversation: conversation}
	if conversation.ParticipantBType == model.DMPartyChannel {
		resolved, err := r.ResolveTarget(TargetRef{Type: model.DMPartyChannel, ID: conversation.ParticipantB})
		if err != nil {
			return ConversationAccess{}, err
		}
		access.ChannelOwnerID = resolved.OwnerUserID
	}
	if _, err := ResolveSender(actorUserID, conversation, access.ChannelOwnerID); err != nil {
		return ConversationAccess{}, err
	}
	return access, nil
}

func (r *Repo) CreateMessage(message *model.DMMessage) (model.DMMessage, bool, error) {
	result := r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(message)
	if result.Error != nil {
		return model.DMMessage{}, false, result.Error
	}
	if result.RowsAffected > 0 {
		return *message, true, nil
	}
	existing, err := r.FindMessageByClientID(message.ActorUserID, message.ClientMessageID)
	if err != nil {
		return model.DMMessage{}, false, err
	}
	return existing, false, nil
}

func (r *Repo) RecipientPermission(recipient TargetRef) (string, error) {
	switch recipient.Type {
	case model.DMPartyUser:
		settings := model.UserSettings{UserID: recipient.ID}
		if err := r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&settings).Error; err != nil {
			return "", err
		}
		if err := r.db.First(&settings, "user_id = ?", recipient.ID).Error; err != nil {
			return "", err
		}
		if settings.DMPermission == "" {
			return model.DMPermissionOneBeforeReply, nil
		}
		return settings.DMPermission, nil
	case model.DMPartyChannel:
		settings := model.DMChannelSettings{ChannelID: recipient.ID}
		if err := r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&settings).Error; err != nil {
			return "", err
		}
		if err := r.db.First(&settings, "channel_id = ?", recipient.ID).Error; err != nil {
			return "", err
		}
		if settings.Permission == "" {
			return model.DMPermissionOneBeforeReply, nil
		}
		return settings.Permission, nil
	default:
		return "", ErrTargetNotFound
	}
}

func (r *Repo) IsFollowing(followerID, followingID uuid.UUID) (bool, error) {
	var count int64
	err := r.db.Model(&model.Follow{}).Where("follower_id = ? AND following_id = ?", followerID, followingID).Count(&count).Error
	return count > 0, err
}

func (r *Repo) IsBlocked(firstUserID, secondUserID uuid.UUID) (bool, error) {
	var count int64
	err := r.db.Model(&model.UserBlock{}).Where("(blocker_id = ? AND blocked_id = ?) OR (blocker_id = ? AND blocked_id = ?)", firstUserID, secondUserID, secondUserID, firstUserID).Count(&count).Error
	return count > 0, err
}

func (r *Repo) CountMessagesFromSender(conversationID uuid.UUID, sender SenderIdentity) (int64, error) {
	var count int64
	err := r.db.Model(&model.DMMessage{}).Where("conversation_id = ? AND sender_type = ? AND sender_id = ?", conversationID, sender.SenderType, sender.SenderID).Count(&count).Error
	return count, err
}

func (r *Repo) CountActorMessagesSince(actorID uuid.UUID, since time.Time) (int64, error) {
	var count int64
	err := r.db.Model(&model.DMMessage{}).Where("actor_user_id = ? AND created_at >= ?", actorID, since).Count(&count).Error
	return count, err
}

func (r *Repo) CountActorInitiatedTargetsSince(actorID uuid.UUID, since time.Time) (int64, error) {
	var count int64
	err := r.db.Raw(`
		SELECT COUNT(*)
		FROM dm_conversations conversation
		WHERE EXISTS (
			SELECT 1 FROM dm_messages message
			WHERE message.conversation_id = conversation.id
				AND message.actor_user_id = ?
				AND message.created_at >= ?
				AND NOT EXISTS (
					SELECT 1 FROM dm_messages earlier
					WHERE earlier.conversation_id = message.conversation_id
						AND (earlier.created_at < message.created_at OR (earlier.created_at = message.created_at AND earlier.id < message.id))
				)
		)
	`, actorID, since).Scan(&count).Error
	return count, err
}

func (r *Repo) LockActor(actorID uuid.UUID) error {
	var user model.User
	return r.db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, "uuid = ?", actorID).Error
}

func (r *Repo) BlockUser(blockerID, blockedID uuid.UUID) error {
	return r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.UserBlock{BlockerID: blockerID, BlockedID: blockedID}).Error
}

func (r *Repo) UnblockUser(blockerID, blockedID uuid.UUID) error {
	return r.db.Where("blocker_id = ? AND blocked_id = ?", blockerID, blockedID).Delete(&model.UserBlock{}).Error
}

func (r *Repo) OtherUserForConversation(access ConversationAccess, actorID uuid.UUID) (uuid.UUID, error) {
	conversation := access.Conversation
	if conversation.ParticipantBType == model.DMPartyChannel {
		if actorID == conversation.ParticipantA {
			return access.ChannelOwnerID, nil
		}
		if actorID == access.ChannelOwnerID {
			return conversation.ParticipantA, nil
		}
		return uuid.Nil, ErrConversationForbidden
	}
	if actorID == conversation.ParticipantA {
		return conversation.ParticipantB, nil
	}
	if actorID == conversation.ParticipantB {
		return conversation.ParticipantA, nil
	}
	return uuid.Nil, ErrConversationForbidden
}

func (r *Repo) IsConversationBlockedForActor(conversation model.DMConversation, actorID uuid.UUID) (bool, error) {
	access := ConversationAccess{Conversation: conversation}
	if conversation.ParticipantBType == model.DMPartyChannel {
		resolved, err := r.ResolveTarget(TargetRef{Type: model.DMPartyChannel, ID: conversation.ParticipantB})
		if err != nil {
			return false, err
		}
		access.ChannelOwnerID = resolved.OwnerUserID
	}
	otherUserID, err := r.OtherUserForConversation(access, actorID)
	if err != nil {
		return false, err
	}
	return r.IsBlocked(actorID, otherUserID)
}

func (r *Repo) FindMessageByClientID(actorUserID, clientMessageID uuid.UUID) (model.DMMessage, error) {
	var message model.DMMessage
	err := r.db.Where("actor_user_id = ? AND client_message_id = ?", actorUserID, clientMessageID).First(&message).Error
	return message, err
}

func (r *Repo) UpdateConversationPreview(conversationID uuid.UUID, preview string) error {
	return r.db.Model(&model.DMConversation{}).Where("id = ?", conversationID).Updates(map[string]any{"last_message_at": gorm.Expr("CURRENT_TIMESTAMP"), "last_message_preview": preview}).Error
}

func (r *Repo) ListOwnedChannels(userID uuid.UUID) ([]model.Channel, error) {
	var channels []model.Channel
	err := r.db.Where("user_id = ?", userID).Order("name ASC, id ASC").Find(&channels).Error
	return channels, err
}

func (r *Repo) AuthorizeMailbox(actorID uuid.UUID, mailbox TargetRef) error {
	switch mailbox.Type {
	case model.DMPartyUser:
		if mailbox.ID != actorID {
			return ErrConversationForbidden
		}
		return nil
	case model.DMPartyChannel:
		var channel model.Channel
		if err := r.db.Where("id = ? AND user_id = ?", mailbox.ID, actorID).First(&channel).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrConversationForbidden
			}
			return err
		}
		return nil
	default:
		return ErrConversationForbidden
	}
}

func (r *Repo) ListMailboxConversations(actorID uuid.UUID, mailbox TargetRef, cursor *Cursor, limit int) ([]model.DMConversation, error) {
	db := r.db.Model(&model.DMConversation{})
	if mailbox.Type == model.DMPartyUser {
		db = db.Where("(participant_b_type = ? AND (participant_a = ? OR participant_b = ?)) OR (participant_b_type = ? AND participant_a = ?)", model.DMPartyUser, actorID, actorID, model.DMPartyChannel, actorID)
	} else {
		db = db.Where("participant_b_type = ? AND participant_b = ?", model.DMPartyChannel, mailbox.ID)
	}
	if cursor != nil {
		if cursor.Null {
			db = db.Where("last_message_at IS NULL AND id < ?", cursor.ID)
		} else {
			db = db.Where("last_message_at IS NULL OR (last_message_at < ?) OR (last_message_at = ? AND id < ?)", cursor.At, cursor.At, cursor.ID)
		}
	}
	var conversations []model.DMConversation
	err := db.Order("last_message_at DESC NULLS LAST, id DESC").Limit(limit + 1).Find(&conversations).Error
	return conversations, err
}

func (r *Repo) ListMessages(conversationID uuid.UUID, before *Cursor, limit int) ([]model.DMMessage, error) {
	db := r.db.Where("conversation_id = ?", conversationID)
	if before != nil {
		db = db.Where("(created_at < ?) OR (created_at = ? AND id < ?)", before.At, before.At, before.ID)
	}
	var messages []model.DMMessage
	err := db.Order("created_at DESC, id DESC").Limit(limit + 1).Find(&messages).Error
	return messages, err
}

func (r *Repo) CountUnreadForConversation(actorID uuid.UUID, conversation model.DMConversation) (int64, error) {
	db := r.db.Model(&model.DMMessage{}).Where("conversation_id = ? AND read_at IS NULL", conversation.ID)
	if conversation.ParticipantBType == model.DMPartyChannel {
		if conversation.ParticipantA == actorID {
			db = db.Where("sender_type = ?", model.DMPartyChannel)
		} else {
			db = db.Where("sender_type = ?", model.DMPartyUser)
		}
	} else {
		db = db.Where("sender_type = ? AND sender_id != ?", model.DMPartyUser, actorID)
	}
	var count int64
	return count, db.Count(&count).Error
}

func (r *Repo) CountUnreadForMailbox(actorID uuid.UUID, mailbox TargetRef) (int64, error) {
	conversations, err := r.ListMailboxConversations(actorID, mailbox, nil, 1000000)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, conversation := range conversations {
		count, err := r.CountUnreadForConversation(actorID, conversation)
		if err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

func (r *Repo) CountUnreadDM(actorID uuid.UUID) (int64, error) {
	mailboxes, err := r.ListOwnedChannels(actorID)
	if err != nil {
		return 0, err
	}
	total, err := r.CountUnreadForMailbox(actorID, TargetRef{Type: model.DMPartyUser, ID: actorID})
	if err != nil {
		return 0, err
	}
	for _, mailbox := range mailboxes {
		count, err := r.CountUnreadForMailbox(actorID, TargetRef{Type: model.DMPartyChannel, ID: mailbox.ID})
		if err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

func (r *Repo) MarkConversationRead(actorID uuid.UUID, conversation model.DMConversation, readAt time.Time) error {
	db := r.db.Model(&model.DMMessage{}).Where("conversation_id = ? AND read_at IS NULL", conversation.ID)
	if conversation.ParticipantBType == model.DMPartyChannel {
		if conversation.ParticipantA == actorID {
			db = db.Where("sender_type = ?", model.DMPartyChannel)
		} else {
			db = db.Where("sender_type = ?", model.DMPartyUser)
		}
	} else {
		db = db.Where("sender_type = ? AND sender_id != ?", model.DMPartyUser, actorID)
	}
	return db.Update("read_at", readAt).Error
}
