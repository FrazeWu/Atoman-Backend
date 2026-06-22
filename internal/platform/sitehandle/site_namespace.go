package sitehandle

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrInvalid  = errors.New("site handle is invalid")
	ErrReserved = errors.New("site handle is reserved")
	ErrTaken    = errors.New("site handle is already in use")
)

var handlePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,28}[a-z0-9]$`)

// New public website features should reserve their handle here before launch.
var reservedHandles = map[string]struct{}{
	"feed": {}, "media": {}, "kanbo": {}, "music": {}, "blog": {}, "forum": {},
	"debate": {}, "timeline": {}, "podcast": {}, "video": {},
	"www": {}, "api": {}, "admin": {}, "root": {}, "portal": {},
	"auth": {}, "login": {}, "logout": {}, "register": {}, "signup": {}, "signin": {},
	"account": {}, "accounts": {}, "setting": {}, "settings": {}, "profile": {}, "profiles": {},
	"user": {}, "users": {}, "member": {}, "members": {}, "channel": {}, "channels": {},
	"post": {}, "posts": {}, "article": {}, "articles": {}, "collection": {}, "collections": {},
	"topic": {}, "topics": {}, "comment": {}, "comments": {}, "bookmark": {}, "bookmarks": {},
	"notification": {}, "notifications": {}, "inbox": {}, "dm": {}, "message": {}, "messages": {},
	"search": {}, "explore": {}, "discover": {}, "upload": {}, "uploads": {},
	"song": {}, "songs": {}, "album": {}, "albums": {}, "artist": {}, "artists": {},
	"playlist": {}, "playlists": {}, "watch": {}, "episode": {}, "episodes": {},
	"subscription": {}, "subscriptions": {}, "rss": {}, "atom": {}, "feed-source": {},
	"help": {}, "about": {}, "terms": {}, "privacy": {}, "contact": {}, "support": {},
	"static": {}, "assets": {}, "cdn": {}, "status": {}, "health": {}, "metrics": {},
	"dev": {}, "test": {}, "stage": {}, "staging": {}, "prod": {}, "production": {},
}

type Resolution struct {
	Type     string `json:"type"`
	Handle   string `json:"handle"`
	Module   string `json:"module,omitempty"`
	Username string `json:"username,omitempty"`
	Slug     string `json:"slug,omitempty"`
}

type Service struct {
	db *gorm.DB
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

func Normalize(raw string) (string, error) {
	handle := strings.ToLower(strings.TrimSpace(raw))
	if !handlePattern.MatchString(handle) {
		return "", ErrInvalid
	}
	return handle, nil
}

func (s *Service) NormalizeHandle(raw string) (string, error) {
	return Normalize(raw)
}

func (s *Service) Resolve(ctx context.Context, raw string) (Resolution, error) {
	handle, err := Normalize(raw)
	if err != nil {
		return Resolution{}, err
	}
	if _, ok := reservedHandles[handle]; ok {
		return Resolution{Type: "module", Handle: handle, Module: handle}, nil
	}

	var user model.User
	if err := s.db.WithContext(ctx).Where("LOWER(username) = ?", handle).First(&user).Error; err == nil {
		return Resolution{Type: "user", Handle: handle, Username: user.Username}, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return Resolution{}, err
	}

	var channel model.Channel
	if err := s.db.WithContext(ctx).Where("LOWER(slug) = ?", handle).First(&channel).Error; err == nil {
		return Resolution{Type: "channel", Handle: handle, Slug: channel.Slug}, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return Resolution{}, err
	}

	return Resolution{Type: "unknown", Handle: handle}, nil
}

func (s *Service) ValidateUsernameAvailable(ctx context.Context, username string) error {
	handle, err := Normalize(username)
	if err != nil {
		return err
	}
	if _, ok := reservedHandles[handle]; ok {
		return ErrReserved
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&model.Channel{}).Where("LOWER(slug) = ?", handle).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrTaken
	}
	return nil
}

func (s *Service) ValidateChannelSlugAvailable(ctx context.Context, slug string, excludeChannelID *uuid.UUID) error {
	handle, err := Normalize(slug)
	if err != nil {
		return err
	}
	if _, ok := reservedHandles[handle]; ok {
		return ErrReserved
	}
	var userCount int64
	if err := s.db.WithContext(ctx).Model(&model.User{}).Where("LOWER(username) = ?", handle).Count(&userCount).Error; err != nil {
		return err
	}
	if userCount > 0 {
		return ErrTaken
	}
	query := s.db.WithContext(ctx).Model(&model.Channel{}).Where("LOWER(slug) = ?", handle)
	if excludeChannelID != nil {
		query = query.Where("id <> ?", *excludeChannelID)
	}
	var channelCount int64
	if err := query.Count(&channelCount).Error; err != nil {
		return err
	}
	if channelCount > 0 {
		return ErrTaken
	}
	return nil
}
