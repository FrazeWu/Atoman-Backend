package oauthprovider

import (
	"context"
	"sort"
)

type AuthorizationRequest struct {
	State         string
	CodeChallenge string
	Nonce         string
}

type CallbackRequest struct {
	Code         string
	CodeVerifier string
	NonceHash    string
}

type Profile struct {
	Issuer        string
	Subject       string
	Email         string
	EmailVerified bool
	DisplayName   string
	AvatarURL     string
}

type Provider interface {
	Name() string
	AuthorizationURL(AuthorizationRequest) (string, error)
	Exchange(context.Context, CallbackRequest) (Profile, error)
}

type Registry struct {
	providers map[string]Provider
}

func NewRegistry(providers ...Provider) *Registry {
	registry := &Registry{providers: make(map[string]Provider, len(providers))}
	for _, provider := range providers {
		if provider != nil && provider.Name() != "" {
			registry.providers[provider.Name()] = provider
		}
	}
	return registry
}

func (r *Registry) Get(name string) (Provider, bool) {
	if r == nil {
		return nil, false
	}
	provider, ok := r.providers[name]
	return provider, ok
}

func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
