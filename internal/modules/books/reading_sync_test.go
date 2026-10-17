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

func readyBookReadingFile(t *testing.T, service *Service, user authctx.CurrentUser, workID uuid.UUID, lang, format string) (model.BookAsset, model.BookAssetRevision) {
	t.Helper()
	asset, err := service.CreateBookResource(context.Background(), user, workID, CreateBookResourceInput{Language: lang, Format: format})
	require.NoError(t, err)
	media := model.MediaAsset{Key: "private/" + uuid.NewString(), Purpose: "books.file", ContentType: "text/plain", Size: 100}
	require.NoError(t, service.db.Create(&media).Error)
	revision := model.BookAssetRevision{AssetID: asset.ID, Number: 1, MediaAssetID: media.ID, SHA256: strings.Repeat("a", 64), OriginalFilename: "file." + format, Status: "ready", CreatedBy: user.ID}
	require.NoError(t, service.db.Create(&revision).Error)
	require.NoError(t, service.db.Model(&asset).Update("current_revision_id", revision.ID).Error)
	asset.CurrentRevisionID = &revision.ID
	return asset, revision
}

func newBookCloudTestSession(t *testing.T, service *Service, user authctx.CurrentUser, revisionID uuid.UUID, device string) BookReadingSessionDTO {
	t.Helper()
	input := CreateBookReadingSessionInput{ClientSessionID: uuid.New(), RevisionID: revisionID, DeviceID: device}
	session, err := service.CreateBookReadingSession(context.Background(), user, input)
	require.NoError(t, err)
	retried, err := service.CreateBookReadingSession(context.Background(), user, input)
	require.NoError(t, err)
	require.Equal(t, session.Session.ID, retried.Session.ID)
	input.DeviceID += "other"
	_, err = service.CreateBookReadingSession(context.Background(), user, input)
	require.Equal(t, "books.session_id_reused", apperr.FromError(err).Code)
	return session
}

func TestBookCloudReadingBacktrackingRetriesAndOutOfOrder(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	_, revision := readyBookReadingFile(t, service, user, uuid.MustParse(work.ID), "zh", "txt")
	session := newBookCloudTestSession(t, service, user, revision.ID, "web-a")
	first := SaveBookCloudReadingStateInput{SessionID: session.Session.ID, RequestID: uuid.New(), Sequence: 2, TXTOffset: 80, CurrentPercent: .8}
	result, err := service.SaveBookCloudReadingState(ctx, user, revision.ID, first)
	require.NoError(t, err)
	require.True(t, result.PositionApplied)
	require.EqualValues(t, 1, result.State.Version)
	back := SaveBookCloudReadingStateInput{SessionID: session.Session.ID, RequestID: uuid.New(), Sequence: 3, BaseVersion: 1, TXTOffset: 20, CurrentPercent: .2}
	result, err = service.SaveBookCloudReadingState(ctx, user, revision.ID, back)
	require.NoError(t, err)
	require.Equal(t, .2, result.State.CurrentPercent)
	require.Equal(t, .8, result.State.FurthestPercent)
	retried, err := service.SaveBookCloudReadingState(ctx, user, revision.ID, first)
	require.NoError(t, err)
	require.True(t, retried.Duplicate)
	require.EqualValues(t, 1, retried.State.Version)
	current, err := service.GetBookCloudReadingState(ctx, user, revision.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, current.Version)
	require.Equal(t, .2, current.CurrentPercent)
	first.CurrentPercent = .4
	_, err = service.SaveBookCloudReadingState(ctx, user, revision.ID, first)
	require.Equal(t, "books.request_id_reused", apperr.FromError(err).Code)
	old := SaveBookCloudReadingStateInput{SessionID: session.Session.ID, RequestID: uuid.New(), Sequence: 1, TXTOffset: 90, CurrentPercent: .9}
	result, err = service.SaveBookCloudReadingState(ctx, user, revision.ID, old)
	require.NoError(t, err)
	require.False(t, result.PositionApplied)
	require.Equal(t, "out_of_order", result.IgnoredReason)
	require.Equal(t, .2, result.State.CurrentPercent)
	require.Equal(t, .9, result.State.FurthestPercent)
	require.EqualValues(t, 3, result.State.Version)
	_, err = service.SaveBookCloudReadingState(ctx, user, revision.ID, SaveBookCloudReadingStateInput{SessionID: session.Session.ID, RequestID: uuid.New(), Sequence: 4, BaseVersion: 2, CurrentPercent: .3})
	require.Equal(t, "books.reading_state_conflict", apperr.FromError(err).Code)
	var count int64
	require.NoError(t, service.db.Model(&model.BookReadingWrite{}).Count(&count).Error)
	require.EqualValues(t, 3, count)
}

