package dm

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ImageStore interface {
	GetImage(ctx context.Context, actorUserID, imageID uuid.UUID) (model.DMImage, error)
}

type Publisher interface {
	PublishDM(ctx context.Context, message MessageDTO) error
}

type SiteUnreadCounter interface {
	IncrementDM(ctx context.Context, userID uuid.UUID) error
}

type Service struct {
	repo    *Repo
	images  ImageStore
	publish Publisher
	unread  SiteUnreadCounter
}

func NewService(repo *Repo, images ImageStore, publisher Publisher, unread SiteUnreadCounter) *Service {
	return &Service{repo: repo, images: images, publish: publisher, unread: unread}
}

func (s *Service) SendToTarget(ctx context.Context, actorUserID uuid.UUID, target TargetRef, input SendInput) (MessageDTO, error) {
	var result MessageDTO
	err := s.repo.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repo := NewRepo(tx)
		if existing, err := repo.FindMessageByClientID(actorUserID, input.ClientMessageID); err == nil {
			result = messageDTO(existing)
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		resolved, err := repo.ResolveTarget(target)
		if err != nil {
			return err
		}
		if resolved.OwnerUserID == actorUserID {
			return ErrSelfTarget
		}
		first, second, err := NormalizeParties(TargetRef{Type: model.DMPartyUser, ID: actorUserID}, resolved.Ref)
		if err != nil {
			return err
		}
		conversation, err := repo.FindOrCreateConversation(first, second)
		if err != nil {
			return err
		}
		access, err := repo.GetConversationForActor(conversation.ID, actorUserID)
		if err != nil {
			return err
		}
		return s.createMessage(repo, access.Conversation, SenderIdentity{SenderType: model.DMPartyUser, SenderID: actorUserID, ActorUserID: actorUserID}, input, &result)
	})
	return result, err
}

func (s *Service) SendInConversation(ctx context.Context, actorUserID, conversationID uuid.UUID, input SendInput) (MessageDTO, error) {
	var result MessageDTO
	err := s.repo.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repo := NewRepo(tx)
		if existing, err := repo.FindMessageByClientID(actorUserID, input.ClientMessageID); err == nil {
			result = messageDTO(existing)
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		access, err := repo.GetConversationForActor(conversationID, actorUserID)
		if err != nil {
			return err
		}
		sender, err := ResolveSender(actorUserID, access.Conversation, access.ChannelOwnerID)
		if err != nil {
			return err
		}
		return s.createMessage(repo, access.Conversation, sender, input, &result)
	})
	return result, err
}

func (s *Service) GetTargetConversation(ctx context.Context, actorUserID uuid.UUID, target TargetRef) (ConversationDTO, error) {
	repo := NewRepo(s.repo.db.WithContext(ctx))
	resolved, err := repo.ResolveTarget(target)
	if err != nil {
		return ConversationDTO{}, err
	}
	if resolved.OwnerUserID == actorUserID {
		return ConversationDTO{}, ErrSelfTarget
	}
	conversation, err := repo.FindConversation(TargetRef{Type: model.DMPartyUser, ID: actorUserID}, resolved.Ref)
	if err != nil {
		return ConversationDTO{}, err
	}
	return conversationDTO(conversation), nil
}

func (s *Service) createMessage(repo *Repo, conversation model.DMConversation, sender SenderIdentity, input SendInput, result *MessageDTO) error {
	content := strings.TrimSpace(input.Content)
	imageURL := strings.TrimSpace(input.ImageURL)
	if utf8.RuneCountInString(content) > 4000 || (content == "" && input.ImageID == nil && imageURL == "") {
		return ErrImageInvalid
	}
	message := model.DMMessage{ConversationID: conversation.ID, SenderType: sender.SenderType, SenderID: sender.SenderID, ActorUserID: sender.ActorUserID, ClientMessageID: input.ClientMessageID, Content: content, ImageID: input.ImageID, ImageURL: imageURL}
	stored, created, err := repo.CreateMessage(&message)
	if err != nil {
		return err
	}
	if !created {
		*result = messageDTO(stored)
		return nil
	}
	preview := content
	if preview == "" {
		preview = "[图片]"
	}
	if err := repo.UpdateConversationPreview(conversation.ID, truncateRunes(preview, 100)); err != nil {
		return err
	}
	*result = messageDTO(stored)
	return nil
}

func messageDTO(message model.DMMessage) MessageDTO {
	return MessageDTO{ID: message.ID, ConversationID: message.ConversationID, SenderType: message.SenderType, SenderID: message.SenderID, ClientMessageID: message.ClientMessageID, Content: message.Content, ImageID: message.ImageID, ImageURL: message.ImageURL, CreatedAt: message.CreatedAt}
}
func conversationDTO(conversation model.DMConversation) ConversationDTO {
	return ConversationDTO{ID: conversation.ID, ParticipantA: PartyDTO{Type: conversation.ParticipantAType, ID: conversation.ParticipantA}, ParticipantB: PartyDTO{Type: conversation.ParticipantBType, ID: conversation.ParticipantB}, LastMessageAt: conversation.LastMessageAt, LastMessagePreview: conversation.LastMessagePreview}
}
func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}
