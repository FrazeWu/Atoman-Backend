package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/oauthprovider"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const oauthFlowTTL = 10 * time.Minute

type OAuthService struct {
	db        *gorm.DB
	providers *oauthprovider.Registry
	now       func() time.Time
}

type OAuthBeginInput struct {
	Provider string
	Purpose  string
	ReturnTo string
	UserID   *uuid.UUID
}

type OAuthBeginResult struct {
	AuthorizationURL string
	State            string
}

const (
	OAuthCallbackAuthenticated = "authenticated"
	OAuthCallbackPending       = "pending"
)

type OAuthCallbackInput struct {
	Provider string
	State    string
	Code     string
}

type OAuthCallbackResult struct {
	Status       string
	Stage        string
	PendingToken string
	User         *model.User
	ReturnTo     string
}

type OAuthCompleteProfileInput struct {
	PendingToken string
	Username     string
}

type OAuthConfirmAccountInput struct {
	PendingToken string
	Password     string
}

type OAuthCompletionResult struct {
	User     model.User
	ReturnTo string
}

type OAuthPendingInfo struct {
	Provider string
	Stage    string
	Email    string
	ReturnTo string
}

func NewOAuthService(db *gorm.DB, providers *oauthprovider.Registry) *OAuthService {
	return &OAuthService{db: db, providers: providers, now: time.Now}
}

func (s *OAuthService) ProviderNames() []string {
	return s.providers.Names()
}

func (s *OAuthService) Begin(ctx context.Context, input OAuthBeginInput) (OAuthBeginResult, error) {
	provider, ok := s.providers.Get(input.Provider)
	if !ok {
		return OAuthBeginResult{}, apperr.NotFound("oauth.provider_unavailable", "Login provider is unavailable")
	}
	purpose := input.Purpose
	if purpose == "" {
		purpose = model.OAuthPurposeLogin
	}
	if purpose != model.OAuthPurposeLogin && purpose != model.OAuthPurposeLink {
		return OAuthBeginResult{}, apperr.BadRequest("oauth.invalid_purpose", "Invalid OAuth purpose")
	}
	if purpose == model.OAuthPurposeLink && input.UserID == nil {
		return OAuthBeginResult{}, apperr.Unauthorized("Login required")
	}

	state, err := randomOAuthToken(32)
	if err != nil {
		return OAuthBeginResult{}, apperr.Internal(err)
	}
	verifier, err := randomOAuthToken(48)
	if err != nil {
		return OAuthBeginResult{}, apperr.Internal(err)
	}
	nonce, err := randomOAuthToken(32)
	if err != nil {
		return OAuthBeginResult{}, apperr.Internal(err)
	}

	flow := model.OAuthFlow{
		SecretHash:   hashOAuthSecret(state),
		Provider:     input.Provider,
		Purpose:      purpose,
		Stage:        model.OAuthStageStarted,
		UserID:       input.UserID,
		CodeVerifier: verifier,
		NonceHash:    hashOAuthSecret(nonce),
		ReturnTo:     sanitizeOAuthReturnTo(input.ReturnTo),
		ExpiresAt:    s.now().UTC().Add(oauthFlowTTL),
	}
	if err := s.db.WithContext(ctx).Create(&flow).Error; err != nil {
		return OAuthBeginResult{}, apperr.Internal(err)
	}

	authorizationURL, err := provider.AuthorizationURL(oauthprovider.AuthorizationRequest{
		State:         state,
		CodeChallenge: oauthCodeChallenge(verifier),
		Nonce:         nonce,
	})
	if err != nil {
		return OAuthBeginResult{}, apperr.Wrap(502, "oauth.provider_error", "Login provider is unavailable", err)
	}
	return OAuthBeginResult{AuthorizationURL: authorizationURL, State: state}, nil
}

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
			if !profile.EmailVerified || profile.Email == "" {
				return apperr.Unprocessable("oauth.verified_email_required", "A verified email address is required")
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
			Password:    "",
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

func (s *OAuthService) ConfirmAccount(ctx context.Context, input OAuthConfirmAccountInput) (OAuthCompletionResult, error) {
	flow, err := s.pendingFlow(ctx, input.PendingToken, model.OAuthStageConfirmAccount)
	if err != nil {
		return OAuthCompletionResult{}, err
	}
	if flow.UserID == nil {
		return OAuthCompletionResult{}, apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired")
	}

	var user model.User
	if err := s.db.WithContext(ctx).Where("uuid = ? AND is_active = ?", *flow.UserID, true).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return OAuthCompletionResult{}, apperr.Forbidden("oauth.account_unavailable", "Account is unavailable")
		}
		return OAuthCompletionResult{}, apperr.Internal(err)
	}
	if user.Password == "" {
		return OAuthCompletionResult{}, apperr.Conflict("oauth.password_not_set", "Use an existing login method to link this account")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(input.Password)); err != nil {
		return OAuthCompletionResult{}, apperr.New(401, "oauth.invalid_credentials", "Password is incorrect", nil)
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

func (s *OAuthService) ListIdentities(ctx context.Context, userID uuid.UUID) ([]model.ExternalIdentity, error) {
	var identities []model.ExternalIdentity
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).Order("provider ASC").Find(&identities).Error; err != nil {
		return nil, apperr.Internal(err)
	}
	return identities, nil
}

func (s *OAuthService) PendingInfo(ctx context.Context, token string) (OAuthPendingInfo, error) {
	if strings.TrimSpace(token) == "" {
		return OAuthPendingInfo{}, apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired")
	}
	var flow model.OAuthFlow
	err := s.db.WithContext(ctx).
		Where("secret_hash = ? AND stage IN ? AND consumed_at IS NULL AND expires_at > ?",
			hashOAuthSecret(token), []string{model.OAuthStageCompleteProfile, model.OAuthStageConfirmAccount}, s.now().UTC()).
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
	return OAuthPendingInfo{Provider: flow.Provider, Stage: flow.Stage, Email: flow.Email, ReturnTo: flow.ReturnTo}, nil
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

func randomOAuthToken(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func hashOAuthSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func oauthCodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func sanitizeOAuthReturnTo(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "/"
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(raw, "//") {
		return "/"
	}
	return raw
}
