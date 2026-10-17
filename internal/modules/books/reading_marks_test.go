package books

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func txtBookHighlight(start, end int64, note string) SaveBookReadingMarkInput {
	return SaveBookReadingMarkInput{Kind: "highlight", Anchor: BookReadingMarkAnchor{TXTOffsetStart: start, TXTOffsetEnd: end}, Quote: "选中文本", Note: note}
}

func TestBookRevisionMarksMergeWithoutLosingNotes(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	_, revision := readyBookReadingFile(t, service, user, uuid.MustParse(work.ID), "zh", "txt")
	var saved []BookReadingMarkDTO
	// Last interval bridges the first two; grouping must be transitive.
	for _, input := range []SaveBookReadingMarkInput{txtBookHighlight(0, 10, "想法甲"), txtBookHighlight(20, 30, "想法乙"), txtBookHighlight(5, 25, "想法丙")} {
		id := uuid.New()
		item, err := service.SaveBookReadingMark(ctx, user, revision.ID, id, input)
		require.NoError(t, err)
		require.EqualValues(t, 1, item.Version)
		saved = append(saved, item)
		retry, err := service.SaveBookReadingMark(ctx, user, revision.ID, id, input)
		require.NoError(t, err)
		require.EqualValues(t, 1, retry.Version)
	}
	bookmark, err := service.SaveBookReadingMark(ctx, user, revision.ID, uuid.New(), SaveBookReadingMarkInput{Kind: "bookmark", Label: "重点", Anchor: BookReadingMarkAnchor{TXTOffsetStart: 12}})
	require.NoError(t, err)
	blue := txtBookHighlight(8, 15, "蓝色想法")
	blue.Color = "blue"
	_, err = service.SaveBookReadingMark(ctx, user, revision.ID, uuid.New(), blue)
	require.NoError(t, err)
	list, err := service.ListBookReadingMarks(ctx, user, revision.ID)
	require.NoError(t, err)
	require.Len(t, list.Items, 5)
	require.Len(t, list.HighlightGroups, 2)
	require.Len(t, list.HighlightGroups[0].MemberIDs, 3)
	require.EqualValues(t, 0, list.HighlightGroups[0].Anchor.TXTOffsetStart)
	require.EqualValues(t, 30, list.HighlightGroups[0].Anchor.TXTOffsetEnd)
	encoded, err := json.Marshal(list)
	require.NoError(t, err)
	for _, note := range []string{"想法甲", "想法乙", "想法丙", "蓝色想法"} {
		require.Contains(t, string(encoded), note)
	}
	require.NotContains(t, string(encoded), user.ID.String())
	// Deleting only the bridge splits the visual group, not the source notes.
	_, err = service.DeleteBookReadingMark(ctx, user, revision.ID, saved[2].ID, 1)
	require.NoError(t, err)
	list, err = service.ListBookReadingMarks(ctx, user, revision.ID)
	require.NoError(t, err)
	require.Len(t, list.HighlightGroups, 3)
	require.Len(t, list.Deleted, 1)
	require.Len(t, list.Items, 4)
	require.Equal(t, bookmark.ID, list.Items[2].ID)
}

