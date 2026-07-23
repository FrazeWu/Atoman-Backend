package dm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ImageStore interface {
	Put(ctx context.Context, objectKey, contentType string, body io.Reader, size int64) error
	Open(ctx context.Context, objectKey string) (io.ReadCloser, error)
	SignedURL(ctx context.Context, objectKey string, ttl time.Duration) (string, error)
	Delete(ctx context.Context, objectKey string) error
	IsLocal() bool
}

type Publisher interface {
	PublishDM(ctx context.Context, message MessageDTO) error
}

type SiteUnreadCounter interface {
	CountSiteUnread(userID uuid.UUID) (int64, error)
}

func (s *Service) ListMailboxes(ctx context.Context, actor authctx.CurrentUser) ([]MailboxDTO, error) {
	if actor.ID == uuid.Nil {
		return nil, ErrConversationForbidden
	}
	repo := NewRepo(s.repo.db.WithContext(ctx))
	userUnread, err := repo.CountUnreadForMailbox(actor.ID, TargetRef{Type: model.DMPartyUser, ID: actor.ID})
	if err != nil {
		return nil, err
	}
	mailboxes := []MailboxDTO{{Party: PartyDTO{Type: model.DMPartyUser, ID: actor.ID, Name: actor.Username}, Unread: userUnread}}
	channels, err := repo.ListOwnedChannels(actor.ID)
	if err != nil {
		return nil, err
	}
	for _, channel := range channels {
		unread, err := repo.CountUnreadForMailbox(actor.ID, TargetRef{Type: model.DMPartyChannel, ID: channel.ID})
		if err != nil {
			return nil, err
		}
		mailboxes = append(mailboxes, MailboxDTO{Party: PartyDTO{Type: model.DMPartyChannel, ID: channel.ID, Name: channel.Name, AvatarURL: channel.CoverURL}, Unread: unread})
	}
	return mailboxes, nil
}

func (s *Service) ListConversations(ctx context.Context, actor authctx.CurrentUser, mailbox TargetRef, encodedCursor string, limit int) (PageDTO[ConversationDTO], error) {
	repo := NewRepo(s.repo.db.WithContext(ctx))
	if err := repo.AuthorizeMailbox(actor.ID, mailbox); err != nil {
		return PageDTO[ConversationDTO]{}, err
	}
	var cursor *Cursor
	if encodedCursor != "" {
		value, err := decodeCursor(encodedCursor)
		if err != nil {
			return PageDTO[ConversationDTO]{}, err
		}
		cursor = &value
	}
	conversations, err := repo.ListMailboxConversations(actor.ID, mailbox, cursor, normalizeLimit(limit))
	if err != nil {
		return PageDTO[ConversationDTO]{}, err
	}
	page := PageDTO[ConversationDTO]{Items: make([]ConversationDTO, 0, len(conversations))}
	if len(conversations) > normalizeLimit(limit) {
		conversations = conversations[:normalizeLimit(limit)]
		last := conversations[len(conversations)-1]
		if last.LastMessageAt != nil {
			page.NextCursor = encodeCursor(Cursor{At: *last.LastMessageAt, ID: last.ID})
		} else {
			page.NextCursor = encodeCursor(Cursor{ID: last.ID, Null: true})
		}
	}
	for _, conversation := range conversations {
		unread, err := repo.CountUnreadForConversation(actor.ID, conversation)
		if err != nil {
			return PageDTO[ConversationDTO]{}, err
		}
		blocked, err := repo.IsConversationBlockedForActor(conversation, actor.ID)
		if err != nil {
			return PageDTO[ConversationDTO]{}, err
		}
		dto := conversationDTO(conversation)
		dto.Unread = unread
		dto.Blocked = blocked
		page.Items = append(page.Items, dto)
	}
	return page, nil
}

