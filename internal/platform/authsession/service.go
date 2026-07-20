package authsession

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	KindWeb = "web"
	KindAPI = "api"
	TTL     = 30 * 24 * time.Hour
)

var ErrInvalid = errors.New("invalid auth session")

type Credentials struct {
	Token     string
	CSRFToken string
	ExpiresAt time.Time
}

type Resolved struct {
	Session model.AuthSession
	User    model.User
}

type Service struct {
	db  *gorm.DB
	now func() time.Time
}

func New(db *gorm.DB) *Service {
	return &Service{db: db, now: time.Now}
}

func (service *Service) Create(userID uuid.UUID, kind string) (Credentials, error) {
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
	session := model.AuthSession{
		UserID:    userID,
		TokenHash: Hash(token),
		CSRFHash:  hashOptional(csrfToken),
		Kind:      kind,
		ExpiresAt: expiresAt,
	}
	if err := service.db.Create(&session).Error; err != nil {
		return Credentials{}, err
	}
	return Credentials{Token: token, CSRFToken: csrfToken, ExpiresAt: expiresAt}, nil
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
	if err := service.db.Where("uuid = ? AND is_active = ?", session.UserID, true).First(&user).Error; err != nil {
		return Resolved{}, ErrInvalid
	}
	return Resolved{Session: session, User: user}, nil
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

func (service *Service) RevokeUser(userID uuid.UUID) error {
	now := service.now().UTC()
	return service.db.Model(&model.AuthSession{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", &now).Error
}

func (service *Service) RevokeUserExcept(userID, keepSessionID uuid.UUID) error {
	now := service.now().UTC()
	return service.db.Model(&model.AuthSession{}).
		Where("user_id = ? AND id <> ? AND revoked_at IS NULL", userID, keepSessionID).
		Update("revoked_at", &now).Error
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
