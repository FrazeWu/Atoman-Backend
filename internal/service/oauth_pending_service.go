package service

import (
	"context"
	"errors"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *OAuthService) ListIdentities(ctx context.Context, userID uuid.UUID) ([]model.ExternalIdentity, error) {
	var identities []model.ExternalIdentity
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).Order("provider ASC").Find(&identities).Error; err != nil {
		return nil, apperr.Internal(err)
	}
	return identities, nil
}

func (s *OAuthService) SendPendingEmailVerification(ctx context.Context, token string) (string, error) {
	flow, err := s.pendingFlow(ctx, token, model.OAuthStageVerifyEmail)
	if err != nil {
		return "", err
	}
	if flow.Provider != model.OAuthProviderMicrosoft || flow.Email == "" {
		return "", apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired")
	}
	code, err := NewEmailServiceWithoutRedis(s.db).SendVerificationCodeForPurpose(flow.Email, VerificationPurposeOAuthEmail)
	if err != nil {
		return "", apperr.Internal(err)
	}
	return code, nil
}

func (s *OAuthService) VerifyPendingEmail(ctx context.Context, token, code string) (OAuthPendingInfo, error) {
	flow, err := s.pendingFlow(ctx, token, model.OAuthStageVerifyEmail)
	if err != nil {
		return OAuthPendingInfo{}, err
	}
	valid, err := NewEmailServiceWithoutRedis(s.db).VerifyCodeForPurpose(flow.Email, code, VerificationPurposeOAuthEmail)
	if err != nil {
		return OAuthPendingInfo{}, apperr.Internal(err)
	}
	if !valid {
		return OAuthPendingInfo{}, apperr.BadRequest("oauth.email_code_invalid", "Verification code is invalid or expired")
	}

	var info OAuthPendingInfo
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.OAuthFlow
		if err := tx.Where("uuid = ? AND secret_hash = ? AND stage = ? AND consumed_at IS NULL AND expires_at > ?",
			flow.UUID, hashOAuthSecret(token), model.OAuthStageVerifyEmail, s.now().UTC()).First(&current).Error; err != nil {
			return apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired")
		}
		stage := model.OAuthStageCompleteProfile
		var existing model.User
		if current.Purpose == model.OAuthPurposeLink {
			if current.UserID == nil {
				return apperr.Unauthorized("Login required")
			}
			if err := tx.Where("uuid = ? AND is_active = ?", *current.UserID, true).First(&existing).Error; err != nil {
				return apperr.Forbidden("oauth.account_unavailable", "Account is unavailable")
			}
			stage = model.OAuthStageConfirmAccount
		} else {
			findErr := tx.Where("LOWER(email) = ?", current.Email).First(&existing).Error
			if findErr == nil {
				if !existing.IsActive {
					return apperr.Forbidden("oauth.account_unavailable", "Account is unavailable")
				}
				current.UserID = &existing.UUID
				stage = model.OAuthStageConfirmAccount
			} else if !errors.Is(findErr, gorm.ErrRecordNotFound) {
				return findErr
			}
		}
		updates := map[string]any{"stage": stage, "user_id": current.UserID, "email_verified": true}
		result := tx.Model(&model.OAuthFlow{}).
			Where("uuid = ? AND stage = ? AND consumed_at IS NULL", current.UUID, model.OAuthStageVerifyEmail).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired")
		}
		info = OAuthPendingInfo{
			Provider: current.Provider, Stage: stage, Email: current.Email, EmailVerified: true,
			ReturnTo: current.ReturnTo, HasPassword: existing.Password != "",
		}
		return nil
	})
	if err != nil {
		return OAuthPendingInfo{}, err
	}
	return info, nil
}

func (s *OAuthService) PendingInfo(ctx context.Context, token string) (OAuthPendingInfo, error) {
	if strings.TrimSpace(token) == "" {
		return OAuthPendingInfo{}, apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired")
	}
	var flow model.OAuthFlow
	err := s.db.WithContext(ctx).
		Where("secret_hash = ? AND stage IN ? AND consumed_at IS NULL AND expires_at > ?",
			hashOAuthSecret(token), []string{model.OAuthStageVerifyEmail, model.OAuthStageCompleteProfile, model.OAuthStageConfirmAccount, model.OAuthStageSetPassword}, s.now().UTC()).
		First(&flow).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return OAuthPendingInfo{}, apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired")
	}
	if err != nil {
		return OAuthPendingInfo{}, apperr.Internal(err)
	}
	if _, ok := s.providers.Get(flow.Provider); !ok {
		return OAuthPendingInfo{}, apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired")
	}
	hasPassword := false
	if flow.UserID != nil {
		var user model.User
		if err := s.db.WithContext(ctx).Select("password").Where("uuid = ? AND is_active = ?", *flow.UserID, true).First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return OAuthPendingInfo{}, apperr.Forbidden("oauth.account_unavailable", "Account is unavailable")
			}
			return OAuthPendingInfo{}, apperr.Internal(err)
		}
		hasPassword = user.Password != ""
	}
	return OAuthPendingInfo{
		Provider: flow.Provider, Stage: flow.Stage, Email: flow.Email,
		EmailVerified: flow.EmailVerified, ReturnTo: flow.ReturnTo, HasPassword: hasPassword,
	}, nil
}

func (s *OAuthService) CancelPending(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	return s.db.WithContext(ctx).Model(&model.OAuthFlow{}).
		Where("secret_hash = ? AND consumed_at IS NULL", hashOAuthSecret(token)).
		Update("consumed_at", s.now().UTC()).Error
}

func (s *OAuthService) Unlink(ctx context.Context, userID uuid.UUID, provider string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("uuid = ? AND is_active = ?", userID, true).First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("oauth.account_not_found", "Account not found")
			}
			return err
		}

		var identity model.ExternalIdentity
		if err := tx.Where("user_id = ? AND provider = ?", userID, provider).First(&identity).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("oauth.identity_not_found", "Login provider is not linked")
			}
			return err
		}

		var identityCount int64
		if err := tx.Model(&model.ExternalIdentity{}).Where("user_id = ?", userID).Count(&identityCount).Error; err != nil {
			return err
		}
		if user.Password == "" && identityCount <= 1 {
			return apperr.Conflict("oauth.last_login_method", "Add another login method before unlinking this provider")
		}
		if err := tx.Delete(&identity).Error; err != nil {
			return err
		}
		return nil
	})
}

func (s *OAuthService) pendingFlow(ctx context.Context, token string, stage string) (model.OAuthFlow, error) {
	if strings.TrimSpace(token) == "" {
		return model.OAuthFlow{}, apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired")
	}
	var flow model.OAuthFlow
	err := s.db.WithContext(ctx).
		Where("secret_hash = ? AND stage = ? AND consumed_at IS NULL AND expires_at > ?", hashOAuthSecret(token), stage, s.now().UTC()).
		First(&flow).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.OAuthFlow{}, apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired")
	}
	if err != nil {
		return model.OAuthFlow{}, apperr.Internal(err)
	}
	if _, ok := s.providers.Get(flow.Provider); !ok {
		return model.OAuthFlow{}, apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired")
	}
	return flow, nil
}
