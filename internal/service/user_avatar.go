package service

import (
	"strings"

	"atoman/internal/model"
	"gorm.io/gorm"
)

// ResolveUserAvatarURL keeps older OAuth accounts visible when their avatar
// was stored only on the linked identity.
func ResolveUserAvatarURL(db *gorm.DB, user model.User) string {
	if strings.TrimSpace(user.AvatarURL) != "" {
		return user.AvatarURL
	}
	if db == nil {
		return ""
	}

	var identity model.ExternalIdentity
	if err := db.Where("user_id = ? AND avatar_url <> ''", user.UUID).Order("updated_at DESC").First(&identity).Error; err == nil {
		return identity.AvatarURL
	}
	return ""
}

func (s *OAuthService) ResolveUserAvatarURL(user model.User) string {
	return ResolveUserAvatarURL(s.db, user)
}
