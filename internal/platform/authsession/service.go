package authsession

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	KindWeb                 = "web"
	KindAPI                 = "api"
	TTL                     = 30 * 24 * time.Hour
	MaxActiveSessions       = 10
	lastActiveWriteInterval = 5 * time.Minute
)

var ErrInvalid = errors.New("invalid auth session")

type Credentials struct {
	SessionID uuid.UUID
	Token     string
	CSRFToken string
	ExpiresAt time.Time
}

type Resolved struct {
	Session model.AuthSession
	User    model.User
}

type Metadata struct {
	UserAgent string
	IPAddress string
	IPPrefix  string
}

type Item struct {
	ID           uuid.UUID `json:"id"`
	Kind         string    `json:"kind"`
	DeviceName   string    `json:"device_name"`
	UserAgent    string    `json:"user_agent"`
	IPAddress    string    `json:"ip_address"`
	IPPrefix     string    `json:"ip_prefix"`
	CreatedAt    time.Time `json:"created_at"`
	LastActiveAt time.Time `json:"last_active_at"`
	Current      bool      `json:"current"`
}

type Service struct {
	db  *gorm.DB
	now func() time.Time
}

func New(db *gorm.DB) *Service {
	return &Service{db: db, now: time.Now}
}

func (service *Service) Create(userID uuid.UUID, kind string, metadata ...Metadata) (Credentials, error) {
	var user model.User
	if err := service.db.Select("auth_version").First(&user, "uuid = ?", userID).Error; err != nil {
		return Credentials{}, err
	}
	return service.CreateAtVersion(userID, kind, user.AuthVersion, metadata...)
}

func (service *Service) CreateAtVersion(userID uuid.UUID, kind string, authVersion uint, metadata ...Metadata) (Credentials, error) {
	token, err := randomCredential()
	if err != nil {
		return Credentials{}, err
	}
	csrfToken := ""
	if kind == KindWeb {
		csrfToken, err = randomCredential()
		if err != nil {
			return Credentials{}, err
		}
	}
	expiresAt := service.now().UTC().Add(TTL)
	meta := Metadata{}
	if len(metadata) > 0 {
		meta = metadata[0]
	}
	now := service.now().UTC()
	session := model.AuthSession{
		UserID:       userID,
		AuthVersion:  authVersion,
		TokenHash:    Hash(token),
		CSRFHash:     hashOptional(csrfToken),
		Kind:         kind,
		UserAgent:    truncate(strings.TrimSpace(meta.UserAgent), 512),
		IPAddress:    truncate(strings.TrimSpace(meta.IPAddress), 45),
		IPPrefix:     truncate(strings.TrimSpace(meta.IPPrefix), 64),
		LastActiveAt: now,
		ExpiresAt:    expiresAt,
	}
	if err := service.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&session).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.AuthSession{}).
			Where("user_id = ? AND auth_version <> ? AND revoked_at IS NULL", userID, authVersion).
			Update("revoked_at", &now).Error; err != nil {
			return err
		}
		var excess []model.AuthSession
		if err := tx.Select("id").
			Where("user_id = ? AND auth_version = ? AND revoked_at IS NULL AND expires_at > ?", userID, authVersion, now).
			Order("last_active_at DESC").Order("created_at DESC").Order("id DESC").
			Offset(MaxActiveSessions).Find(&excess).Error; err != nil {
			return err
		}
		if len(excess) == 0 {
			return nil
		}
		ids := make([]uuid.UUID, 0, len(excess))
		for _, item := range excess {
			ids = append(ids, item.ID)
		}
		return tx.Model(&model.AuthSession{}).Where("id IN ?", ids).Update("revoked_at", &now).Error
	}); err != nil {
		return Credentials{}, err
	}
	return Credentials{SessionID: session.ID, Token: token, CSRFToken: csrfToken, ExpiresAt: expiresAt}, nil
}

func (service *Service) Authenticate(token, kind string) (Resolved, error) {
	if token == "" {
		return Resolved{}, ErrInvalid
	}
	var session model.AuthSession
	err := service.db.Where(
		"token_hash = ? AND kind = ? AND revoked_at IS NULL AND expires_at > ?",
		Hash(token), kind, service.now().UTC(),
	).First(&session).Error
	if err != nil {
		return Resolved{}, ErrInvalid
	}
	var user model.User
	if err := service.db.Where("uuid = ? AND is_active = ? AND auth_version = ?", session.UserID, true, session.AuthVersion).First(&user).Error; err != nil {
		return Resolved{}, ErrInvalid
	}
	now := service.now().UTC()
	if session.LastActiveAt.Before(now.Add(-lastActiveWriteInterval)) {
		_ = service.db.Model(&model.AuthSession{}).
			Where("id = ? AND last_active_at < ?", session.ID, now.Add(-lastActiveWriteInterval)).
			Update("last_active_at", now).Error
	}
	return Resolved{Session: session, User: user}, nil
}

