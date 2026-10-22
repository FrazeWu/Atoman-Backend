package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/modules/books"
	"atoman/internal/modules/music"
	"atoman/internal/modules/search"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/authsession"
	"atoman/internal/testdb"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPrivateProvidersIsolationAndPagination(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.AuthSession{}, &model.Revision{}, &model.BookWork{}, &model.BookEdition{}, &model.BookPerson{}, &model.BookContribution{}, &model.BookRating{},
		&model.BookList{}, &model.BookListBookmark{}, &model.BookPrivateNote{}, &model.BookLibraryEntry{}, &model.Playlist{}, &model.PlaylistBookmark{})
	users := []model.User{
		{Username: "private-a", Email: "a@example.test", Password: "hash", IsActive: true, Role: "user"},
		{Username: "private-b", Email: "b@example.test", Password: "hash", IsActive: true, Role: "admin"},
	}
	for i := range users {
		require.NoError(t, db.Create(&users[i]).Error)
	}
	registry := search.NewRegistry()
	bookProvider, musicProvider := NewBooksProvider(books.NewService(db)), NewMusicProvider(music.NewService(db))
	require.NoError(t, registry.Register(bookProvider))
	require.NoError(t, registry.Register(musicProvider))
	service := search.NewService(registry)
	cases := []struct{ module, kind string }{{"books", "note"}, {"books", "book_list"}, {"books", "library"}, {"books", "bookmark"}, {"music", "playlist"}, {"music", "bookmark"}}
	// 每人每种结果八条，相同关键词但不同内容，管理员也不能扩大 mine。
	now := time.Now().UTC()
	for index, user := range users {
		for i := range 8 {
			title := fmt.Sprintf("共同检索-%d-%02d", index, i)
			work := model.BookWork{Title: title, LifecycleStatus: "active"}
			require.NoError(t, db.Create(&work).Error)
			require.NoError(t, db.Create(&model.Revision{ContentType: "book_work", ContentID: work.ID, VersionNumber: 1, ContentSnapshot: []byte(`{}`), EditorID: user.UUID, IsCurrent: true}).Error)
			require.NoError(t, db.Create(&model.BookPrivateNote{UserID: user.UUID, WorkID: work.ID, Title: title, Body: fmt.Sprintf("本人摘要%d", index), Version: 1}).Error)
			require.NoError(t, db.Create(&model.BookLibraryEntry{UserID: user.UUID, WorkID: work.ID, Status: "reading", Version: 1}).Error)
			list := model.BookList{UserID: user.UUID, Version: 1, DraftJSON: fmt.Sprintf(`{"title":%q,"description":"本人的草稿"}`, title)}
			require.NoError(t, db.Create(&list).Error)
			// 收藏自己有权读的已发布私有快照；草稿和发布标题分离。
			bookmarked := model.BookList{UserID: user.UUID, Version: 1, PublishedVersion: 1, Visibility: "private", PublishedAt: &now,
				DraftJSON: `{"title":"收藏不应检索草稿"}`, PublishedJSON: fmt.Sprintf(`{"title":%q,"description":"收藏发布摘要"}`, title)}
			require.NoError(t, db.Create(&bookmarked).Error)
			require.NoError(t, db.Create(&model.BookListBookmark{UserID: user.UUID, ListID: bookmarked.ID}).Error)
			playlist := model.Playlist{UserID: user.UUID, Name: title, Description: "私人歌单摘要"}
			require.NoError(t, db.Create(&playlist).Error)
			require.NoError(t, db.Create(&model.PlaylistBookmark{UserID: user.UUID, PlaylistID: playlist.ID}).Error)
		}
	}
	for index, user := range users {
		viewer := search.Viewer{ID: user.UUID, Role: user.Role}
		for _, c := range cases {
			t.Run(fmt.Sprintf("%d/%s/%s", index, c.module, c.kind), func(t *testing.T) {
				for _, view := range []search.View{search.ViewPreview, search.ViewGrouped, search.ViewList} {
					request := search.SearchRequest{Scope: search.ScopeMine, Module: c.module, Types: []string{c.kind}, Query: "共同检索", View: view}
					if view == search.ViewList {
						request.Page, request.PageSize = 2, 2
					}
					result, err := service.Search(context.Background(), viewer, request)
					require.NoError(t, err)
					require.False(t, result.Partial)
					items := result.Items
					if view == search.ViewList {
						require.EqualValues(t, 8, result.Pagination.Total)
						require.Len(t, items, 2)
						require.True(t, result.Pagination.HasMore)
					} else {
						require.Nil(t, result.Groups[0].Total)
						require.True(t, result.Groups[0].HasMore)
						items = result.Groups[0].Items
						if view == search.ViewPreview {
							require.Len(t, items, 2)
						} else {
							require.Len(t, items, 6)
						}
					}
					for _, hit := range items {
						require.Contains(t, hit.Title, fmt.Sprintf("共同检索-%d-", index))
						require.NoError(t, search.ValidateSearchHit(hit))
					}
				}
				// 空词仅在选定类型内发现；分页唯一排序稳定、跨页不重复。
				request := search.SearchRequest{Scope: search.ScopeMine, Module: c.module, Types: []string{c.kind}, View: search.ViewList, Sort: "recent", PageSize: 1}
				first, err := service.Search(context.Background(), viewer, request)
				require.NoError(t, err)
				request.Page = 2
				second, err := service.Search(context.Background(), viewer, request)
				require.NoError(t, err)
				require.NotEqual(t, first.Items[0].Key, second.Items[0].Key)
			})
		}
	}
	// Preview/grouped 的领域查询不可偷偷 COUNT。
	counts := 0
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:private-counts", func(tx *gorm.DB) {
		if strings.Contains(strings.ToLower(tx.Statement.SQL.String()), "count(") {
			counts++
		}
	}))
	for _, c := range cases {
		_, err := service.Search(context.Background(), search.Viewer{ID: users[0].UUID}, search.SearchRequest{Scope: search.ScopeMine, Module: c.module, Types: []string{c.kind}, Query: "共同检索", View: search.ViewPreview})
		require.NoError(t, err)
	}
	require.Zero(t, counts)

	// 真实 Cookie/API Token 认证、切换账号、拒绝伪造身份、缓存头。
	gin.SetMode(gin.TestMode)
	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
	router := gin.New()
	search.RegisterRoutes(router.Group("/api/v1"), service)
	for index, kind := range []string{authsession.KindWeb, authsession.KindAPI} {
		credentials, err := authsession.New(db).Create(users[index].UUID, kind)
		require.NoError(t, err)
		for _, c := range cases {
			path := "/api/v1/search?scope=mine&module=" + c.module + "&type=" + c.kind + "&view=list&q=" + url.QueryEscape("共同检索")
			req := httptest.NewRequest(http.MethodGet, path, nil)
			if kind == authsession.KindWeb {
				req.AddCookie(&http.Cookie{Name: middleware.AuthSessionCookieName, Value: credentials.Token})
			} else {
				req.Header.Set("Authorization", "Bearer "+credentials.Token)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
			require.Contains(t, response.Header().Get("Vary"), "Cookie, Authorization")
			var envelope search.SearchEnvelope
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
			for _, item := range envelope.Data.Items {
				require.Contains(t, item.Title, fmt.Sprintf("共同检索-%d-", index))
			}
		}
	}
	for _, path := range []string{"/api/v1/search?scope=mine&q=共同检索", "/api/v1/search/capabilities?scope=mine"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusUnauthorized, response.Code)
	}
	// 没有私人能力的匿名 Provider 也不能直接查询。
	require.Empty(t, bookProvider.Capabilities(context.Background(), search.Viewer{}, search.ScopeMine).Types)
	require.Empty(t, musicProvider.Capabilities(context.Background(), search.Viewer{}, search.ScopeMine).Types)
	_, err := books.NewService(db).SearchPrivateBooks(context.Background(), authctx.CurrentUser{}, "note", books.BookSearchOptions{})
	require.Error(t, err)
	for _, c := range cases {
		for _, keyword := range []string{"他人不存在的秘密", "%", "_", "' OR 1=1 --"} {
			result, err := service.Search(context.Background(), search.Viewer{ID: users[0].UUID}, search.SearchRequest{Scope: search.ScopeMine, Module: c.module, Types: []string{c.kind}, Query: keyword, View: search.ViewList})
			require.NoError(t, err)
			require.Zero(t, result.Pagination.Total)
			require.Empty(t, result.Items)
		}
	}
	// 同样的私有正文关键词，对另一个账号连数量也不可见。
	result, err := service.Search(context.Background(), search.Viewer{ID: users[0].UUID}, search.SearchRequest{Scope: search.ScopeMine, Module: "books", Types: []string{"note"}, Query: "本人摘要1", View: search.ViewList})
	require.NoError(t, err)
	require.Zero(t, result.Pagination.Total)
	result, err = service.Search(context.Background(), search.Viewer{ID: users[1].UUID}, search.SearchRequest{Scope: search.ScopeMine, Module: "books", Types: []string{"note"}, Query: "本人摘要1", View: search.ViewList})
	require.NoError(t, err)
	require.EqualValues(t, 8, result.Pagination.Total)
	for _, req := range []search.SearchRequest{
		{Scope: search.ScopeMine, Module: "books", Types: []string{"work"}, Query: "共同"},
		{Scope: search.ScopePublic, Module: "books", Types: []string{"note"}, Query: "共同"},
		{Scope: search.ScopeMine, Module: "music", Types: []string{"playlist"}, Query: "共同", Filters: map[string][]string{"user_id": {users[1].UUID.String()}}},
	} {
		_, err := service.Search(context.Background(), search.Viewer{ID: users[0].UUID}, req)
		require.Error(t, err)
	}
	// 书架结果遵守作品可见性，移出书架、关闭作品和已清除笔记均不再返回。
	var entries []model.BookLibraryEntry
	require.NoError(t, db.Where("user_id = ?", users[0].UUID).Order("id").Find(&entries).Error)
	require.NoError(t, db.Delete(&entries[0]).Error)
	require.NoError(t, db.Model(&model.BookWork{}).Where("id = ?", entries[1].WorkID).Update("edit_status", "closed").Error)
	result, err = service.Search(context.Background(), search.Viewer{ID: users[0].UUID}, search.SearchRequest{Scope: search.ScopeMine, Module: "books", Types: []string{"library"}, View: search.ViewList})
	require.NoError(t, err)
	require.EqualValues(t, 6, result.Pagination.Total)
	require.NoError(t, db.Model(&model.BookPrivateNote{}).Where("user_id = ?", users[0].UUID).Update("purged_at", now).Error)
	result, err = service.Search(context.Background(), search.Viewer{ID: users[0].UUID}, search.SearchRequest{Scope: search.ScopeMine, Module: "books", Types: []string{"note"}, View: search.ViewList})
	require.NoError(t, err)
	require.Zero(t, result.Pagination.Total)
}