func TestBookCloudReadingConcurrentDevicesAndPrivacy(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	asset, revision := readyBookReadingFile(t, service, user, uuid.MustParse(work.ID), "zh", "txt")
	a := newBookCloudTestSession(t, service, user, revision.ID, "web-a")
	b := newBookCloudTestSession(t, service, user, revision.ID, "web-b")
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, session := range []BookReadingSessionDTO{a, b} {
		go func(id uuid.UUID) {
			<-start
			_, err := service.SaveBookCloudReadingState(ctx, user, revision.ID, SaveBookCloudReadingStateInput{SessionID: id, RequestID: uuid.New(), Sequence: 1, TXTOffset: 30, CurrentPercent: .3})
			results <- err
		}(session.Session.ID)
	}
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else {
			require.Equal(t, "books.reading_state_conflict", apperr.FromError(err).Code)
			conflicts++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	other := model.User{UUID: uuid.New(), Username: "reader-other", Email: "reader-other@example.test", Password: "hash", IsActive: true}
	require.NoError(t, service.db.Create(&other).Error)
	otherUser := authctx.CurrentUser{ID: other.UUID, Role: authctx.RoleUser}
	_, err := service.SaveBookCloudReadingState(ctx, otherUser, revision.ID, SaveBookCloudReadingStateInput{SessionID: a.Session.ID, RequestID: uuid.New(), Sequence: 2, BaseVersion: 1, CurrentPercent: .1})
	require.Equal(t, "books.reading_session_not_found", apperr.FromError(err).Code)
	state, err := service.GetBookCloudReadingState(ctx, otherUser, revision.ID)
	require.NoError(t, err)
	require.Zero(t, state.Version)
	require.Zero(t, state.CurrentPercent)
	encoded, err := json.Marshal(a)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), user.ID.String())
	// Takedown blocks new access, not an existing reader's private offline progress.
	require.NoError(t, service.db.Model(&asset).Update("status", "removed").Error)
	_, err = service.CreateBookReadingSession(ctx, user, CreateBookReadingSessionInput{ClientSessionID: uuid.New(), RevisionID: revision.ID, DeviceID: "new"})
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	_, err = service.OpenBookRevision(ctx, revision.ID)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	saved, err := service.SaveBookCloudReadingState(ctx, user, revision.ID, SaveBookCloudReadingStateInput{SessionID: a.Session.ID, RequestID: uuid.New(), Sequence: 2, BaseVersion: 1, TXTOffset: 10, CurrentPercent: .1})
	require.NoError(t, err)
	require.Equal(t, .1, saved.State.CurrentPercent)
	require.Equal(t, .3, saved.State.FurthestPercent)
	state, err = service.GetBookCloudReadingState(ctx, user, revision.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, state.Version)
}