func (s *Service) ListMessages(ctx context.Context, actor authctx.CurrentUser, conversationID uuid.UUID, encodedBefore string, limit int) (PageDTO[MessageDTO], error) {
	repo := NewRepo(s.repo.db.WithContext(ctx))
	if _, err := repo.GetConversationForActor(conversationID, actor.ID); err != nil {
		return PageDTO[MessageDTO]{}, err
	}
	var before *Cursor
	if encodedBefore != "" {
		value, err := decodeCursor(encodedBefore)
		if err != nil {
			return PageDTO[MessageDTO]{}, err
		}
		before = &value
	}
	limit = normalizeLimit(limit)
	messages, err := repo.ListMessages(conversationID, before, limit)
	if err != nil {
		return PageDTO[MessageDTO]{}, err
	}
	page := PageDTO[MessageDTO]{Items: make([]MessageDTO, 0, len(messages))}
	if len(messages) > limit {
		messages = messages[:limit]
		last := messages[len(messages)-1]
		page.NextCursor = encodeCursor(Cursor{At: last.CreatedAt, ID: last.ID})
	}
	for index := len(messages) - 1; index >= 0; index-- {
		dto, err := s.messageDTO(ctx, messages[index])
		if err != nil {
			return PageDTO[MessageDTO]{}, err
		}
		page.Items = append(page.Items, dto)
	}
	return page, nil
}

func (s *Service) MarkRead(ctx context.Context, actor authctx.CurrentUser, conversationID uuid.UUID) (ReadResultDTO, error) {
	repo := NewRepo(s.repo.db.WithContext(ctx))
	access, err := repo.GetConversationForActor(conversationID, actor.ID)
	if err != nil {
		return ReadResultDTO{}, err
	}
	if err := repo.MarkConversationRead(actor.ID, access.Conversation, time.Now()); err != nil {
		return ReadResultDTO{}, err
	}
	conversationUnread, err := repo.CountUnreadForConversation(actor.ID, access.Conversation)
	if err != nil {
		return ReadResultDTO{}, err
	}
	mailbox := TargetRef{Type: model.DMPartyUser, ID: actor.ID}
	if access.Conversation.ParticipantBType == model.DMPartyChannel && access.Conversation.ParticipantA != actor.ID {
		mailbox = TargetRef{Type: model.DMPartyChannel, ID: access.Conversation.ParticipantB}
	}
	mailboxUnread, err := repo.CountUnreadForMailbox(actor.ID, mailbox)
	if err != nil {
		return ReadResultDTO{}, err
	}
	dmUnread, err := repo.CountUnreadDM(actor.ID)
	if err != nil {
		return ReadResultDTO{}, err
	}
	totalUnread := dmUnread
	if s.unread != nil {
		totalUnread, err = s.unread.CountSiteUnread(actor.ID)
		if err != nil {
			return ReadResultDTO{}, err
		}
	}
	return ReadResultDTO{ConversationUnread: conversationUnread, MailboxUnread: mailboxUnread, DMUnread: dmUnread, TotalUnread: totalUnread}, nil
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
		sender := SenderIdentity{SenderType: model.DMPartyUser, SenderID: actorUserID, ActorUserID: actorUserID}
		return s.sendWithPolicy(repo, access, sender, input, &result)
	})
	if err != nil {
		return result, err
	}
	return s.messageDTO(ctx, model.DMMessage{Base: model.Base{ID: result.ID, CreatedAt: result.CreatedAt}, ConversationID: result.ConversationID, SenderType: result.SenderType, SenderID: result.SenderID, ClientMessageID: result.ClientMessageID, Content: result.Content, ImageID: result.ImageID})
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
		return s.sendWithPolicy(repo, access, sender, input, &result)
	})
	if err != nil {
		return result, err
	}
	return s.messageDTO(ctx, model.DMMessage{Base: model.Base{ID: result.ID, CreatedAt: result.CreatedAt}, ConversationID: result.ConversationID, SenderType: result.SenderType, SenderID: result.SenderID, ClientMessageID: result.ClientMessageID, Content: result.Content, ImageID: result.ImageID})
}

func (s *Service) BlockConversation(ctx context.Context, actor authctx.CurrentUser, conversationID uuid.UUID) (ConversationDTO, error) {
	var result ConversationDTO
	err := s.repo.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repo := NewRepo(tx)
		access, err := repo.GetConversationForActor(conversationID, actor.ID)
		if err != nil {
			return err
		}
		otherUserID, err := repo.OtherUserForConversation(access, actor.ID)
		if err != nil {
			return err
		}
		if err := repo.BlockUser(actor.ID, otherUserID); err != nil {
			return err
		}
		blocked, err := repo.IsConversationBlockedForActor(access.Conversation, actor.ID)
		if err != nil {
			return err
		}
		result = conversationDTO(access.Conversation)
		result.Blocked = blocked
		return nil
	})
	return result, err
}

