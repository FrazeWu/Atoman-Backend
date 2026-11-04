package feed

import (
	"net/http/httptest"
	"testing"
	"time"

	"atoman/internal/platform/authctx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestSnapshotReadGateDenyByDefaultHTTPQueryMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := FeedQuery{Page: 1, PageSize: 20, HideDuplicates: true, Sort: "recent"}
	guest := authctx.CurrentUser{}
	check := func(raw string, q FeedQuery, u authctx.CurrentUser, scope snapshotReadScope) bool {
		t.Helper()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/api/v1/feed/explore"+raw, nil)
		return eligiblePublicRecentDedupSnapshotRequest(c, q, u, scope)
	}
	if !check("?page=1&page_size=20&sort=recent&hide_duplicates=true", base, guest, snapshotReadScopeVerifiedPublicRSSOnly) {
		t.Fatal("verified RSS-only guest request rejected")
	}
	cases := []struct {
		name, url string
		modify    func(*FeedQuery, *authctx.CurrentUser, *snapshotReadScope)
	}{
		{"unknown provenance", "?hide_duplicates=true", func(q *FeedQuery, u *authctx.CurrentUser, s *snapshotReadScope) { *s = snapshotReadScopeUnknown }},
		{"authenticated user", "?hide_duplicates=true", func(q *FeedQuery, u *authctx.CurrentUser, s *snapshotReadScope) { u.ID = uuid.New() }},
		{"authenticated role", "?hide_duplicates=true", func(q *FeedQuery, u *authctx.CurrentUser, s *snapshotReadScope) { u.Role = authctx.RoleUser }},
		{"dedupe off", "", func(q *FeedQuery, u *authctx.CurrentUser, s *snapshotReadScope) { q.HideDuplicates = false }},
		{"search", "?q=needle", func(q *FeedQuery, u *authctx.CurrentUser, s *snapshotReadScope) { q.Search = "needle" }},
		{"source filter", "?source_id=foo", func(q *FeedQuery, u *authctx.CurrentUser, s *snapshotReadScope) { q.SourceID = uuid.New() }},
		{"group filter", "?group_id=foo", func(q *FeedQuery, u *authctx.CurrentUser, s *snapshotReadScope) { q.GroupID = uuid.New() }},
		{"read false", "?is_read=false", func(q *FeedQuery, u *authctx.CurrentUser, s *snapshotReadScope) { v := false; q.IsRead = &v }},
		{"category", "?category=technology", func(q *FeedQuery, u *authctx.CurrentUser, s *snapshotReadScope) { q.Category = "technology" }},
		{"language", "?language=zh", func(q *FeedQuery, u *authctx.CurrentUser, s *snapshotReadScope) { q.LanguageCode = "zh" }},
		{"content type", "?content_type=post", func(q *FeedQuery, u *authctx.CurrentUser, s *snapshotReadScope) { q.ContentType = "post" }},
		{"source type", "?source_type=external_rss", func(q *FeedQuery, u *authctx.CurrentUser, s *snapshotReadScope) { q.SourceType = "external_rss" }},
		{"popular", "?sort=popular", func(q *FeedQuery, u *authctx.CurrentUser, s *snapshotReadScope) { q.Sort = "popular" }},
		{"random", "?sort=random", func(q *FeedQuery, u *authctx.CurrentUser, s *snapshotReadScope) { q.Sort = "random" }},
		{"viewer", "", func(q *FeedQuery, u *authctx.CurrentUser, s *snapshotReadScope) { q.viewerID = uuid.New() }},
		{"hidden after", "", func(q *FeedQuery, u *authctx.CurrentUser, s *snapshotReadScope) {
			q.sourceVisibleAfter = map[uuid.UUID]time.Time{uuid.New(): time.Now()}
		}},
		{"unknown raw param", "?future_filter=x", nil},
		{"duplicate raw key", "?page=1&page=2", nil},
		{"legacy unread alias", "?unread_only=true", nil},
		{"sort normalized away", "?sort=POPULAR", nil},
		{"raw hide false", "?hide_duplicates=false", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := base
			u := guest
			scope := snapshotReadScopeVerifiedPublicRSSOnly
			if tc.modify != nil {
				tc.modify(&q, &u, &scope)
			}
			if check(tc.url, q, u, scope) {
				t.Fatalf("unsafe request admitted: %s", tc.url)
			}
		})
	}
}