func TestBookRevisionMarksConflictIsolationAndTakedown(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	workID := uuid.MustParse(work.ID)
	asset, old := readyBookReadingFile(t, service, user, workID, "zh", "txt")
	_, newRevision := readyBookReadingFile(t, service, user, workID, "zh", "txt")
	id := uuid.New()
	input := txtBookHighlight(1, 10, "私有笔记")
	mark, err := service.SaveBookReadingMark(ctx, user, old.ID, id, input)
	require.NoError(t, err)
	input.BaseVersion = 1
	input.Note = "修订后的笔记"
	mark, err = service.SaveBookReadingMark(ctx, user, old.ID, id, input)
	require.NoError(t, err)
	require.EqualValues(t, 2, mark.Version)
	input.Note = "另一设备过期内容"
	_, err = service.SaveBookReadingMark(ctx, user, old.ID, id, input)
	require.Equal(t, "books.reading_mark_conflict", apperr.FromError(err).Code)
	_, err = service.DeleteBookReadingMark(ctx, user, old.ID, id, 1)
	require.Equal(t, "books.reading_mark_conflict", apperr.FromError(err).Code)
	list, err := service.ListBookReadingMarks(ctx, user, newRevision.ID)
	require.NoError(t, err)
	require.Empty(t, list.Items)
	_, err = service.SaveBookReadingMark(ctx, user, newRevision.ID, id, input)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	other := model.User{UUID: uuid.New(), Username: "mark-reader", Email: "mark-reader@example.test", Password: "hash", IsActive: true}
	require.NoError(t, service.db.Create(&other).Error)
	otherUser := authctx.CurrentUser{ID: other.UUID, Role: authctx.RoleUser}
	list, err = service.ListBookReadingMarks(ctx, otherUser, old.ID)
	require.NoError(t, err)
	require.Empty(t, list.Items)
	_, err = service.DeleteBookReadingMark(ctx, otherUser, old.ID, id, 2)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	input.BaseVersion = 0
	_, err = service.SaveBookReadingMark(ctx, otherUser, old.ID, id, input)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	require.NoError(t, service.db.Model(&asset).Update("status", "removed").Error)
	list, err = service.ListBookReadingMarks(ctx, user, old.ID)
	require.NoError(t, err)
	require.Equal(t, "修订后的笔记", list.Items[0].Note)
	input.BaseVersion = 2
	input.Note = "下架后仍可整理自己的笔记"
	_, err = service.SaveBookReadingMark(ctx, user, old.ID, id, input)
	require.NoError(t, err)
	input.BaseVersion = 0
	_, err = service.SaveBookReadingMark(ctx, user, old.ID, uuid.New(), input)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	tombstone, err := service.DeleteBookReadingMark(ctx, user, old.ID, id, 3)
	require.NoError(t, err)
	require.EqualValues(t, 4, tombstone.Version)
	retry, err := service.DeleteBookReadingMark(ctx, user, old.ID, id, 3)
	require.NoError(t, err)
	require.Equal(t, tombstone, retry)
	_, err = service.SaveBookReadingMark(ctx, user, old.ID, id, input)
	require.Equal(t, "books.reading_mark_conflict", apperr.FromError(err).Code)
	list, err = service.ListBookReadingMarks(ctx, user, old.ID)
	require.NoError(t, err)
	require.Empty(t, list.Items)
	require.Len(t, list.Deleted, 1)
}

func TestBookRevisionMarksConcurrentWrites(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	_, revision := readyBookReadingFile(t, service, user, uuid.MustParse(work.ID), "zh", "txt")
	id := uuid.New()
	input := txtBookHighlight(0, 5, "原始想法")
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := service.SaveBookReadingMark(ctx, user, revision.ID, id, input)
			results <- err
		}()
	}
	close(start)
	for range 2 {
		require.NoError(t, <-results)
	}
	list, err := service.ListBookReadingMarks(ctx, user, revision.ID)
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.EqualValues(t, 1, list.Items[0].Version)
	start = make(chan struct{})
	for _, note := range []string{"设备甲", "设备乙"} {
		go func(note string) {
			<-start
			edit := input
			edit.BaseVersion = 1
			edit.Note = note
			_, err := service.SaveBookReadingMark(ctx, user, revision.ID, id, edit)
			results <- err
		}(note)
	}
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else {
			require.Equal(t, "books.reading_mark_conflict", apperr.FromError(err).Code)
			conflicts++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	list, err = service.ListBookReadingMarks(ctx, user, revision.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, list.Items[0].Version)
}

