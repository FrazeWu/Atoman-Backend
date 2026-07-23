package dm

import (
	"errors"
	"fmt"

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
	if err := r.db.Create(&conversation).Error; err != nil {
		// The typed unique index decides the winner under concurrent creation.
		found, findErr := r.FindConversation(first, second)
		if findErr == nil {
			return found, nil
		}
		return model.DMConversation{}, fmt.Errorf("create conversation: %w", err)
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

func (r *Repo) CreateMessage(message *model.DMMessage) error { return r.db.Create(message).Error }

func (r *Repo) FindMessageByClientID(actorUserID, clientMessageID uuid.UUID) (model.DMMessage, error) {
	var message model.DMMessage
	err := r.db.Where("actor_user_id = ? AND client_message_id = ?", actorUserID, clientMessageID).First(&message).Error
	return message, err
}

func (r *Repo) UpdateConversationPreview(conversationID uuid.UUID, preview string) error {
	return r.db.Model(&model.DMConversation{}).Where("id = ?", conversationID).Updates(map[string]any{"last_message_at": gorm.Expr("CURRENT_TIMESTAMP"), "last_message_preview": preview}).Error
}
