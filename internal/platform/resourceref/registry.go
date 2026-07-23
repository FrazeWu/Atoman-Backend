package resourceref

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

var (
	ErrInvalidResolver       = errors.New("invalid resource resolver")
	ErrResolverNotRegistered = errors.New("resource resolver not registered")
)

type Viewer struct {
	UserID uuid.UUID
}

type Resolved struct {
	Kind          string
	ID            uuid.UUID
	Title         string
	Visible       bool
	Referenceable bool
}

type Resolver func(context.Context, Viewer, uuid.UUID) (Resolved, error)

type Registry struct {
	resolvers map[string]Resolver
}

func NewRegistry() *Registry {
	return &Registry{resolvers: make(map[string]Resolver)}
}

func (r *Registry) Register(kind string, resolver Resolver) error {
	if !isSupportedKind(kind) {
		return fmt.Errorf("%w: %s", ErrUnknownKind, kind)
	}
	if resolver == nil {
		return ErrInvalidResolver
	}
	r.resolvers[kind] = resolver
	return nil
}

func (r *Registry) Resolve(ctx context.Context, viewer Viewer, kind string, resourceID uuid.UUID) (Resolved, error) {
	if !isSupportedKind(kind) {
		return Resolved{}, fmt.Errorf("%w: %s", ErrUnknownKind, kind)
	}
	resolver, ok := r.resolvers[kind]
	if !ok {
		return Resolved{}, fmt.Errorf("%w: %s", ErrResolverNotRegistered, kind)
	}
	return resolver(ctx, viewer, resourceID)
}
