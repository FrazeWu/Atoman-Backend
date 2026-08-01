package service

import (
	"context"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authlogin"
	"atoman/internal/platform/authsession"
	"atoman/internal/platform/oauthprovider"
	"atoman/internal/platform/requestmeta"

	"github.com/google/uuid"
	"gorm.io/gorm"
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
	PendingToken    string
	Username        string
	Password        string
	PasswordConfirm string
}

type OAuthConfirmAccountInput struct {
	PendingToken string
	Password     string
}

type OAuthSetPasswordInput struct {
	PendingToken    string
	Password        string
	PasswordConfirm string
}

type OAuthCompletionResult struct {
	User     model.User
	ReturnTo string
}

type OAuthPendingInfo struct {
	Provider      string
	Stage         string
	Email         string
	EmailVerified bool
	ReturnTo      string
	HasPassword   bool
}

func NewOAuthService(db *gorm.DB, providers *oauthprovider.Registry) *OAuthService {
	return &OAuthService{db: db, providers: providers, now: time.Now}
}

func (s *OAuthService) ProviderNames() []string {
	return s.providers.Names()
}

func (s *OAuthService) CreateWebSession(userID uuid.UUID, method string, info requestmeta.Info, replacedToken string) (authsession.Credentials, error) {
	var credentials authsession.Credentials
	err := s.db.Transaction(func(tx *gorm.DB) error {
		created, err := authsession.New(tx).Create(userID, authsession.KindWeb, authsession.Metadata{
			UserAgent: info.UserAgent, IPAddress: info.IPAddress, IPPrefix: info.IPPrefix,
		})
		if err != nil {
			return err
		}
		credentials = created
		if replacedToken != "" && replacedToken != credentials.Token {
			if err := authsession.New(tx).Revoke(replacedToken); err != nil {
				return err
			}
		}
		return authlogin.Record(tx, userID, &credentials.SessionID, method, model.LoginResultSucceeded, "", info)
	})
	return credentials, err
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
