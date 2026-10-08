package blog

import (
	"atoman/internal/model"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPostSummaryListOmitsBodyAndKeepsMetadata(t *testing.T) {
	svc, db, user := newBlogHTTPTestService(t)
	channel, err := svc.CreateDefaultChannelForUser(user.ID, "Alice")
	require.NoError(t, err)
	post := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "文章标题", Summary: "摘要", Content: "large full article body", Status: "published", Visibility: "public"}
	require.NoError(t, db.Create(&post).Error)
	canonicalizeBlogTestPost(t, db, post)
	r := newBlogHTTPRouter(svc, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/blog/posts?view=summary", nil))
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "文章标题")
	require.Contains(t, w.Body.String(), "摘要")
	require.NotContains(t, w.Body.String(), "large full article body")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/blog/posts", nil))
	require.Contains(t, w.Body.String(), "large full article body")
}

func TestPostSummaryListUsesBoundedBodyExcerptWhenSummaryIsBlank(t *testing.T) {
	for _, test := range []struct {
		name    string
		summary string
		body    string
	}{
		{name: "short body", body: "未填写摘要时，列表应保留这段短正文作为摘录。"},
		{name: "long unicode body", body: strings.Repeat("正文", 300)},
		{name: "whitespace summary", summary: "   ", body: strings.Repeat("摘录", 250)},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, db, user := newBlogHTTPTestService(t)
			channel, err := svc.CreateDefaultChannelForUser(user.ID, "Alice")
			require.NoError(t, err)
			post := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "自动摘录", Summary: test.summary, Content: test.body, Status: "published", Visibility: "public"}
			require.NoError(t, db.Create(&post).Error)
			canonicalizeBlogTestPost(t, db, post)
			router := newBlogHTTPRouter(svc, nil)
			readList := func(path string) []model.Post {
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
				require.Equal(t, 200, response.Code, response.Body.String())
				var payload struct {
					Data []model.Post `json:"data"`
				}
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
				return payload.Data
			}
			items := readList("/api/v1/blog/posts?view=summary")
			require.Len(t, items, 1)
			require.Empty(t, items[0].Content)
			require.NotEmpty(t, items[0].Summary)
			require.LessOrEqual(t, utf8.RuneCountInString(items[0].Summary), 400)
			wantExcerpt := []rune(test.body)
			if len(wantExcerpt) > 400 {
				wantExcerpt = wantExcerpt[:400]
			}
			require.Equal(t, string(wantExcerpt), items[0].Summary)
			legacyItems := readList("/api/v1/blog/posts")
			require.Len(t, legacyItems, 1)
			require.Equal(t, test.body, legacyItems[0].Content)
		})
	}
}
