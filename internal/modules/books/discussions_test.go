package books

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/modules/comment"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBookDiscussionPassagesAreRevisionPinnedAndDeduplicated(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	asset, revision := readyBookReadingFile(t, service, user, uuid.MustParse(work.ID), "zh", "txt")
	input := CreateBookDiscussionPassageInput{Anchor: BookReadingMarkAnchor{TXTOffsetStart: 10, TXTOffsetEnd: 20}}
	start := make(chan struct{})
	results := make(chan BookDiscussionPassageDTO, 2)
	errors := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			result, err := service.CreateBookDiscussionPassage(ctx, user, revision.ID, input)
			results <- result
			errors <- err
		}()
	}
	close(start)
	for range 2 {
		require.NoError(t, <-errors)
	}
	first, second := <-results, <-results
	require.Equal(t, first.ID, second.ID)
	require.Equal(t, "book_passage", first.TargetKind)
	other := newBookLibraryTestReader(t, service)
	same, err := service.CreateBookDiscussionPassage(ctx, other, revision.ID, input)
	require.NoError(t, err)
	require.Equal(t, first.ID, same.ID, "all readers share the same selected range discussion")
	// Updating the current file cannot move a discussion to the replacement.
	media := model.MediaAsset{Key: "private/replacement", Purpose: "books.file", ContentType: "text/plain", Size: 100}
	require.NoError(t, service.db.Create(&media).Error)
	replacement := model.BookAssetRevision{AssetID: asset.ID, Number: 2, MediaAssetID: media.ID, SHA256: strings.Repeat("b", 64), OriginalFilename: "new.txt", Status: "ready", CreatedBy: user.ID}
	require.NoError(t, service.db.Create(&replacement).Error)
	require.NoError(t, service.db.Model(&asset).Update("current_revision_id", replacement.ID).Error)
	newPassage, err := service.CreateBookDiscussionPassage(ctx, user, replacement.ID, input)
	require.NoError(t, err)
	require.NotEqual(t, first.ID, newPassage.ID)
	stored, err := service.GetBookDiscussionPassage(ctx, first.ID)
	require.NoError(t, err)
	require.Equal(t, revision.ID, stored.RevisionID)
	list, err := service.ListBookDiscussionPassages(ctx, revision.ID, 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, list.Total)
	require.Equal(t, first.ID, list.Items[0].ID)
	encoded, err := json.Marshal(first)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), user.ID.String())
	require.NotContains(t, string(encoded), "quote")
	require.NotContains(t, string(encoded), "note")
	registry := comment.NewTargetRegistry(service.db)
	resolved, err := registry.Resolve(comment.Viewer{}, comment.TargetRef{Kind: first.TargetKind, ResourceID: first.ID})
	require.NoError(t, err)
	require.True(t, resolved.Visible)
	require.Nil(t, resolved.OwnerID, "the first reader must not own community discussion moderation")
	for _, status := range []string{"scanning", "quarantined", "removed"} {
		require.NoError(t, service.db.Model(&model.BookAssetRevision{}).Where("id = ?", revision.ID).Update("status", status).Error)
		_, err = service.GetBookDiscussionPassage(ctx, first.ID)
		require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
		_, err = registry.Resolve(comment.Viewer{}, comment.TargetRef{Kind: first.TargetKind, ResourceID: first.ID})
		require.ErrorIs(t, err, comment.ErrTargetNotFound)
	}
}

func TestBookDiscussionPassagesValidateFormatsAndSurviveMergeUndo(t *testing.T) {
	service, user, source, target := newBookMergeTestService(t)
	ctx := context.Background()
	for _, test := range []struct {
		format string
		anchor BookReadingMarkAnchor
	}{
		{"txt", BookReadingMarkAnchor{TXTOffsetStart: 0, TXTOffsetEnd: 5}},
		{"epub", BookReadingMarkAnchor{EPUBStartCFI: " epubcfi(/6/2!/4/1:0) ", EPUBEndCFI: "epubcfi(/6/2!/4/1:5)"}},
		{"pdf", BookReadingMarkAnchor{PDFPage: 1, PDFRects: []BookPDFRect{{X: .1, Y: .1, Width: .2, Height: .2}}}},
	} {
		_, revision := readyBookReadingFile(t, service, user, uuid.MustParse(source.ID), "zh", test.format)
		_, err := service.CreateBookDiscussionPassage(ctx, user, revision.ID, CreateBookDiscussionPassageInput{})
		require.Equal(t, 400, apperr.FromError(err).HTTPStatus)
		passage, err := service.CreateBookDiscussionPassage(ctx, user, revision.ID, CreateBookDiscussionPassageInput{Anchor: test.anchor})
		require.NoError(t, err)
		merge := mergeBookTestWorks(t, service, user, source, target)
		loaded, err := service.GetBookDiscussionPassage(ctx, passage.ID)
		require.NoError(t, err)
		require.Equal(t, revision.ID, loaded.RevisionID)
		_, err = service.RevertBookWorkMerge(ctx, user, merge.ID)
		require.NoError(t, err)
		loaded, err = service.GetBookDiscussionPassage(ctx, passage.ID)
		require.NoError(t, err)
		require.Equal(t, passage.ID, loaded.ID)
		source, err = service.GetBookWork(ctx, uuid.MustParse(source.ID))
		require.NoError(t, err)
		target, err = service.GetBookWork(ctx, uuid.MustParse(target.ID))
		require.NoError(t, err)
	}
}

func TestBookDiscussionPassageHTTP(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	_, revision := readyBookReadingFile(t, service, user, uuid.MustParse(work.ID), "zh", "txt")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { authctx.SetCurrentUser(c, user) })
	RegisterRoutes(router.Group("/api/v1/books"), service)
	url := "/api/v1/books/asset-revisions/" + revision.ID.String() + "/passages"
	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{"anchor":{"txt_offset_start":0,"txt_offset_end":5},"note":"不应公开的私有内容"}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.NotContains(t, response.Body.String(), "不应公开")
	var payload struct {
		Data BookDiscussionPassageDTO `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	for _, path := range []string{url, "/api/v1/books/passages/" + payload.Data.ID.String()} {
		response = httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, 200, response.Code, response.Body.String())
	}
}