func (service *Service) List(userID uuid.UUID, currentToken string) ([]Item, error) {
	var sessions []model.AuthSession
	if err := service.db.Where("user_id = ? AND revoked_at IS NULL AND expires_at > ?", userID, service.now().UTC()).Order("last_active_at DESC").Find(&sessions).Error; err != nil {
		return nil, err
	}
	currentHash := Hash(currentToken)
	items := make([]Item, 0, len(sessions))
	for _, session := range sessions {
		items = append(items, Item{
			ID: session.ID, Kind: session.Kind, DeviceName: DeviceName(session.UserAgent), UserAgent: session.UserAgent,
			IPAddress: session.IPAddress, IPPrefix: session.IPPrefix,
			CreatedAt: session.CreatedAt, LastActiveAt: session.LastActiveAt, Current: session.TokenHash == currentHash,
		})
	}
	return items, nil
}

func (service *Service) VerifyCSRF(session model.AuthSession, token string) bool {
	if session.Kind != KindWeb || token == "" || session.CSRFHash == "" {
		return false
	}
	expected, err := hex.DecodeString(session.CSRFHash)
	if err != nil {
		return false
	}
	actualSum := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(expected, actualSum[:]) == 1
}

func (service *Service) Revoke(token string) error {
	if token == "" {
		return nil
	}
	now := service.now().UTC()
	return service.db.Model(&model.AuthSession{}).
		Where("token_hash = ? AND revoked_at IS NULL", Hash(token)).
		Update("revoked_at", &now).Error
}

func (service *Service) RevokeID(sessionID uuid.UUID) error {
	now := service.now().UTC()
	return service.db.Model(&model.AuthSession{}).
		Where("id = ? AND revoked_at IS NULL", sessionID).
		Update("revoked_at", &now).Error
}

func (service *Service) RevokeUserSession(userID, sessionID uuid.UUID) error {
	now := service.now().UTC()
	result := service.db.Model(&model.AuthSession{}).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL", sessionID, userID).
		Update("revoked_at", &now)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrInvalid
	}
	return nil
}

func (service *Service) RevokeUser(userID uuid.UUID) error {
	now := service.now().UTC()
	return service.db.Model(&model.AuthSession{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", &now).Error
}

func (service *Service) RevokeUserExcept(userID, keepSessionID uuid.UUID) error {
	now := service.now().UTC()
	if err := service.db.Model(&model.AuthSession{}).
		Where("user_id = ? AND id <> ? AND revoked_at IS NULL", userID, keepSessionID).
		Update("revoked_at", &now).Error; err != nil {
		return err
	}
	var user model.User
	if err := service.db.Select("auth_version").First(&user, "uuid = ?", userID).Error; err != nil {
		return err
	}
	return service.db.Model(&model.AuthSession{}).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL", keepSessionID, userID).
		Update("auth_version", user.AuthVersion).Error
}

func (service *Service) RotateCSRF(sessionID uuid.UUID) (string, error) {
	token, err := randomCredential()
	if err != nil {
		return "", err
	}
	result := service.db.Model(&model.AuthSession{}).
		Where("id = ? AND kind = ? AND revoked_at IS NULL AND expires_at > ?", sessionID, KindWeb, service.now().UTC()).
		Update("csrf_hash", Hash(token))
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected != 1 {
		return "", ErrInvalid
	}
	return token, nil
}

func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomCredential() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func hashOptional(token string) string {
	if token == "" {
		return ""
	}
	return Hash(token)
}

func DeviceName(userAgent string) string {
	userAgent = strings.ToLower(userAgent)
	switch {
	case strings.Contains(userAgent, "iphone") || strings.Contains(userAgent, "ipad"):
		return "iPhone 或 iPad"
	case strings.Contains(userAgent, "android"):
		return "Android 设备"
	case strings.Contains(userAgent, "windows"):
		return "Windows 浏览器"
	case strings.Contains(userAgent, "macintosh") || strings.Contains(userAgent, "mac os"):
		return "Mac 浏览器"
	default:
		return "浏览器"
	}
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
