package comment

import (
	"fmt"
	"sort"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) validateAuthor(user authctx.CurrentUser) error {
	if user.ID == uuid.Nil {
		return ErrAuthenticationRequired
	}
	var stored model.User
	if err := s.db.Select("uuid", "is_active").First(&stored, "uuid = ?", user.ID).Error; err != nil || !stored.IsActive {
		return ErrAuthenticationRequired
	}
	return nil
}

func (s *Service) resolveVisible(viewer Viewer, target TargetRef) (ResolvedTarget, error) {
	resolved, err := s.registry.Resolve(viewer, target)
	if err != nil {
		return ResolvedTarget{}, err
	}
	if !resolved.Visible {
		return ResolvedTarget{}, ErrTargetNotVisible
	}
	if resolved.Kind == TargetKindForumTopic && s.forumPolicy != nil {
		if err := s.forumPolicy.CanViewTopic(viewer, resolved.ResourceID); err != nil {
			return ResolvedTarget{}, err
		}
	}
	return resolved, nil
}

func (s *Service) resolveStoredTarget(viewer Viewer, target model.DiscussionTarget) (ResolvedTarget, error) {
	resolved, err := s.resolveVisible(viewer, TargetRef{Kind: target.Kind, ResourceID: target.ResourceID})
	if err != nil {
		return ResolvedTarget{}, err
	}
	if resolved.Kind != target.Kind || resolved.ResourceID != target.ResourceID || resolved.ResourceKey != target.ResourceKey {
		return ResolvedTarget{}, ErrInvalidTargetResource
	}
	return resolved, nil
}

func validateCommentContent(raw string, attachments []uuid.UUID) (string, string, error) {
	normalized := NormalizeContent(raw)
	if normalized == "" && len(attachments) == 0 {
		return "", "", ErrInvalidContent
	}
	if len([]rune(normalized)) > 2000 {
		return "", "", ErrInvalidContent
	}
	rendered, err := RenderCommentMarkdown(normalized)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrInvalidContent, err)
	}
	return normalized, rendered, nil
}

func (s *Service) validateAttachments(db *gorm.DB, userID uuid.UUID, ids []uuid.UUID) ([]model.MediaAsset, error) {
	if len(ids) > 4 {
		return nil, ErrInvalidAttachment
	}
	seen := make(map[uuid.UUID]struct{}, len(ids))
	orderedIDs := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id == uuid.Nil {
			return nil, ErrInvalidAttachment
		}
		if _, exists := seen[id]; exists {
			return nil, ErrInvalidAttachment
		}
		seen[id] = struct{}{}
		orderedIDs = append(orderedIDs, id)
	}
	orderedIDs = sortedUUIDs(orderedIDs)
	assetsByID := make(map[uuid.UUID]model.MediaAsset, len(ids))
	allowed := map[string]bool{"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true}
	const maxAttachmentSize = 10 * 1024 * 1024
	for _, id := range orderedIDs {
		var asset model.MediaAsset
		if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&asset, "id = ?", id).Error; err != nil || asset.UserID == nil || *asset.UserID != userID || asset.Purpose != "comment.image" || !allowed[asset.ContentType] || asset.Size <= 0 || asset.Size > maxAttachmentSize || strings.TrimSpace(asset.Key) == "" || strings.TrimSpace(asset.URL) == "" {
			return nil, ErrInvalidAttachment
		}
		assetsByID[id] = asset
	}
	assets := make([]model.MediaAsset, 0, len(ids))
	for _, id := range ids {
		assets = append(assets, assetsByID[id])
	}
	return assets, nil
}

func (s *Service) validateMentions(db *gorm.DB, content string, mentions []MentionInput) error {
	if err := ValidateMentions(content, mentions); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidMention, err)
	}
	seen := make(map[uuid.UUID]struct{}, len(mentions))
	userIDs := make([]uuid.UUID, 0, len(mentions))
	for _, mention := range mentions {
		if _, exists := seen[mention.UserID]; exists {
			continue
		}
		seen[mention.UserID] = struct{}{}
		userIDs = append(userIDs, mention.UserID)
	}
	userIDs = sortedUUIDs(userIDs)
	var users []model.User
	if len(userIDs) > 0 {
		if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Select("uuid", "username").Where("uuid IN ? AND is_active = ?", userIDs, true).Order("uuid ASC").Find(&users).Error; err != nil {
			return ErrInvalidMention
		}
	}
	usernames := make(map[uuid.UUID]string, len(users))
	for _, user := range users {
		usernames[user.UUID] = user.Username
	}
	runes := []rune(content)
	for _, mention := range mentions {
		username, exists := usernames[mention.UserID]
		if !exists || string(runes[mention.Start:mention.End]) != "@"+username {
			return ErrInvalidMention
		}
	}
	return nil
}

func sortedUUIDs(ids []uuid.UUID) []uuid.UUID {
	result := append([]uuid.UUID(nil), ids...)
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result
}

func isMediaTarget(kind string) bool {
	return kind == TargetKindVideo || kind == TargetKindPodcastEpisode || kind == TargetKindMusicSong
}