func (s *Service) UnblockConversation(ctx context.Context, actor authctx.CurrentUser, conversationID uuid.UUID) (ConversationDTO, error) {
	var result ConversationDTO
	err := s.repo.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repo := NewRepo(tx)
		access, err := repo.GetConversationForActor(conversationID, actor.ID)
		if err != nil {
			return err
		}
		otherUserID, err := repo.OtherUserForConversation(access, actor.ID)
		if err != nil {
			return err
		}
		if err := repo.UnblockUser(actor.ID, otherUserID); err != nil {
			return err
		}
		blocked, err := repo.IsConversationBlockedForActor(access.Conversation, actor.ID)
		if err != nil {
			return err
		}
		result = conversationDTO(access.Conversation)
		result.Blocked = blocked
		return nil
	})
	return result, err
}

func (s *Service) sendWithPolicy(repo *Repo, access ConversationAccess, sender SenderIdentity, input SendInput, result *MessageDTO) error {
	if err := repo.LockActor(sender.ActorUserID); err != nil {
		return err
	}
	if existing, err := repo.FindMessageByClientID(sender.ActorUserID, input.ClientMessageID); err == nil {
		*result = messageDTO(existing)
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	recipient, err := RecipientForSender(access.Conversation, sender)
	if err != nil {
		return err
	}
	recipientTarget, err := repo.ResolveTarget(recipient)
	if err != nil {
		return err
	}
	blocked, err := repo.IsBlocked(sender.ActorUserID, recipientTarget.OwnerUserID)
	if err != nil {
		return err
	}
	if blocked {
		return ErrBlocked
	}
	permission, err := repo.RecipientPermission(recipient)
	if err != nil {
		return err
	}
	if permission == model.DMPermissionClosed {
		return ErrPermissionDenied
	}
	if permission == model.DMPermissionFollowingOnly && sender.SenderType == model.DMPartyUser && recipient.Type == model.DMPartyUser {
		following, err := repo.IsFollowing(recipient.ID, sender.ActorUserID)
		if err != nil {
			return err
		}
		if !following {
			return ErrPermissionDenied
		}
	}
	if permission == model.DMPermissionOneBeforeReply {
		senderCount, err := repo.CountMessagesFromSender(access.Conversation.ID, sender)
		if err != nil {
			return err
		}
		recipientCount, err := repo.CountMessagesFromSender(access.Conversation.ID, SenderIdentity{SenderType: recipient.Type, SenderID: recipient.ID})
		if err != nil {
			return err
		}
		if senderCount > 0 && recipientCount == 0 {
			return ErrWaitingReply
		}
	}
	messages, err := repo.CountActorMessagesSince(sender.ActorUserID, time.Now().Add(-time.Minute))
	if err != nil {
		return err
	}
	if messages >= 30 {
		return ErrRateLimited
	}
	senderCount, err := repo.CountMessagesFromSender(access.Conversation.ID, sender)
	if err != nil {
		return err
	}
	if senderCount == 0 {
		targets, err := repo.CountActorInitiatedTargetsSince(sender.ActorUserID, time.Now().Add(-time.Hour))
		if err != nil {
			return err
		}
		if targets >= 10 {
			return ErrRateLimited
		}
	}
	return s.createMessage(repo, access.Conversation, sender, input, result)
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

const maxDMImageSize = 10 * 1024 * 1024

var allowedDMImageTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

type ImageDTO struct {
	ID  uuid.UUID `json:"id"`
	URL string    `json:"url"`
}

func (s *Service) UploadImage(ctx context.Context, actor authctx.CurrentUser, body io.Reader, declaredType string, declaredSize int64) (ImageDTO, error) {
	if s.images == nil || actor.ID == uuid.Nil || declaredSize <= 0 || declaredSize > maxDMImageSize {
		return ImageDTO{}, ErrImageInvalid
	}
	declaredType = strings.TrimSpace(declaredType)
	ext, ok := allowedDMImageTypes[declaredType]
	if !ok {
		return ImageDTO{}, ErrImageInvalid
	}
	data, err := io.ReadAll(io.LimitReader(body, maxDMImageSize+1))
	if err != nil || len(data) == 0 || len(data) > maxDMImageSize || int64(len(data)) != declaredSize || http.DetectContentType(data) != declaredType {
		return ImageDTO{}, ErrImageInvalid
	}
	image := model.DMImage{UploadedByUserID: actor.ID, ContentType: declaredType, SizeBytes: int64(len(data))}
	image.ID = uuid.New()
	image.ObjectKey = path.Join("images", actor.ID.String(), image.ID.String()+ext)
	if err := s.images.Put(ctx, image.ObjectKey, image.ContentType, bytes.NewReader(data), image.SizeBytes); err != nil {
		return ImageDTO{}, err
	}
	if err := s.repo.db.WithContext(ctx).Create(&image).Error; err != nil {
		if cleanupErr := s.images.Delete(ctx, image.ObjectKey); cleanupErr != nil {
			return ImageDTO{}, fmt.Errorf("%w; cleanup private image: %w", err, cleanupErr)
		}
		return ImageDTO{}, err
	}
	return s.imageDTO(ctx, image)
}

func (s *Service) OpenImage(ctx context.Context, actor authctx.CurrentUser, imageID uuid.UUID) (io.ReadCloser, string, error) {
	if s.images == nil || actor.ID == uuid.Nil {
		return nil, "", ErrConversationForbidden
	}
	image, err := s.repo.FindReadableImage(actor.ID, imageID)
	if err != nil {
		return nil, "", err
	}
	body, err := s.images.Open(ctx, image.ObjectKey)
	if err != nil {
		return nil, "", err
	}
	return body, image.ContentType, nil
}

func (s *Service) imageDTO(ctx context.Context, image model.DMImage) (ImageDTO, error) {
	if s.images.IsLocal() {
		return ImageDTO{ID: image.ID, URL: "/api/v1/dm/images/" + image.ID.String() + "/content"}, nil
	}
	url, err := s.images.SignedURL(ctx, image.ObjectKey, 5*time.Minute)
	if err != nil {
		return ImageDTO{}, err
	}
	return ImageDTO{ID: image.ID, URL: url}, nil
}

func (s *Service) createMessage(repo *Repo, conversation model.DMConversation, sender SenderIdentity, input SendInput, result *MessageDTO) error {
	content := strings.TrimSpace(input.Content)
	if utf8.RuneCountInString(content) > 4000 || (content == "" && input.ImageID == nil) || strings.TrimSpace(input.ImageURL) != "" {
		return ErrImageInvalid
	}
	if input.ImageID != nil {
		if _, err := repo.LockUsableImage(sender.ActorUserID, *input.ImageID); err != nil {
			return err
		}
	}
	message := model.DMMessage{ConversationID: conversation.ID, SenderType: sender.SenderType, SenderID: sender.SenderID, ActorUserID: sender.ActorUserID, ClientMessageID: input.ClientMessageID, Content: content, ImageID: input.ImageID}
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
	return MessageDTO{ID: message.ID, ConversationID: message.ConversationID, SenderType: message.SenderType, SenderID: message.SenderID, ClientMessageID: message.ClientMessageID, Content: message.Content, ImageID: message.ImageID, CreatedAt: message.CreatedAt}
}

func (s *Service) messageDTO(ctx context.Context, message model.DMMessage) (MessageDTO, error) {
	dto := messageDTO(message)
	if message.ImageID == nil {
		return dto, nil
	}
	var image model.DMImage
	if err := s.repo.db.WithContext(ctx).First(&image, "id = ?", *message.ImageID).Error; err != nil {
		return MessageDTO{}, err
	}
	imageDTO, err := s.imageDTO(ctx, image)
	if err != nil {
		return MessageDTO{}, err
	}
	dto.ImageURL = imageDTO.URL
	return dto, nil
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