func TestBookRevisionMarkAnchorsAndCFISorting(t *testing.T) {
	for _, pair := range [][2]string{
		{"epubcfi(/6/2!/4/2/1:0)", "epubcfi(/6/10!/4/2/1:0)"},
		{"epubcfi(/6/2!/4/2/1:2)", "epubcfi(/6/2!/4/2/1:10)"},
		{"epubcfi(/6/2!/4/2/1:2)", "epubcfi(/6/2!/4/10/1:0)"},
	} {
		for _, cfi := range pair {
			_, err := parseBookPointCFI(cfi)
			require.NoError(t, err)
		}
		require.Negative(t, compareBookPointCFI(pair[0], pair[1]))
	}
	a := "epubcfi(/6/2[chapter^]one]!/4/2/1:5[abc^,def,ghi])"
	_, err := parseBookPointCFI(a)
	require.NoError(t, err)
	require.Zero(t, compareBookPointCFI(a, "epubcfi(/6/2!/4/2/1:5)"))
	for _, cfi := range []string{"epubcfi(/6/2!/4,/2/1:0,/2/1:3)", "epubcfi(/6/2[bad]!/4/1:-1)", "epubcfi(/6/2[bad!/4/1:0)", "epubcfi(/6/0!/4/1:0)", "epubcfi(/6/2!/4/1:0~3)", "epubcfi(/6/2!/4/1:0/2)", "epubcfi(/6/2!/4/1:99999999999999999999999)"} {
		_, err := parseBookPointCFI(cfi)
		require.Error(t, err, cfi)
	}
	valid := []struct {
		format, kind string
		anchor       BookReadingMarkAnchor
	}{
		{"txt", "bookmark", BookReadingMarkAnchor{}},
		{"txt", "highlight", BookReadingMarkAnchor{TXTOffsetStart: 0, TXTOffsetEnd: 2}},
		{"epub", "bookmark", BookReadingMarkAnchor{EPUBStartCFI: a}},
		{"epub", "highlight", BookReadingMarkAnchor{EPUBStartCFI: "epubcfi(/6/2!/4/1:0)", EPUBEndCFI: "epubcfi(/6/2!/4/1:3)"}},
		{"pdf", "bookmark", BookReadingMarkAnchor{PDFPage: 2}},
		{"pdf", "highlight", BookReadingMarkAnchor{PDFPage: 2, PDFRects: []BookPDFRect{{X: .1, Y: .1, Width: .2, Height: .1}}}},
	}
	for _, test := range valid {
		require.NoError(t, validateBookReadingMarkAnchor(test.anchor, test.format, test.kind))
	}
	invalid := []struct {
		format, kind string
		anchor       BookReadingMarkAnchor
	}{
		{"txt", "highlight", BookReadingMarkAnchor{TXTOffsetStart: 3, TXTOffsetEnd: 2}},
		{"txt", "highlight", BookReadingMarkAnchor{TXTOffsetStart: -1, TXTOffsetEnd: 2}},
		{"txt", "bookmark", BookReadingMarkAnchor{TXTOffsetEnd: 2}},
		{"txt", "bookmark", BookReadingMarkAnchor{PDFPage: 1}},
		{"pdf", "bookmark", BookReadingMarkAnchor{}},
		{"pdf", "highlight", BookReadingMarkAnchor{PDFPage: 1}},
		{"pdf", "highlight", BookReadingMarkAnchor{PDFPage: 1, PDFRects: []BookPDFRect{{X: math.NaN(), Width: .1, Height: .1}}}},
		{"pdf", "highlight", BookReadingMarkAnchor{PDFPage: 1, PDFRects: []BookPDFRect{{X: .9, Width: .2, Height: .1}}}},
		{"epub", "highlight", BookReadingMarkAnchor{EPUBStartCFI: a, EPUBEndCFI: a}},
	}
	for _, test := range invalid {
		require.Error(t, validateBookReadingMarkAnchor(test.anchor, test.format, test.kind))
	}
}

func TestBookPDFHighlightGroupsKeepExactGeometry(t *testing.T) {
	makeItem := func(page int, rects ...BookPDFRect) BookReadingMarkDTO {
		return BookReadingMarkDTO{BookReadingMark: model.BookReadingMark{Base: model.Base{ID: uuid.New()}, Kind: "highlight", Color: "yellow", Style: "highlight"}, Anchor: BookReadingMarkAnchor{PDFPage: page, PDFRects: rects}}
	}
	items := []BookReadingMarkDTO{
		makeItem(1, BookPDFRect{X: .1, Y: .1, Width: .2, Height: .1}, BookPDFRect{X: .1, Y: .8, Width: .2, Height: .1}),
		makeItem(1, BookPDFRect{X: .2, Y: .1, Width: .2, Height: .1}),
		makeItem(1, BookPDFRect{X: .2, Y: .5, Width: .1, Height: .1}),
		makeItem(2, BookPDFRect{X: .1, Y: .1, Width: .2, Height: .1}),
	}
	groups := groupBookHighlights(items, "pdf")
	require.Len(t, groups, 3)
	require.Len(t, groups[0].MemberIDs, 2)
	require.Len(t, groups[0].Anchor.PDFRects, 3)
	require.Len(t, items[0].Anchor.PDFRects, 2, "grouping must not modify stored anchors")
}

func TestBookRevisionMarksHTTP(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	_, revision := readyBookReadingFile(t, service, user, uuid.MustParse(work.ID), "zh", "txt")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { authctx.SetCurrentUser(c, user); c.Next() })
	RegisterRoutes(router.Group("/api/v1/books"), service)
	url := "/api/v1/books/me/reading-marks/" + revision.ID.String()
	id := uuid.NewString()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
		return w
	}
	body := `{"kind":"highlight","base_version":0,"anchor":{"txt_offset_start":0,"txt_offset_end":12},"quote":"划线","note":"私有想法"}`
	w := request(http.MethodPut, url+"/"+id, body)
	require.Equal(t, 200, w.Code, w.Body.String())
	w = request(http.MethodGet, url, "")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), "私有想法")
	require.Contains(t, w.Body.String(), "highlight_groups")
	w = request(http.MethodDelete, url+"/"+id, "")
	require.Equal(t, 400, w.Code)
	w = request(http.MethodDelete, url+"/"+id+"?base_version=1", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	w = request(http.MethodPut, url+"/"+id, body)
	require.Equal(t, 409, w.Code)
	w = request(http.MethodGet, url, "")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"items":[]`)
	require.Contains(t, w.Body.String(), `"version":2`)
}
