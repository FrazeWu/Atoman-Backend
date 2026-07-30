package service

import (
	"context"
	"errors"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/oauthprovider"

	"gorm.io/gorm"
)

func (s *OAuthService) HandleCallback(ctx context.Context, input OAuthCallbackInput) (OAuthCallbackResult, error) {
	provider, ok := s.providers.Get(input.Provider)
	if !ok {
		return OAuthCallbackResult{}, apperr.NotFound("oauth.provider_unavailable", "Login provider is unavailable")
	}
	if strings.TrimSpace(input.State) == "" || strings.TrimSpace(input.Code) == "" {
		return OAuthCallbackResult{}, apperr.BadRequest("oauth.invalid_callback", "Invalid OAuth callback")
	}

	var flow model.OAuthFlow
	err := s.db.WithContext(ctx).
		Where("secret_hash = ? AND provider = ? AND stage = ? AND consumed_at IS NULL AND expires_at > ?",
			hashOAuthSecret(input.State), input.Provider, model.OAuthStageStarted, s.now().UTC()).
		First(&flow).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return OAuthCallbackResult{}, apperr.BadRequest("oauth.invalid_state", "OAuth session is invalid or expired")
	}
	if err != nil {
		return OAuthCallbackResult{}, apperr.Internal(err)
	}

	profile, err := provider.Exchange(ctx, oauthprovider.CallbackRequest{
		Code:         input.Code,
		CodeVerifier: flow.CodeVerifier,
		NonceHash:    flow.NonceHash,
	})
	if err != nil {
		return OAuthCallbackResult{}, apperr.Wrap(502, "oauth.provider_error", "Login provider rejected the request", err)
	}
	profile.Issuer = strings.TrimSpace(profile.Issuer)
	profile.Subject = strings.TrimSpace(profile.Subject)
	profile.Email = strings.ToLower(strings.TrimSpace(profile.Email))
	if profile.Issuer == "" || profile.Subject == "" {
		return OAuthCallbackResult{}, apperr.BadRequest("oauth.invalid_identity", "Login provider returned an invalid identity")
	}

	var result OAuthCallbackResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var identity model.ExternalIdentity
		findErr := tx.Where("provider = ? AND issuer = ? AND subject = ?", input.Provider, profile.Issuer, profile.Subject).
			First(&identity).Error
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			if profile.Email == "" {
				return apperr.Unprocessable("oauth.verified_email_required", "A verified email address is required")
			}
			if !profile.EmailVerified {
				if input.Provider != model.OAuthProviderMicrosoft {
					return apperr.Unprocessable("oauth.verified_email_required", "A verified email address is required")
				}
				pendingToken, err := randomOAuthToken(32)
				if err != nil {
					return err
				}
				updates := map[string]any{
					"secret_hash":    hashOAuthSecret(pendingToken),
					"stage":          model.OAuthStageVerifyEmail,
					"issuer":         profile.Issuer,
					"subject":        profile.Subject,
					"email":          profile.Email,
					"email_verified": false,
					"display_name":   profile.DisplayName,
					"avatar_url":     profile.AvatarURL,
				}
				rotate := tx.Model(&model.OAuthFlow{}).
					Where("uuid = ? AND secret_hash = ? AND stage = ? AND consumed_at IS NULL", flow.UUID, flow.SecretHash, model.OAuthStageStarted).
					Updates(updates)
				if rotate.Error != nil {
					return rotate.Error
				}
				if rotate.RowsAffected != 1 {
					return apperr.BadRequest("oauth.invalid_state", "OAuth session is invalid or expired")
				}
				result = OAuthCallbackResult{
					Status: OAuthCallbackPending, Stage: model.OAuthStageVerifyEmail,
					PendingToken: pendingToken, ReturnTo: flow.ReturnTo,
				}
				return nil
			}
			if flow.Purpose == model.OAuthPurposeLink {
				if flow.UserID == nil {
					return apperr.Unauthorized("Login required")
				}
				var currentUser model.User
				if err := tx.Where("uuid = ? AND is_active = ?", *flow.UserID, true).First(&currentUser).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return apperr.Forbidden("oauth.account_unavailable", "Account is unavailable")
					}
					return err
				}
				now := s.now().UTC()
				identity := model.ExternalIdentity{
					UserID:        currentUser.UUID,
					Provider:      input.Provider,
					Issuer:        profile.Issuer,
					Subject:       profile.Subject,
					Email:         profile.Email,
					EmailVerified: profile.EmailVerified,
					DisplayName:   profile.DisplayName,
					AvatarURL:     profile.AvatarURL,
					LastLoginAt:   &now,
				}
				if err := tx.Create(&identity).Error; err != nil {
					return apperr.Conflict("oauth.identity_conflict", "This login provider is already linked")
				}
				consume := tx.Model(&model.OAuthFlow{}).
					Where("uuid = ? AND consumed_at IS NULL AND stage = ?", flow.UUID, model.OAuthStageStarted).
					Update("consumed_at", now)
				if consume.Error != nil {
					return consume.Error
				}
				if consume.RowsAffected != 1 {
					return apperr.BadRequest("oauth.invalid_state", "OAuth session is invalid or expired")
				}
				result = OAuthCallbackResult{
					Status:   OAuthCallbackAuthenticated,
					User:     &currentUser,
					ReturnTo: flow.ReturnTo,
				}
				return nil
			}

			stage := model.OAuthStageCompleteProfile
			var existing model.User
			findUserErr := tx.Where("LOWER(email) = ?", profile.Email).First(&existing).Error
			if findUserErr == nil {
				if !existing.IsActive {
					return apperr.Forbidden("oauth.account_unavailable", "Account is unavailable")
				}
				stage = model.OAuthStageConfirmAccount
				flow.UserID = &existing.UUID
			} else if !errors.Is(findUserErr, gorm.ErrRecordNotFound) {
				return findUserErr
			}

			pendingToken, err := randomOAuthToken(32)
			if err != nil {
				return err
			}
			updates := map[string]any{
				"secret_hash":    hashOAuthSecret(pendingToken),
				"stage":          stage,
				"user_id":        flow.UserID,
				"issuer":         profile.Issuer,
				"subject":        profile.Subject,
				"email":          profile.Email,
				"email_verified": profile.EmailVerified,
				"display_name":   profile.DisplayName,
				"avatar_url":     profile.AvatarURL,
			}
			rotate := tx.Model(&model.OAuthFlow{}).
				Where("uuid = ? AND secret_hash = ? AND stage = ? AND consumed_at IS NULL", flow.UUID, flow.SecretHash, model.OAuthStageStarted).
				Updates(updates)
			if rotate.Error != nil {
				return rotate.Error
			}
			if rotate.RowsAffected != 1 {
				return apperr.BadRequest("oauth.invalid_state", "OAuth session is invalid or expired")
			}
			result = OAuthCallbackResult{
				Status:       OAuthCallbackPending,
				Stage:        stage,
				PendingToken: pendingToken,
				ReturnTo:     flow.ReturnTo,
			}
			return nil
		}
		if findErr != nil {
			return findErr
		}
		if flow.Purpose == model.OAuthPurposeLink && (flow.UserID == nil || identity.UserID != *flow.UserID) {
			return apperr.Conflict("oauth.identity_conflict", "This login identity is already linked")
		}

		var user model.User
		if err := tx.Where("uuid = ? AND is_active = ?", identity.UserID, true).First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.Forbidden("oauth.account_unavailable", "Account is unavailable")
			}
			return err
		}

		now := s.now().UTC()
		updates := map[string]any{
			"email":          profile.Email,
			"email_verified": profile.EmailVerified,
			"display_name":   profile.DisplayName,
			"avatar_url":     profile.AvatarURL,
			"last_login_at":  now,
		}
		if err := tx.Model(&identity).Updates(updates).Error; err != nil {
			return err
		}
		if flow.Purpose == model.OAuthPurposeLogin && user.Password == "" {
			pendingToken, err := randomOAuthToken(32)
			if err != nil {
				return err
			}
			rotate := tx.Model(&model.OAuthFlow{}).
				Where("uuid = ? AND secret_hash = ? AND stage = ? AND consumed_at IS NULL", flow.UUID, flow.SecretHash, model.OAuthStageStarted).
				Updates(map[string]any{
					"secret_hash":    hashOAuthSecret(pendingToken),
					"stage":          model.OAuthStageSetPassword,
					"user_id":        user.UUID,
					"issuer":         profile.Issuer,
					"subject":        profile.Subject,
					"email":          profile.Email,
					"email_verified": profile.EmailVerified,
					"display_name":   profile.DisplayName,
					"avatar_url":     profile.AvatarURL,
				})
			if rotate.Error != nil {
				return rotate.Error
			}
			if rotate.RowsAffected != 1 {
				return apperr.BadRequest("oauth.invalid_state", "OAuth session is invalid or expired")
			}
			result = OAuthCallbackResult{
				Status:       OAuthCallbackPending,
				Stage:        model.OAuthStageSetPassword,
				PendingToken: pendingToken,
				ReturnTo:     flow.ReturnTo,
			}
			return nil
		}
		consume := tx.Model(&model.OAuthFlow{}).
			Where("uuid = ? AND consumed_at IS NULL AND stage = ?", flow.UUID, model.OAuthStageStarted).
			Update("consumed_at", now)
		if consume.Error != nil {
			return consume.Error
		}
		if consume.RowsAffected != 1 {
			return apperr.BadRequest("oauth.invalid_state", "OAuth session is invalid or expired")
		}

		result = OAuthCallbackResult{
			Status:   OAuthCallbackAuthenticated,
			User:     &user,
			ReturnTo: flow.ReturnTo,
		}
		return nil
	})
	if err != nil {
		return OAuthCallbackResult{}, err
	}
	return result, nil
}
