package feed

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

// The public explore endpoint combines RSS with other public content.
// A valid-looking RSS snapshot request cannot prove the route is RSS-only.
// Keep this real HTTP contract test so a future snapshot rollout cannot
// silently replace the existing mixed-content API response.
func TestExploreHTTPAlwaysMatchesLegacyMixedContentContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, db, user := newFeedTestService(t)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), service)
	token := signedFeedHTTPTokenForTest(t, db, user)

	cases := []struct {
		name, query string
		auth        bool
	}{
		{"anonymous recent dedup", "?page=1&page_size=100&sort=recent&hide_duplicates=true", false},
		{"anonymous second page", "?page=2&page_size=1&sort=recent&hide_duplicates=true", false},
		{"anonymous empty tail", "?page=500&page_size=2&sort=recent&hide_duplicates=true", false},
		{"anonymous popular", "?page=1&page_size=10&sort=popular&hide_duplicates=true", false},
		{"anonymous search", "?page=1&page_size=10&q=post", false},
		{"anonymous blog filter", "?content_type=blog&page=1&page_size=10", false},
		{"anonymous RSS filter", "?source_type=external_rss&page=1&page_size=10", false},
		{"authenticated recent dedup", "?page=1&page_size=100&sort=recent&hide_duplicates=true", true},
		{"authenticated read filter", "?is_read=false&page=1&page_size=10", true},
		{"authenticated unread alias", "?unread_only=true&page=1&page_size=10", true},
		{"authenticated popular", "?sort=popular&page=1&page_size=10", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := "/api/v1/feed/explore" + tc.query
			req := httptest.NewRequest(http.MethodGet, path, nil)
			viewer := authctx.CurrentUser{}
			if tc.auth {
				req.Header.Set("Authorization", "Bearer "+token)
				viewer = user
			}
			queryContext, _ := gin.CreateTestContext(httptest.NewRecorder())
			queryContext.Request = req
			query, err := queryFromContext(queryContext)
			if err != nil {
				t.Fatalf("parse expected legacy query: %v", err)
			}
			// Scope must be unknown for this mixed-content HTTP route.
			if eligiblePublicRecentDedupSnapshotRequest(queryContext, query, viewer, snapshotReadScopeUnknown) {
				t.Fatal("mixed-content explore route must not be admitted to RSS-only snapshot")
			}
			expected, total, err := service.GetExploreFeed(viewer, query)
			if err != nil {
				t.Fatalf("legacy service query: %v", err)
			}

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
			var got struct {
				Data []TimelineItemDTO `json:"data"`
				Meta httpx.PageMeta    `json:"meta"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode HTTP response: %v", err)
			}
			actualJSON, _ := json.Marshal(got.Data)
			expectedJSON, _ := json.Marshal(expected)
			if !bytes.Equal(actualJSON, expectedJSON) {
				t.Fatalf("HTTP differs from legacy service\nactual: %s\nexpected: %s", actualJSON, expectedJSON)
			}
			page, size := normalizedPageFromQuery(queryContext), normalizedPageSizeFromQuery(queryContext)
			wantMore := page > 0 && size > 0 && total > 0 && int64(page) <= (total-1)/int64(size)
			if got.Meta.Page != page || got.Meta.PageSize != size || got.Meta.Total != total || got.Meta.HasMore != wantMore {
				t.Fatalf("pagination meta mismatch: got %+v expected page=%d size=%d total=%d hasMore=%v", got.Meta, page, size, total, wantMore)
			}
			if tc.name == "anonymous recent dedup" && !strings.Contains(rec.Body.String(), "Post item") {
				t.Fatal(fmt.Sprintf("mixed explore lost published blog posts: %s", rec.Body.String()))
			}
		})
	}
}
