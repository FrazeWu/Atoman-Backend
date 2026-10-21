package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/books"
	"atoman/internal/modules/search"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBooksProviderUnifiedAPIContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Revision{}, &model.BookWork{}, &model.BookEdition{}, &model.BookPerson{}, &model.BookContribution{}, &model.BookRating{}, &model.BookList{}, &model.BookSource{})
	user := model.User{UUID: uuid.New(), Username: "reader", Email: "reader@example.test", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	bookService := books.NewService(db)
	for i := range 9 {
		_, err := bookService.CreateBookWork(context.Background(), authctx.CurrentUser{ID: user.UUID}, books.CreateBookWorkInput{Title: fmt.Sprintf("公开作品%02d", i), Authors: []string{fmt.Sprintf("公开作者%02d", i)}})
		require.NoError(t, err)
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		require.NoError(t, db.Create(&model.BookList{UserID: user.UUID, Version: 2, PublishedVersion: 1, Visibility: "public", PublishedAt: &now,
			DraftJSON: `{"title":"私人秘密"}`, PublishedJSON: fmt.Sprintf(`{"title":"公开书单%02d","description":"公共简介","items":[]}`, i)}).Error)
	}
	registry := search.NewRegistry()
	provider := NewBooksProvider(bookService)
	require.NoError(t, registry.Register(provider))
	service := search.NewService(registry)
	router := gin.New()
	search.RegisterRoutes(router.Group("/api/v1"), service)
	get := func(path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		return response
	}
	for _, view := range []string{"preview", "grouped", "list"} {
		for _, kind := range []string{"work", "author", "book_list"} {
			path := "/api/v1/search?q=公开&module=books&type=" + kind + "&view=" + view
			if view == "list" {
				path += "&page_size=2&page=2"
			}
			response := get(path)
			require.Equal(t, 200, response.Code, response.Body.String())
			require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
			var envelope search.SearchEnvelope
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
			result := envelope.Data
			require.False(t, result.Partial)
			items := result.Items
			if view == "list" {
				require.EqualValues(t, 9, result.Pagination.Total)
				require.True(t, result.Pagination.HasMore)
				require.Len(t, items, 2)
			} else {
				require.Len(t, result.Groups, 1)
				require.Nil(t, result.Groups[0].Total)
				require.True(t, result.Groups[0].HasMore)
				items = result.Groups[0].Items
				limit := 6
				if view == "preview" {
					limit = 2
				}
				require.Len(t, items, limit)
			}
			for _, item := range items {
				require.NoError(t, search.ValidateSearchHit(item))
				require.Equal(t, search.BuildSearchKey("books", kind, item.ID), item.Key)
				switch kind {
				case "author":
					u, err := url.Parse(item.Href)
					require.NoError(t, err)
					require.Equal(t, "/books/search", u.Path)
					require.Equal(t, item.ID, u.Query().Get("filter[author_id]"))
				case "work":
					require.Equal(t, "/books/work/"+item.ID, item.Href)
				case "book_list":
					require.Equal(t, "/books/lists/"+item.ID, item.Href)
					require.Equal(t, user.UUID.String(), item.Metadata["owner_id"])
				}
			}
			require.NotContains(t, response.Body.String(), "私人秘密")
			require.NotContains(t, response.Body.String(), "draft_json")
		}
	}
	for _, params := range []string{
		"type=work&filter[year_from]=2026.5", "type=work&filter[year_from]=0", "type=work&filter[year_to]=10000",
		"type=work&filter[author_id]=bad", "type=work&type=author&filter[language]=zh", "type=author&sort=rating_desc",
	} {
		response := get("/api/v1/search?q=公开&module=books&" + params)
		require.Equal(t, 400, response.Code, response.Body.String())
	}
	response := get("/api/v1/search?q=私人秘密&module=books&type=book_list&view=list")
	require.Equal(t, 200, response.Code, response.Body.String())
	var empty search.SearchEnvelope
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &empty))
	require.Zero(t, empty.Data.Pagination.Total)
	response = get("/api/v1/search/capabilities?module=books")
	require.Equal(t, 200, response.Code)
	var caps search.CapabilitiesEnvelope
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &caps))
	require.Len(t, caps.Data.Modules[0].Types, 3)
	require.Empty(t, caps.Data.Modules[0].Filters, "模块级筛选必须是三种类型的交集")
	// public 不因登录身份而增加私人结果；mine 在 P6 前没有图书能力。
	for _, viewer := range []search.Viewer{{}, {ID: user.UUID}, {ID: uuid.New(), Role: authctx.RoleAdmin}} {
		result, err := service.Search(context.Background(), viewer, search.SearchRequest{Query: "私人秘密", Module: "books", Types: []string{"book_list"}, View: search.ViewList})
		require.NoError(t, err)
		require.Zero(t, result.Pagination.Total)
	}
	require.Empty(t, provider.Capabilities(context.Background(), search.Viewer{ID: user.UUID}, search.ScopeMine).Types)
	_, err := provider.Search(context.Background(), search.Viewer{ID: user.UUID}, search.ProviderSearchRequest{Scope: search.ScopeMine, Type: "book_list"})
	require.Error(t, err)
}

func TestBooksProviderSnippetIsUnicodeSafe(t *testing.T) {
	value := ""
	for range 241 {
		value += "中"
	}
	require.Len(t, []rune(searchSnippet(value)), 241) // 240 字加省略号。
	require.Equal(t, "一 二", searchSnippet(" 一\n 二 "))
}
