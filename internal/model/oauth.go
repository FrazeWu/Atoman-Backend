package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	OAuthProviderGoogle    = "google"
	OAuthProviderGitHub    = "github"
	OAuthProviderMicrosoft = "microsoft"

	OAuthPurposeLogin = "login"
	OAuthPurposeLink  = "link"

	OAuthStageStarted         = "started"
	OAuthStageCompleteProfile = "complete_profile"
	OAuthStageConfirmAccount  = "confirm_account"
)

type ExternalIdentity struct {
	UUID          uuid.UUID  `json:"uuid" gorm:"type:uuid;primaryKey"`
	UserID        uuid.UUID  `json:"user_id" gorm:"type:uuid;not null;index;uniqueIndex:idx_external_identity_user_provider,priority:1"`
	Provider      string     `json:"provider" gorm:"size:32;not null;uniqueIndex:idx_external_identity_subject,priority:1;uniqueIndex:idx_external_identity_user_provider,priority:2"`
	Issuer        string     `json:"issuer" gorm:"size:255;not null;uniqueIndex:idx_external_identity_subject,priority:2"`
	Subject       string     `json:"subject" gorm:"size:255;not null;uniqueIndex:idx_external_identity_subject,priority:3"`
	Email         string     `json:"email" gorm:"size:320"`
	EmailVerified bool       `json:"email_verified" gorm:"not null;default:false"`
	DisplayName   string     `json:"display_name" gorm:"size:255"`
	AvatarURL     string     `json:"avatar_url" gorm:"size:2048"`
	LastLoginAt   *time.Time `json:"last_login_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (ExternalIdentity) TableName() string {
	return "external_identities"
}

func (m *ExternalIdentity) BeforeCreate(tx *gorm.DB) error {
	if m.UUID != uuid.Nil {
		return nil
	}
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	m.UUID = id
	return nil
}

type OAuthFlow struct {
	UUID          uuid.UUID  `json:"uuid" gorm:"type:uuid;primaryKey"`
	SecretHash    string     `json:"-" gorm:"size:64;not null;uniqueIndex"`
	Provider      string     `json:"provider" gorm:"size:32;not null;index"`
	Purpose       string     `json:"purpose" gorm:"size:16;not null"`
	Stage         string     `json:"stage" gorm:"size:32;not null"`
	UserID        *uuid.UUID `json:"user_id,omitempty" gorm:"type:uuid;index"`
	CodeVerifier  string     `json:"-" gorm:"size:128"`
	NonceHash     string     `json:"-" gorm:"size:64"`
	Issuer        string     `json:"-" gorm:"size:255"`
	Subject       string     `json:"-" gorm:"size:255"`
	Email         string     `json:"email" gorm:"size:320"`
	EmailVerified bool       `json:"email_verified" gorm:"not null;default:false"`
	DisplayName   string     `json:"display_name" gorm:"size:255"`
	AvatarURL     string     `json:"avatar_url" gorm:"size:2048"`
	ReturnTo      string     `json:"return_to" gorm:"size:2048"`
	ExpiresAt     time.Time  `json:"expires_at" gorm:"not null;index"`
	ConsumedAt    *time.Time `json:"consumed_at,omitempty" gorm:"index"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (OAuthFlow) TableName() string {
	return "oauth_flows"
}

func (m *OAuthFlow) BeforeCreate(tx *gorm.DB) error {
	if m.UUID != uuid.Nil {
		return nil
	}
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	m.UUID = id
	return nil
}
