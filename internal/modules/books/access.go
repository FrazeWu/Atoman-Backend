package books

import (
	"strings"

	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
)

// BookViewer is the authorization input shared by private-resource and review flows.
type BookViewer struct {
	UserID uuid.UUID
	Role   string
}

func CanReadPrivateResource(viewer BookViewer, ownerID uuid.UUID) bool {
	return viewer.UserID != uuid.Nil && ownerID != uuid.Nil && viewer.UserID == ownerID
}

func CanReviewSubmission(viewer BookViewer, submitterID uuid.UUID) bool {
	if viewer.UserID == uuid.Nil || submitterID == uuid.Nil || viewer.UserID == submitterID {
		return false
	}
	return authctx.RoleAtLeast(viewer.Role, authctx.RoleModerator)
}

func IsPublicBookLifecycle(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "active")
}

func IsPublicBookAssetStatus(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "published")
}
