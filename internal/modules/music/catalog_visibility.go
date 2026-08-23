package music

import (
	"fmt"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func musicEntryVisibilityCondition(table, ownerColumn string, user *authctx.CurrentUser, includeMerged bool) (string, []any) {
	if user != nil && (user.Role == authctx.RoleAdmin || user.Role == authctx.RoleOwner) {
		return "1 = 1", nil
	}
	lifecycleColumn := fmt.Sprintf("%s.lifecycle_status", table)
	visible := []string{model.MusicLifecycleActive}
	if includeMerged {
		visible = append(visible, model.MusicLifecycleMerged)
	}
	if user != nil && user.ID != uuid.Nil && ownerColumn != "" {
		owner := fmt.Sprintf("%s.%s", table, ownerColumn)
		return fmt.Sprintf("(%s IN ? OR (%s = ? AND %s = ?))", lifecycleColumn, lifecycleColumn, owner), []any{visible, model.MusicLifecycleDraft, user.ID}
	}
	return lifecycleColumn + " IN ?", []any{visible}
}

func scopeVisibleMusicEntries(db *gorm.DB, table, ownerColumn string, user *authctx.CurrentUser, includeMerged bool) *gorm.DB {
	condition, args := musicEntryVisibilityCondition(table, ownerColumn, user, includeMerged)
	return db.Where(condition, args...)
}

func visibleArtistPreload(user *authctx.CurrentUser) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return scopeVisibleMusicEntries(db, `"Artists"`, "created_by", user, true)
	}
}

func visibleAlbumPreload(user *authctx.CurrentUser) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return scopeVisibleMusicEntries(db, `"Albums"`, "uploaded_by", user, true)
	}
}

func visibleSongPreload(user *authctx.CurrentUser) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return scopeVisibleMusicEntries(db, `"Songs"`, "uploaded_by", user, false).
			Order("COALESCE(NULLIF(disc_number, 0), 1) ASC, track_number ASC, created_at ASC")
	}
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
