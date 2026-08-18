package music

import (
	"fmt"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func scopeVisibleMusicEntries(db *gorm.DB, table, ownerColumn string, user *authctx.CurrentUser, includeMerged bool) *gorm.DB {
	if user != nil && (user.Role == authctx.RoleAdmin || user.Role == authctx.RoleOwner) {
		return db
	}
	lifecycleColumn := fmt.Sprintf("%s.lifecycle_status", table)
	visible := []string{model.MusicLifecycleActive}
	if includeMerged {
		visible = append(visible, model.MusicLifecycleMerged)
	}
	if user != nil && user.ID != uuid.Nil && ownerColumn != "" {
		owner := fmt.Sprintf("%s.%s", table, ownerColumn)
		return db.Where("("+lifecycleColumn+" IN ? OR ("+lifecycleColumn+" = ? AND "+owner+" = ?))", visible, model.MusicLifecycleDraft, user.ID)
	}
	return db.Where(lifecycleColumn+" IN ?", visible)
}

func canViewMusicLifecycle(lifecycle string, ownerID *uuid.UUID, user *authctx.CurrentUser, includeMerged bool) bool {
	if user != nil && (user.Role == authctx.RoleAdmin || user.Role == authctx.RoleOwner) {
		return true
	}
	if lifecycle == "" || lifecycle == model.MusicLifecycleActive || (includeMerged && lifecycle == model.MusicLifecycleMerged) {
		return true
	}
	return lifecycle == model.MusicLifecycleDraft && user != nil && ownerID != nil && *ownerID == user.ID
}

func musicViewer(cUser authctx.CurrentUser, ok bool) *authctx.CurrentUser {
	if !ok {
		return nil
	}
	return &cUser
}

func visibleSongPreload(user *authctx.CurrentUser) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return scopeVisibleMusicEntries(db, "\"Songs\"", "uploaded_by", user, false).
			Order("COALESCE(NULLIF(disc_number, 0), 1) ASC, track_number ASC, created_at ASC")
	}
}
