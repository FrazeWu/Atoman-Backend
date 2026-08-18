package handlers

import (
	"fmt"
	"net/http"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func RetiredLegacyMusicWriteHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusGone, gin.H{"error": "This endpoint is retired; use music revisions and Wiki state requests"})
	}
}

func scopeVisibleLegacyMusic(c *gin.Context, db *gorm.DB, table, ownerColumn string, includeMerged bool) *gorm.DB {
	user, authenticated := authctx.Current(c)
	if authenticated && (user.Role == authctx.RoleAdmin || user.Role == authctx.RoleOwner) {
		return db
	}
	lifecycleColumn := fmt.Sprintf("%s.lifecycle_status", table)
	visible := []string{model.MusicLifecycleActive}
	if includeMerged {
		visible = append(visible, model.MusicLifecycleMerged)
	}
	if authenticated && user.ID != uuid.Nil && ownerColumn != "" {
		owner := fmt.Sprintf("%s.%s", table, ownerColumn)
		return db.Where("("+lifecycleColumn+" IN ? OR ("+lifecycleColumn+" = ? AND "+owner+" = ?))", visible, model.MusicLifecycleDraft, user.ID)
	}
	return db.Where(lifecycleColumn+" IN ?", visible)
}
