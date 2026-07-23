package resourceref

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRegistryRejectsUnknownKindAndNilResolver(t *testing.T) {
	registry := NewRegistry()
	resolver := Resolver(func(context.Context, Viewer, uuid.UUID) (Resolved, error) {
		return Resolved{}, nil
	})

	require.ErrorIs(t, registry.Register("alubm", resolver), ErrUnknownKind)
	require.ErrorIs(t, registry.Register(KindAlbum, nil), ErrInvalidResolver)
}

func TestRegistryRejectsUnregisteredResolver(t *testing.T) {
	registry := NewRegistry()

	_, err := registry.Resolve(context.Background(), Viewer{}, KindAlbum, uuid.New())

	require.ErrorIs(t, err, ErrResolverNotRegistered)
}

func TestRegistryRejectsUnknownKindDuringResolve(t *testing.T) {
	registry := NewRegistry()

	_, err := registry.Resolve(context.Background(), Viewer{}, "alubm", uuid.New())

	require.ErrorIs(t, err, ErrUnknownKind)
}

func TestRegistryResolvesWithContextViewerAndResourceID(t *testing.T) {
	type contextKey string
	const key contextKey = "request"
	ctx := context.WithValue(context.Background(), key, "expected")
	viewer := Viewer{UserID: uuid.New()}
	resourceID := uuid.New()
	want := Resolved{
		Kind: KindAlbum, ID: resourceID, Title: "Album title", Visible: true, Referenceable: true,
	}
	registry := NewRegistry()
	require.NoError(t, registry.Register(KindAlbum, func(gotContext context.Context, gotViewer Viewer, gotID uuid.UUID) (Resolved, error) {
		require.Equal(t, "expected", gotContext.Value(key))
		require.Equal(t, viewer, gotViewer)
		require.Equal(t, resourceID, gotID)
		return want, nil
	}))

	got, err := registry.Resolve(ctx, viewer, KindAlbum, resourceID)

	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestRegistryReturnsInvisibleAndUnreferenceableResultUnchanged(t *testing.T) {
	resourceID := uuid.New()
	want := Resolved{
		Kind: KindPost, ID: resourceID, Title: "Private draft", Visible: false, Referenceable: false,
	}
	registry := NewRegistry()
	require.NoError(t, registry.Register(KindPost, func(context.Context, Viewer, uuid.UUID) (Resolved, error) {
		return want, nil
	}))

	got, err := registry.Resolve(context.Background(), Viewer{}, KindPost, resourceID)

	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestRegistryReturnsResolverResultAndErrorUnchanged(t *testing.T) {
	resolverErr := errors.New("resolver failed")
	resourceID := uuid.New()
	want := Resolved{Kind: KindAlbum, ID: resourceID, Title: "Partial result"}
	registry := NewRegistry()
	require.NoError(t, registry.Register(KindAlbum, func(context.Context, Viewer, uuid.UUID) (Resolved, error) {
		return want, resolverErr
	}))

	got, err := registry.Resolve(context.Background(), Viewer{}, KindAlbum, resourceID)

	require.Equal(t, want, got)
	require.ErrorIs(t, err, resolverErr)
}
