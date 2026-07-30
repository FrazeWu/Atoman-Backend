package service

import (
	"context"
	"errors"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *OAuthService) CompleteProfile(ctx context.Context, input OAuthCompleteProfileInput) (OAuthCompletionResult, error) {
	flow, err := s.pendingFlow(ctx, input.PendingToken, model.OAuthStageCompleteProfile)
	if err != nil {
		return OAuthCompletionResult{}, err
	}
	if !flow.EmailVerified || flow.Email == "" || flow.Issuer == "" || flow.Subject == "" {
		return OAuthCompletionResult{}, apperr.Unprocessable("oauth.invalid_identity", "OAuth identity is incomplete")
	}

	username := strings.ToLower(strings.TrimSpace(input.Username))
	if err := NewSiteNamespaceService(s.db).ValidateUsernameAvailable(ctx, username); err != nil {
		switch {
		case errors.Is(err, ErrSiteHandleInvalid):
			return OAuthCompletionResult{}, apperr.BadRequest("oauth.username_invalid", "Username is invalid")
		case errors.Is(err, ErrSiteHandleReserved):
			return OAuthCompletionResult{}, apperr.BadRequest("oauth.username_reserved", "Username is unavailable")
		case errors.Is(err, ErrSiteHandleTaken):
			return OAuthCompletionResult{}, apperr.Conflict("oauth.username_taken", "Username is already in use")
		default:
			return OAuthCompletionResult{}, apperr.Internal(err)
		}
	}
	if err := validateOAuthPassword(input.Password, input.PasswordConfirm); err != nil {
		return OAuthCompletionResult{}, err
	}
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return OAuthCompletionResult{}, apperr.Internal(err)
	}

	var result OAuthCompletionResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.OAuthFlow
		if err := tx.Where("uuid = ? AND secret_hash = ? AND stage = ? AND consumed_at IS NULL AND expires_at > ?",
			flow.UUID, hashOAuthSecret(input.PendingToken), model.OAuthStageCompleteProfile, s.now().UTC()).First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired")
			}
			return err
		}

		user := model.User{
			Username:    username,
			Email:       current.Email,
			Password:    string(hashedPassword),
			Role:        "user",
			DisplayName: current.DisplayName,
			AvatarURL:   current.AvatarURL,
			IsActive:    true,
		}
		if err := tx.Create(&user).Error; err != nil {
			return apperr.Conflict("oauth.account_conflict", "Username or email is already in use")
		}
		if err := tx.Create(&model.UserSettings{UserID: user.UUID}).Error; err != nil {
			return err
		}
		if err := NewUserBootstrapService(tx).EnsureDefaults(user.UUID, user.Username); err != nil {
			return err
		}

		now := s.now().UTC()
		identity := model.ExternalIdentity{
			UserID:        user.UUID,
			Provider:      current.Provider,
			Issuer:        current.Issuer,
			Subject:       current.Subject,
			Email:         current.Email,
			EmailVerified: current.EmailVerified,
			DisplayName:   current.DisplayName,
			AvatarURL:     current.AvatarURL,
			LastLoginAt:   &now,
		}
		if err := tx.Create(&identity).Error; err != nil {
			return apperr.Conflict("oauth.identity_conflict", "This login identity is already linked")
		}
		consume := tx.Model(&model.OAuthFlow{}).
			Where("uuid = ? AND consumed_at IS NULL", current.UUID).
			Update("consumed_at", now)
		if consume.Error != nil {
			return consume.Error
		}
		if consume.RowsAffected != 1 {
			return apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired")
		}
		result = OAuthCompletionResult{User: user, ReturnTo: current.ReturnTo}
		return nil
	})
	if err != nil {
		return OAuthCompletionResult{}, err
	}
	return result, nil
}

