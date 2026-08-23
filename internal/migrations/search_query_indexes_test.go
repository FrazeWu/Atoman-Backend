package migrations

import (
	"strings"
	"testing"
)

func TestSearchQueryIndexStatementsCoverSearchExecutionPaths(t *testing.T) {
	statements := searchQueryIndexStatements()
	if len(statements) != 12 {
		t.Fatalf("expected twelve search query indexes, got %d", len(statements))
	}
	joined := strings.Join(statements, "\n")
	for _, fragment := range []string{
		"idx_posts_public_search_order",
		"idx_feed_sources_visible_type_language",
		"idx_feed_items_public_published",
		"idx_feed_items_source_published_live",
		"idx_subscriptions_user_source_live",
		"idx_discussion_targets_kind_resource_live",
		"idx_forum_permissions_category_view",
		"idx_forum_group_members_user_group",
		"idx_debates_status_created_live",
		"idx_timeline_events_public_date",
		"idx_timeline_persons_public_name",
		"idx_feed_item_stars_item",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS",
	} {
		if !strings.Contains(joined, fragment) {
			t.Fatalf("expected search query index SQL to contain %q, got: %s", fragment, joined)
		}
	}
}