func TestBookCloudReadingPositionFormats(t *testing.T) {
	for _, test := range []struct {
		format string
		input  SaveBookCloudReadingStateInput
		valid  bool
	}{
		{"epub", SaveBookCloudReadingStateInput{EPUBCFI: "epubcfi(/6/2!/4/1:5)"}, true},
		{"epub", SaveBookCloudReadingStateInput{PDFPage: 1}, false},
		{"epub", SaveBookCloudReadingStateInput{EPUBCFI: "epubcfi(not-a-position)"}, false},
		{"pdf", SaveBookCloudReadingStateInput{PDFPage: 1}, true},
		{"pdf", SaveBookCloudReadingStateInput{}, false},
		{"txt", SaveBookCloudReadingStateInput{TXTOffset: 0}, true},
		{"txt", SaveBookCloudReadingStateInput{PDFPage: 1}, false},
		{"txt", SaveBookCloudReadingStateInput{TXTOffset: -1}, false},
		{"txt", SaveBookCloudReadingStateInput{CurrentPercent: math.NaN()}, false},
		{"txt", SaveBookCloudReadingStateInput{CurrentPercent: 1.1}, false},
	} {
		err := validateBookCloudPosition(test.input, test.format)
		if test.valid {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
}

func TestBookCloudReadingSurvivesWorkMergeAndUndo(t *testing.T) {
	service, user, source, target := newBookMergeTestService(t)
	ctx := context.Background()
	sourceID, targetID := uuid.MustParse(source.ID), uuid.MustParse(target.ID)
	asset, revision := readyBookReadingFile(t, service, user, sourceID, "zh", "txt")
	_, err := service.SetBookReadingSelection(ctx, user, sourceID, SetBookReadingSelectionInput{AssetID: asset.ID, RevisionID: revision.ID})
	require.NoError(t, err)
	session := newBookCloudTestSession(t, service, user, revision.ID, "web")
	mark, err := service.SaveBookReadingMark(ctx, user, revision.ID, uuid.New(), txtBookHighlight(1, 8, "合并前的私人想法"))
	require.NoError(t, err)
	first := SaveBookCloudReadingStateInput{SessionID: session.Session.ID, RequestID: uuid.New(), Sequence: 1, TXTOffset: 40, CurrentPercent: .4}
	_, err = service.SaveBookCloudReadingState(ctx, user, revision.ID, first)
	require.NoError(t, err)
	merge := mergeBookTestWorks(t, service, user, source, target)
	selection, err := service.GetBookReadingSelection(ctx, user, sourceID)
	require.NoError(t, err)
	require.Equal(t, targetID, selection.WorkID)
	_, err = service.SaveBookReadingMark(ctx, user, revision.ID, mark.ID, SaveBookReadingMarkInput{BaseVersion: 1, Kind: "highlight", Anchor: mark.Anchor, Quote: mark.Quote, Note: "合并期间新整理的私人想法"})
	require.NoError(t, err)
	_, err = service.SaveBookCloudReadingState(ctx, user, revision.ID, SaveBookCloudReadingStateInput{SessionID: session.Session.ID, RequestID: uuid.New(), Sequence: 2, BaseVersion: 1, TXTOffset: 10, CurrentPercent: .1})
	require.NoError(t, err)
	_, err = service.RevertBookWorkMerge(ctx, user, merge.ID)
	require.NoError(t, err)
	selection, err = service.GetBookReadingSelection(ctx, user, sourceID)
	require.NoError(t, err)
	require.Equal(t, sourceID, selection.WorkID)
	marks, err := service.ListBookReadingMarks(ctx, user, revision.ID)
	require.NoError(t, err)
	require.Len(t, marks.Items, 1)
	require.Equal(t, "合并期间新整理的私人想法", marks.Items[0].Note)
	state, err := service.GetBookCloudReadingState(ctx, user, revision.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, state.Version)
	require.Equal(t, .1, state.CurrentPercent)
	require.Equal(t, .4, state.FurthestPercent)
	retry, err := service.SaveBookCloudReadingState(ctx, user, revision.ID, first)
	require.NoError(t, err)
	require.True(t, retry.Duplicate)
	require.EqualValues(t, 1, retry.State.Version)
}

func TestBookCloudReadingHTTPRoutesAndPrivateCache(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	workID := uuid.MustParse(work.ID)
	asset, revision := readyBookReadingFile(t, service, user, workID, "zh", "txt")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { authctx.SetCurrentUser(c, user); c.Next() })
	RegisterRoutes(router.Group("/api/v1/books"), service)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1/books"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
		return w
	}
	response := request(http.MethodPut, "/me/selections/"+work.ID, `{"asset_id":"`+asset.ID.String()+`","revision_id":"`+revision.ID.String()+`"}`)
	require.Equal(t, 200, response.Code, response.Body.String())
	response = request(http.MethodGet, "/me/selections/"+work.ID, "")
	require.Equal(t, 200, response.Code)
	require.Contains(t, response.Body.String(), revision.ID.String())
	response = request(http.MethodPost, "/me/reading-sessions", `{"client_session_id":"`+uuid.NewString()+`","revision_id":"`+revision.ID.String()+`","device_id":"web"}`)
	require.Equal(t, 200, response.Code, response.Body.String())
	var envelope struct {
		Data BookReadingSessionDTO `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
	response = request(http.MethodPut, "/me/reading-states/"+revision.ID.String(), `{"session_id":"`+envelope.Data.Session.ID.String()+`","request_id":"`+uuid.NewString()+`","sequence":1,"base_version":0,"txt_offset":60,"current_percent":0.6}`)
	require.Equal(t, 200, response.Code, response.Body.String())
	response = request(http.MethodGet, "/me/reading-states/"+revision.ID.String(), "")
	require.Equal(t, 200, response.Code)
	require.Contains(t, response.Body.String(), `"current_percent":0.6`)
	response = request(http.MethodGet, "/works/"+work.ID+"/reading-recommendation", "")
	require.Equal(t, 200, response.Code)
	require.Contains(t, response.Body.String(), `"source":"personal"`)
	public := gin.New()
	RegisterRoutes(public.Group("/api/v1/books"), service)
	w := httptest.NewRecorder()
	public.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/books/works/"+work.ID+"/reading-recommendation", nil))
	require.Equal(t, 200, w.Code)
	require.NotContains(t, w.Body.String(), `"source":"personal"`)
}

func TestBookReadingRecommendationsKeepExplicitRevision(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	workID := uuid.MustParse(work.ID)
	none, err := service.RecommendBookReading(ctx, user.ID, workID, "zh", nil)
	require.NoError(t, err)
	require.Equal(t, "no_readable_asset", none.Reason)
	en, _ := readyBookReadingFile(t, service, user, workID, "en", "epub")
	zh, old := readyBookReadingFile(t, service, user, workID, "zh-Hans", "txt")
	recommendation, err := service.RecommendBookReading(ctx, uuid.Nil, workID, "zh-CN", nil)
	require.NoError(t, err)
	require.Equal(t, zh.ID, recommendation.Asset.ID)
	selection, err := service.GetBookReadingSelection(ctx, user, workID)
	require.NoError(t, err)
	require.Nil(t, selection, "GET recommendation cannot create personal state")
	_, err = service.SetBookDefaultAsset(ctx, user, workID, "en", en.ID)
	require.NoError(t, err)
	recommendation, err = service.RecommendBookReading(ctx, uuid.Nil, workID, "en", nil)
	require.NoError(t, err)
	require.Equal(t, "community_default", recommendation.Source)
	_, err = service.SetBookReadingSelection(ctx, user, workID, SetBookReadingSelectionInput{AssetID: zh.ID, RevisionID: old.ID})
	require.NoError(t, err)
	media := model.MediaAsset{Key: "private/new", Purpose: "books.file", Size: 100, ContentType: "text/plain"}
	require.NoError(t, service.db.Create(&media).Error)
	newRevision := model.BookAssetRevision{AssetID: zh.ID, Number: 2, MediaAssetID: media.ID, SHA256: strings.Repeat("b", 64), OriginalFilename: "new.txt", Status: "ready", CreatedBy: user.ID}
	require.NoError(t, service.db.Create(&newRevision).Error)
	require.NoError(t, service.db.Model(&zh).Update("current_revision_id", newRevision.ID).Error)
	recommendation, err = service.RecommendBookReading(ctx, user.ID, workID, "en", nil)
	require.NoError(t, err)
	require.Equal(t, "personal", recommendation.Source)
	require.Equal(t, old.ID, *recommendation.RevisionID)
	recommendation, err = service.RecommendBookReading(ctx, user.ID, workID, "en", []string{"epub"})
	require.NoError(t, err)
	require.False(t, recommendation.Available)
	require.Equal(t, "selected_format_unsupported", recommendation.Reason)
	require.NoError(t, service.db.Model(&old).Update("status", "removed").Error)
	recommendation, err = service.RecommendBookReading(ctx, user.ID, workID, "en", nil)
	require.NoError(t, err)
	require.False(t, recommendation.Available)
	require.Equal(t, "selected_unavailable", recommendation.Reason)
	require.Equal(t, old.ID, *recommendation.RevisionID)
	_, err = service.SetBookReadingSelection(ctx, user, workID, SetBookReadingSelectionInput{AssetID: en.ID, RevisionID: newRevision.ID})
	require.Equal(t, "books.selection_revision_mismatch", apperr.FromError(err).Code)
}