func (s *OAuthService) SetPassword(ctx context.Context, input OAuthSetPasswordInput) (OAuthCompletionResult, error) {
	flow, err := s.pendingFlow(ctx, input.PendingToken, model.OAuthStageSetPassword)
	if err != nil {
		return OAuthCompletionResult{}, err
	}
	if flow.UserID == nil {
		return OAuthCompletionResult{}, apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired")
	}
	if err := validateOAuthPassword(input.Password, input.PasswordConfirm); err != nil {
		return OAuthCompletionResult{}, err
	}
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return OAuthCompletionResult{}, apperr.Internal(err)
	}

	var result OAuthCompletionResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.OAuthFlow
		if err := tx.Where("uuid = ? AND secret_hash = ? AND stage = ? AND consumed_at IS NULL AND expires_at > ?",
			flow.UUID, hashOAuthSecret(input.PendingToken), model.OAuthStageSetPassword, s.now().UTC()).First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired")
			}
			return err
		}
		if current.UserID == nil {
			return apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired")
		}

		var user model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("uuid = ? AND is_active = ?", *current.UserID, true).First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.Forbidden("oauth.account_unavailable", "Account is unavailable")
			}
			return err
		}
		var identity model.ExternalIdentity
		if err := tx.Where("user_id = ? AND provider = ? AND issuer = ? AND subject = ?",
			user.UUID, current.Provider, current.Issuer, current.Subject).First(&identity).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.Conflict("oauth.identity_unlinked", "Login identity is no longer linked")
			}
			return err
		}
		setPassword := tx.Model(&model.User{}).
			Where("uuid = ? AND is_active = ? AND password = ?", user.UUID, true, "").
			Update("password", string(hashedPassword))
		if setPassword.Error != nil {
			return setPassword.Error
		}
		if setPassword.RowsAffected != 1 {
			return apperr.Conflict("oauth.password_already_set", "Password is already set")
		}
		now := s.now().UTC()
		consume := tx.Model(&model.OAuthFlow{}).
			Where("uuid = ? AND consumed_at IS NULL", current.UUID).
			Update("consumed_at", now)
		if consume.Error != nil {
			return consume.Error
		}
		if consume.RowsAffected != 1 {
			return apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired")
		}
		user.Password = string(hashedPassword)
		result = OAuthCompletionResult{User: user, ReturnTo: current.ReturnTo}
		return nil
	})
	if err != nil {
		return OAuthCompletionResult{}, err
	}
	return result, nil
}

func (s *OAuthService) ConfirmAccount(ctx context.Context, input OAuthConfirmAccountInput) (OAuthCompletionResult, error) {
	flow, err := s.pendingFlow(ctx, input.PendingToken, model.OAuthStageConfirmAccount)
	if err != nil {
		return OAuthCompletionResult{}, err
	}
	if flow.UserID == nil {
		return OAuthCompletionResult{}, apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired")
	}

	var result OAuthCompletionResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.OAuthFlow
		if err := tx.Where("uuid = ? AND secret_hash = ? AND stage = ? AND consumed_at IS NULL AND expires_at > ?",
			flow.UUID, hashOAuthSecret(input.PendingToken), model.OAuthStageConfirmAccount, s.now().UTC()).First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired")
			}
			return err
		}
		if current.UserID == nil {
			return apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired")
		}

		var user model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("uuid = ? AND is_active = ?", *current.UserID, true).First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.Forbidden("oauth.account_unavailable", "Account is unavailable")
			}
			return err
		}
		if user.Password == "" {
			return apperr.Conflict("oauth.password_not_set", "Use an existing login method to link this account")
		}
		if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(input.Password)); err != nil {
			return apperr.New(401, "oauth.invalid_credentials", "Password is incorrect", nil)
		}

		now := s.now().UTC()
		identity := model.ExternalIdentity{
			UserID:        user.UUID,
			Provider:      current.Provider,
			Issuer:        current.Issuer,
			Subject:       current.Subject,
			Email:         current.Email,
			EmailVerified: current.EmailVerified,
			DisplayName:   current.DisplayName,
			AvatarURL:     current.AvatarURL,
			LastLoginAt:   &now,
		}
		if err := tx.Create(&identity).Error; err != nil {
			return apperr.Conflict("oauth.identity_conflict", "This login identity is already linked")
		}
		consume := tx.Model(&model.OAuthFlow{}).
			Where("uuid = ? AND consumed_at IS NULL", current.UUID).
			Update("consumed_at", now)
		if consume.Error != nil {
			return consume.Error
		}
		if consume.RowsAffected != 1 {
			return apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired")
		}
		result = OAuthCompletionResult{User: user, ReturnTo: current.ReturnTo}
		return nil
	})
	if err != nil {
		return OAuthCompletionResult{}, err
	}
	return result, nil
}
