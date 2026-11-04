package feed

import (
	"strings"

	"atoman/internal/platform/authctx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// snapshotReadScope is an explicit, server-verified data provenance contract,
// NOT a client-supplied parameter. Current public/explore routes mix posts and
// external feeds, so neither route may claim RSSOnly without a separate proof.
type snapshotReadScope uint8

const (
	snapshotReadScopeUnknown snapshotReadScope = iota
	snapshotReadScopeVerifiedPublicRSSOnly
)

// eligiblePublicRecentDedupSnapshotRequest is fail-closed. It is deliberately
// not wired into production HTTP routes: the HTTP route's RSS-only provenance,
// full DTO parity and an independently controlled rollout flag remain unproven.
func eligiblePublicRecentDedupSnapshotRequest(c *gin.Context, query FeedQuery, user authctx.CurrentUser, scope snapshotReadScope) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil || scope != snapshotReadScopeVerifiedPublicRSSOnly || user.ID != uuid.Nil || user.Role != "" && user.Role != authctx.RoleAnonymous {
		return false
	}
	if !query.HideDuplicates || query.IsRead != nil || query.SourceID != uuid.Nil || query.GroupID != uuid.Nil || query.viewerID != uuid.Nil || len(query.sourceVisibleAfter) != 0 {
		return false
	}
	if query.Page < 1 || query.PageSize < 1 || strings.TrimSpace(query.ContentType) != "" || strings.TrimSpace(query.Category) != "" || strings.TrimSpace(query.LanguageCode) != "" || strings.TrimSpace(query.SourceType) != "" || strings.TrimSpace(query.Search) != "" {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(query.Sort), "recent") {
		return false
	}
	allowed := map[string]bool{"page": true, "page_size": true, "hide_duplicates": true, "sort": true}
	for key, values := range c.Request.URL.Query() {
		if !allowed[key] || len(values) != 1 {
			return false
		}
		switch key {
		case "hide_duplicates":
			if values[0] != "true" {
				return false
			}
		case "sort":
			if values[0] != "recent" {
				return false
			}
		}
	}
	return true
}