func TestPrivateBookmarksRecheckVisibilityAndDeletion(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.BookList{}, &model.BookListBookmark{}, &model.Playlist{}, &model.PlaylistBookmark{}, &model.BookPrivateNote{})
	owner, reader := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{owner, reader} {
		require.NoError(t, db.Create(&model.User{UUID: id, Username: id.String(), Email: id.String() + "@example.test", Password: "hash", IsActive: true}).Error)
	}
	now := time.Now().UTC()
	list := model.BookList{UserID: owner, Version: 1, Visibility: "public", PublishedAt: &now, DraftJSON: `{"title":"草稿绝密"}`, PublishedJSON: `{"title":"公开快照"}`}
	require.NoError(t, db.Create(&list).Error)
	require.NoError(t, db.Create(&model.BookListBookmark{UserID: reader, ListID: list.ID}).Error)
	playlist := model.Playlist{UserID: owner, Name: "公开歌单", IsPublic: true}
	require.NoError(t, db.Create(&playlist).Error)
	bookmark := model.PlaylistBookmark{UserID: reader, PlaylistID: playlist.ID}
	require.NoError(t, db.Create(&bookmark).Error)
	bookService, musicService := books.NewService(db), music.NewService(db)
	user := authctx.CurrentUser{ID: reader, Role: authctx.RoleAdmin}
	assertCounts := func(bookCount, musicCount int64) {
		b, err := bookService.SearchPrivateBooks(context.Background(), user, "bookmark", books.BookSearchOptions{ExactTotal: true})
		require.NoError(t, err)
		require.Equal(t, bookCount, *b.Total)
		require.Len(t, b.Items, int(bookCount))
		m, err := musicService.SearchPrivatePlaylists(context.Background(), user, true, music.MusicSearchOptions{ExactTotal: true}, "recent")
		require.NoError(t, err)
		require.Equal(t, musicCount, *m.Total)
		require.Len(t, m.Items, int(musicCount))
	}
	assertCounts(1, 1)
	privateDraft, err := bookService.SearchPrivateBooks(context.Background(), user, "bookmark", books.BookSearchOptions{Query: "草稿绝密", ExactTotal: true})
	require.NoError(t, err)
	require.Zero(t, *privateDraft.Total)
	require.NoError(t, db.Model(&list).Update("visibility", "private").Error)
	require.NoError(t, db.Model(&playlist).Update("is_public", false).Error)
	assertCounts(0, 0)
	require.NoError(t, db.Model(&list).Update("visibility", "public").Error)
	require.NoError(t, db.Model(&playlist).Update("is_public", true).Error)
	require.NoError(t, db.Delete(&bookmark).Error)
	assertCounts(1, 0)
	require.NoError(t, db.Delete(&list).Error)
	assertCounts(0, 0)
	require.NoError(t, db.Unscoped().Model(&bookmark).Update("deleted_at", nil).Error)
	require.NoError(t, db.Delete(&playlist).Error)
	assertCounts(0, 0)
	note := model.BookPrivateNote{UserID: reader, WorkID: uuid.New(), Title: "删除笔记", Version: 1}
	require.NoError(t, db.Create(&note).Error)
	require.NoError(t, db.Delete(&note).Error)
	result, err := bookService.SearchPrivateBooks(context.Background(), user, "note", books.BookSearchOptions{ExactTotal: true})
	require.NoError(t, err)
	require.Zero(t, *result.Total)
	longNote := model.BookPrivateNote{UserID: reader, WorkID: uuid.New(), Title: "长笔记", Body: strings.Repeat("中", 300) + "PRIVATE_TAIL", Version: 1}
	require.NoError(t, db.Create(&longNote).Error)
	result, err = bookService.SearchPrivateBooks(context.Background(), user, "note", books.BookSearchOptions{Query: "PRIVATE_TAIL", ExactTotal: true})
	require.NoError(t, err)
	require.EqualValues(t, 1, *result.Total)
	require.Len(t, []rune(result.Items[0].Snippet), 240)
	require.NotContains(t, result.Items[0].Snippet, "PRIVATE_TAIL")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = bookService.SearchPrivateBooks(cancelled, user, "note", books.BookSearchOptions{})
	require.ErrorIs(t, err, context.Canceled)
	_, err = musicService.SearchPrivatePlaylists(cancelled, user, false, music.MusicSearchOptions{}, "recent")
	require.ErrorIs(t, err, context.Canceled)
}
