package music

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func newMusicHTTPTestService(t *testing.T) (*Service, *gorm.DB, authctx.CurrentUser) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Artist{},
		&model.Album{},
		&model.Song{},
		&model.ArtistBookmark{},
		&model.AlbumBookmark{},
		&model.SongBookmark{},
		&model.Playlist{},
		&model.PlaylistSong{},
		&model.AlbumImportSession{},
		&model.MusicEdit{},
		&model.MusicEditVote{},
		&model.MusicEditDecision{},
		&model.MusicEditChange{},
		&model.AuditLog{},
	)

	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	return NewService(db), db, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: authctx.RoleUser}
}

func newMusicHTTPRouter(service *Service, current *authctx.CurrentUser) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if current != nil {
			authctx.SetCurrentUser(c, *current)
		}
		c.Next()
	})
	v1 := r.Group("/api/v1")
	RegisterRoutes(v1.Group("/music"), service)
	return r
}

func TestRegisterRoutesSubmitEditReturnsCreatedAppliedEditForMainWikiFlow(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	r := newMusicHTTPRouter(service, &user)

	body := map[string]any{
		"type":        "create_artist",
		"entity_type": "artist",
		"payload": map[string]any{
			"name": "HTTP Artist",
		},
		"changes": map[string]any{},
		"reason":  "new artist",
	}
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/music/edits", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")

	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data model.MusicEdit `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.ID == uuid.Nil || resp.Data.Status != "applied" || !resp.Data.AutoApplied || resp.Data.SubmittedBy != user.ID {
		t.Fatalf("unexpected response edit: %#v", resp.Data)
	}

	var persisted model.MusicEdit
	if err := db.First(&persisted, "id = ?", resp.Data.ID).Error; err != nil {
		t.Fatalf("load persisted edit: %v", err)
	}
	if persisted.Status != "applied" || persisted.Type != "create_artist" {
		t.Fatalf("unexpected persisted edit: %#v", persisted)
	}
}

func TestRegisterRoutesListsArtistsThroughMusicV1(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	artist := model.Artist{Name: "Visible Artist", Bio: "bio", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/artists?q=visible", nil)

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []model.Artist `json:"data"`
		Meta struct {
			Page     int  `json:"page"`
			PageSize int  `json:"page_size"`
			Total    int  `json:"total"`
			HasMore  bool `json:"has_more"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Name != "Visible Artist" {
		t.Fatalf("unexpected artists response: %#v", resp.Data)
	}
	if resp.Meta.Page != 1 || resp.Meta.PageSize != 20 || resp.Meta.Total != 1 || resp.Meta.HasMore {
		t.Fatalf("unexpected pagination meta: %#v", resp.Meta)
	}
}

func TestRegisterRoutesListsMusicEditsForModerator(t *testing.T) {
	service, db, _ := newMusicHTTPTestService(t)
	moderator := authctx.CurrentUser{ID: uuid.New(), Username: "mod", Role: authctx.RoleModerator}
	if err := db.Create(&model.User{
		UUID:     moderator.ID,
		Username: moderator.Username,
		Email:    "mod@example.com",
		Password: "hash",
		Role:     moderator.Role,
		IsActive: true,
	}).Error; err != nil {
		t.Fatalf("create moderator: %v", err)
	}
	edit := model.MusicEdit{
		Type:        "create_artist",
		EntityType:  "artist",
		SubmittedBy: moderator.ID,
		Status:      "open",
		Reason:      "seed review queue",
		PayloadJSON: "{}",
		ChangesJSON: "{}",
		SourcesJSON: "[]",
		Votable:     true,
	}
	if err := db.Create(&edit).Error; err != nil {
		t.Fatalf("create music edit: %v", err)
	}
	r := newMusicHTTPRouter(service, &moderator)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/edits?status=open&page_size=10", nil)

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []model.MusicEdit `json:"data"`
		Meta struct {
			Page     int   `json:"page"`
			PageSize int   `json:"page_size"`
			Total    int64 `json:"total"`
			HasMore  bool  `json:"has_more"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != edit.ID {
		t.Fatalf("unexpected edits response: %#v", resp.Data)
	}
	if resp.Meta.Total != 1 || resp.Meta.Page != 1 || resp.Meta.PageSize != 10 || resp.Meta.HasMore {
		t.Fatalf("unexpected meta: %#v", resp.Meta)
	}
}

func TestRegisterRoutesListAlbumsSortsByHotScore(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	artist := model.Artist{Name: "Discovery Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	albums := []model.Album{
		{Title: "Low Heat", EntryStatus: "open", Status: "open", HotScore: 1.5},
		{Title: "High Heat", EntryStatus: "open", Status: "open", HotScore: 42.25},
		{Title: "Mid Heat", EntryStatus: "open", Status: "open", HotScore: 7},
	}
	for i := range albums {
		if err := db.Create(&albums[i]).Error; err != nil {
			t.Fatalf("create album %d: %v", i, err)
		}
		if err := db.Model(&albums[i]).Association("Artists").Append(&artist); err != nil {
			t.Fatalf("append artist to album %d: %v", i, err)
		}
	}
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/albums?sort=hot&page_size=10", nil)

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []model.Album `json:"data"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 3 || resp.Meta.Total != 3 {
		t.Fatalf("unexpected album count: data=%#v meta=%#v", resp.Data, resp.Meta)
	}
	gotTitles := []string{resp.Data[0].Title, resp.Data[1].Title, resp.Data[2].Title}
	wantTitles := []string{"High Heat", "Mid Heat", "Low Heat"}
	for i := range wantTitles {
		if gotTitles[i] != wantTitles[i] {
			t.Fatalf("expected hot order %v, got %v", wantTitles, gotTitles)
		}
	}
	if resp.Data[0].HotScore != 42.25 {
		t.Fatalf("expected hot_score in response, got %#v", resp.Data[0])
	}
}

func TestRegisterRoutesListAlbumsSearchesArtistNames(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	visibleArtist := model.Artist{Name: "Searchable Artist", EntryStatus: "open"}
	otherArtist := model.Artist{Name: "Other Artist", EntryStatus: "open"}
	if err := db.Create(&visibleArtist).Error; err != nil {
		t.Fatalf("create visible artist: %v", err)
	}
	if err := db.Create(&otherArtist).Error; err != nil {
		t.Fatalf("create other artist: %v", err)
	}
	visibleAlbum := model.Album{Title: "Title Does Not Match", EntryStatus: "open", Status: "open"}
	otherAlbum := model.Album{Title: "Other Album", EntryStatus: "open", Status: "open"}
	if err := db.Create(&visibleAlbum).Error; err != nil {
		t.Fatalf("create visible album: %v", err)
	}
	if err := db.Create(&otherAlbum).Error; err != nil {
		t.Fatalf("create other album: %v", err)
	}
	if err := db.Model(&visibleAlbum).Association("Artists").Append(&visibleArtist); err != nil {
		t.Fatalf("append visible artist: %v", err)
	}
	if err := db.Model(&otherAlbum).Association("Artists").Append(&otherArtist); err != nil {
		t.Fatalf("append other artist: %v", err)
	}
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/albums?q=searchable", nil)

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []model.Album `json:"data"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 1 || resp.Meta.Total != 1 || resp.Data[0].Title != "Title Does Not Match" {
		t.Fatalf("unexpected artist-name search response: data=%#v meta=%#v", resp.Data, resp.Meta)
	}
}

func TestRegisterRoutesCreateAlbumImportSessionSupportsArchiveUpload(t *testing.T) {
	service, _, user := newMusicHTTPTestService(t)
	r := newMusicHTTPRouter(service, &user)

	createBody, _ := json.Marshal(CreateAlbumImportSessionInput{
		Status: AlbumImportStatusPendingUpload,
	})
	createRecorder := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/music/imports/albums", bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(createRecorder, createReq)

	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("expected create session 201, got %d: %s", createRecorder.Code, createRecorder.Body.String())
	}

	var createResp struct {
		Data AlbumImportDTO `json:"data"`
	}
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	body, contentType := newAlbumImportUploadRequestBody(t, "Untrue.zip", map[string]string{
		"01 - Untitled.mp3":  "",
		"02 - Archangel.mp3": "",
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/music/imports/albums/"+createResp.Data.ImportID+"/upload", body)
	req.Header.Set("Content-Type", contentType)

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data AlbumImportDTO `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.Status != AlbumImportStatusReady {
		t.Fatalf("expected ready session, got %#v", resp.Data)
	}
	if resp.Data.ArchiveName != "Untrue.zip" {
		t.Fatalf("expected archive name persisted, got %#v", resp.Data.ArchiveName)
	}
}

func TestRegisterRoutesStartsAlbumImportMultipart(t *testing.T) {
	service, _, user := newMusicHTTPTestService(t)
	store := &fakeAlbumImportMultipartStore{uploadID: "upload-http-1"}
	service.albumImportMultipart = store
	r := newMusicHTTPRouter(service, &user)

	createBody, _ := json.Marshal(CreateAlbumImportSessionInput{
		Status: AlbumImportStatusPendingUpload,
	})
	createRecorder := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/music/imports/albums", bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(createRecorder, createReq)

	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("expected create session 201, got %d: %s", createRecorder.Code, createRecorder.Body.String())
	}

	var createResp struct {
		Data AlbumImportDTO `json:"data"`
	}
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	startBody, _ := json.Marshal(StartAlbumImportMultipartInput{
		FileName:    "Untrue.zip",
		FileSize:    64 * 1024 * 1024,
		ContentType: "application/zip",
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/music/imports/albums/"+createResp.Data.ImportID+"/multipart", bytes.NewReader(startBody))
	req.Header.Set("Content-Type", "application/json")

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data AlbumImportMultipartDTO `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.ImportID != createResp.Data.ImportID {
		t.Fatalf("expected import id %s, got %#v", createResp.Data.ImportID, resp.Data)
	}
	if resp.Data.FileName != "Untrue.zip" || resp.Data.FileSize != 64*1024*1024 {
		t.Fatalf("unexpected multipart response: %#v", resp.Data)
	}
	if resp.Data.ObjectKey == "" || resp.Data.PartSize <= 0 || len(resp.Data.CompletedParts) != 0 {
		t.Fatalf("unexpected multipart state: %#v", resp.Data)
	}
	if store.createCalls != 1 || store.createContentType != "application/zip" {
		t.Fatalf("expected multipart store create call, got %#v", store)
	}
}

func TestRegisterRoutesCommitAlbumImportSessionUsesRequestPayload(t *testing.T) {
	service, _, user := newMusicHTTPTestService(t)
	r := newMusicHTTPRouter(service, &user)

	createBody, _ := json.Marshal(CreateAlbumImportSessionInput{
		Status: AlbumImportStatusReady,
	})
	createRecorder := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/music/imports/albums", bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(createRecorder, createReq)

	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("expected create session 201, got %d: %s", createRecorder.Code, createRecorder.Body.String())
	}

	var createResp struct {
		Data AlbumImportDTO `json:"data"`
	}
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	commitBody, _ := json.Marshal(CommitAlbumImportSessionInput{
		Artist: AlbumImportArtistPayload{
			Name:      "HTTP Artist",
			LegalName: "HTTP Legal Artist",
			StageNames: []ArtistStageNamePayload{
				{Name: "HTTP Artist", IsPrimary: true},
			},
			BirthPlace: "Shanghai",
		},
		Album: AlbumImportAlbumPayload{
			Title:       "HTTP Album",
			ReleaseYear: 2026,
			Tracks: []AlbumImportTrackPayload{
				{Title: "Track One", TrackNumber: 1},
			},
		},
	})

	commitRecorder := httptest.NewRecorder()
	commitReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/music/imports/albums/"+createResp.Data.ImportID+"/commit",
		bytes.NewReader(commitBody),
	)
	commitReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(commitRecorder, commitReq)

	if commitRecorder.Code != http.StatusOK {
		t.Fatalf("expected commit 200, got %d: %s", commitRecorder.Code, commitRecorder.Body.String())
	}
}

func newAlbumImportUploadRequestBody(t *testing.T, archiveName string, files map[string]string) (*bytes.Buffer, string) {
	t.Helper()

	archive := newImportTestZipArchive(t, files)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("archive", archiveName)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(archive); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &body, writer.FormDataContentType()
}

func TestRegisterRoutesListAlbumsSearchesArtistNamesWithHotSort(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	artist := model.Artist{Name: "Ranked Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	lowAlbum := model.Album{Title: "Low Ranked", EntryStatus: "open", Status: "open", HotScore: 1}
	highAlbum := model.Album{Title: "High Ranked", EntryStatus: "open", Status: "open", HotScore: 9}
	if err := db.Create(&lowAlbum).Error; err != nil {
		t.Fatalf("create low album: %v", err)
	}
	if err := db.Create(&highAlbum).Error; err != nil {
		t.Fatalf("create high album: %v", err)
	}
	if err := db.Model(&lowAlbum).Association("Artists").Append(&artist); err != nil {
		t.Fatalf("append low artist: %v", err)
	}
	if err := db.Model(&highAlbum).Association("Artists").Append(&artist); err != nil {
		t.Fatalf("append high artist: %v", err)
	}
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/albums?q=ranked&sort=hot", nil)

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []model.Album `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 2 || resp.Data[0].Title != "High Ranked" || resp.Data[1].Title != "Low Ranked" {
		t.Fatalf("expected searched albums in hot order, got %#v", resp.Data)
	}
}

func TestRegisterRoutesAlbumResponsesResolveMediaURLs(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	t.Setenv("PUBLIC_UPLOADS_BASE_URL", "https://cdn.atoman.test")
	t.Setenv("STORAGE_TYPE", "")

	artist := model.Artist{Name: "Resolved Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}

	album := model.Album{
		Title:       "Resolved Album",
		EntryStatus: "open",
		Status:      "open",
		CoverURL:    "uploads/music/covers/albums/resolved/cover.jpg",
	}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	if err := db.Model(&album).Association("Artists").Append(&artist); err != nil {
		t.Fatalf("append artist: %v", err)
	}

	song := model.Song{
		Title:       "Resolved Song",
		Status:      "open",
		AlbumID:     &album.ID,
		AudioURL:    "uploads/music/audio/albums/resolved/song.mp3",
		CoverURL:    "uploads/music/covers/albums/resolved/song-cover.jpg",
		TrackNumber: 1,
	}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}

	r := newMusicHTTPRouter(service, &user)

	listRecorder := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/music/albums", nil)
	r.ServeHTTP(listRecorder, listReq)

	if listRecorder.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d: %s", listRecorder.Code, listRecorder.Body.String())
	}

	var listResp struct {
		Data []struct {
			ID       string `json:"id"`
			CoverURL string `json:"cover_url"`
			Songs    []struct {
				AudioURL string `json:"audio_url"`
				CoverURL string `json:"cover_url"`
			} `json:"songs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listResp.Data) != 1 {
		t.Fatalf("expected 1 album in list response, got %#v", listResp.Data)
	}
	if listResp.Data[0].CoverURL != "https://cdn.atoman.test/uploads/music/covers/albums/resolved/cover.jpg" {
		t.Fatalf("expected resolved list cover_url, got %q", listResp.Data[0].CoverURL)
	}
	if len(listResp.Data[0].Songs) != 1 {
		t.Fatalf("expected 1 song in list response, got %#v", listResp.Data[0].Songs)
	}
	if listResp.Data[0].Songs[0].AudioURL != "https://cdn.atoman.test/uploads/music/audio/albums/resolved/song.mp3" {
		t.Fatalf("expected resolved list song audio_url, got %q", listResp.Data[0].Songs[0].AudioURL)
	}
	if listResp.Data[0].Songs[0].CoverURL != "https://cdn.atoman.test/uploads/music/covers/albums/resolved/song-cover.jpg" {
		t.Fatalf("expected resolved list song cover_url, got %q", listResp.Data[0].Songs[0].CoverURL)
	}

	detailRecorder := httptest.NewRecorder()
	detailReq := httptest.NewRequest(http.MethodGet, "/api/v1/music/albums/"+album.ID.String(), nil)
	r.ServeHTTP(detailRecorder, detailReq)

	if detailRecorder.Code != http.StatusOK {
		t.Fatalf("expected detail 200, got %d: %s", detailRecorder.Code, detailRecorder.Body.String())
	}

	var detailResp struct {
		Data struct {
			CoverURL string `json:"cover_url"`
			Songs    []struct {
				AudioURL string `json:"audio_url"`
				CoverURL string `json:"cover_url"`
			} `json:"songs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(detailRecorder.Body.Bytes(), &detailResp); err != nil {
		t.Fatalf("decode detail response: %v", err)
	}
	if detailResp.Data.CoverURL != "https://cdn.atoman.test/uploads/music/covers/albums/resolved/cover.jpg" {
		t.Fatalf("expected resolved detail cover_url, got %q", detailResp.Data.CoverURL)
	}
	if len(detailResp.Data.Songs) != 1 {
		t.Fatalf("expected 1 song in detail response, got %#v", detailResp.Data.Songs)
	}
	if detailResp.Data.Songs[0].AudioURL != "https://cdn.atoman.test/uploads/music/audio/albums/resolved/song.mp3" {
		t.Fatalf("expected resolved detail song audio_url, got %q", detailResp.Data.Songs[0].AudioURL)
	}
	if detailResp.Data.Songs[0].CoverURL != "https://cdn.atoman.test/uploads/music/covers/albums/resolved/song-cover.jpg" {
		t.Fatalf("expected resolved detail song cover_url, got %q", detailResp.Data.Songs[0].CoverURL)
	}
}

func TestResolveMusicMediaURLAvoidsDuplicatingUploadsPrefix(t *testing.T) {
	t.Setenv("PUBLIC_UPLOADS_BASE_URL", "http://localhost:8080/uploads")
	t.Setenv("STORAGE_TYPE", "")

	gotWithLeadingSlash := resolveMusicMediaURL("/uploads/music/placeholder.jpg")
	if gotWithLeadingSlash != "http://localhost:8080/uploads/music/placeholder.jpg" {
		t.Fatalf("expected no duplicated uploads prefix, got %q", gotWithLeadingSlash)
	}

	gotWithoutLeadingSlash := resolveMusicMediaURL("uploads/music/placeholder.jpg")
	if gotWithoutLeadingSlash != "http://localhost:8080/uploads/music/placeholder.jpg" {
		t.Fatalf("expected no duplicated uploads prefix, got %q", gotWithoutLeadingSlash)
	}
}

func TestMusicRecommendationModeValidation(t *testing.T) {
	service, _, user := newMusicHTTPTestService(t)
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/recommend/albums?mode=bad", nil)

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestMusicRecommendationAlbumsReturnsData(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	album := model.Album{
		Title:       "Recommend Me",
		EntryStatus: "open",
		Status:      "open",
		HotScore:    8.5,
	}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/recommend/albums?mode=hot", nil)

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []struct {
			ID         string `json:"id"`
			Title      string `json:"title"`
			Summary    string `json:"summary"`
			ImageURL   string `json:"image_url"`
			TargetPath string `json:"target_path"`
			ScoreLabel string `json:"score_label"`
		} `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) == 0 {
		t.Fatalf("expected recommendation data, got %s", w.Body.String())
	}
	first := resp.Data[0]
	if first.ID == "" || first.Title == "" || first.TargetPath == "" || first.ScoreLabel == "" {
		t.Fatalf("expected lightweight recommendation dto fields, got %#v", first)
	}
	if first.TargetPath != "/music/album/"+album.ID.String() {
		t.Fatalf("expected target path %s, got %s", "/music/album/"+album.ID.String(), first.TargetPath)
	}
	if resp.Meta.Total == 0 {
		t.Fatalf("expected total > 0, got %#v", resp.Meta)
	}
}

func TestAlbumSortOrdersSupportsRandomMode(t *testing.T) {
	got := albumSortOrders("random")

	if len(got) != 1 || got[0] != "RANDOM()" {
		t.Fatalf("expected RANDOM() order, got %#v", got)
	}
}

func TestRegisterRoutesSubmitEditRequiresCurrentUser(t *testing.T) {
	service, _, _ := newMusicHTTPTestService(t)
	r := newMusicHTTPRouter(service, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/music/edits", bytes.NewBufferString(`{"type":"create_artist","entity_type":"artist","reason":"new"}`))
	req.Header.Set("Content-Type", "application/json")

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterRoutesSubmitEditRejectsInvalidJSON(t *testing.T) {
	service, _, user := newMusicHTTPTestService(t)
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/music/edits", bytes.NewBufferString(`{"type":`))
	req.Header.Set("Content-Type", "application/json")

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "validation.invalid_request") {
		t.Fatalf("expected validation.invalid_request, got %s", w.Body.String())
	}
}

func TestRegisterRoutesGetEditRejectsInvalidUUID(t *testing.T) {
	service, _, user := newMusicHTTPTestService(t)
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/edits/not-a-uuid", nil)

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "validation.invalid_request") {
		t.Fatalf("expected validation.invalid_request, got %s", w.Body.String())
	}
}

func TestRegisterRoutesGetEditRequiresSubmitterOrModerator(t *testing.T) {
	service, db, submitter := newMusicHTTPTestService(t)
	edit, err := service.SubmitEdit(submitter, SubmitEditRequest{Type: "create_artist", EntityType: "artist", Payload: map[string]any{"name": "HTTP Artist"}, Reason: "new artist"})
	if err != nil {
		t.Fatalf("submit edit: %v", err)
	}

	otherUserModel := model.User{Username: "bob", Email: "bob@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&otherUserModel).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}
	otherUser := authctx.CurrentUser{ID: otherUserModel.UUID, Username: otherUserModel.Username, Role: authctx.RoleUser}
	moderatorModel := model.User{Username: "mod", Email: "mod-http@example.com", Password: "hash", Role: authctx.RoleModerator, IsActive: true}
	if err := db.Create(&moderatorModel).Error; err != nil {
		t.Fatalf("create moderator: %v", err)
	}
	moderator := authctx.CurrentUser{ID: moderatorModel.UUID, Username: moderatorModel.Username, Role: authctx.RoleModerator}

	rSubmitter := newMusicHTTPRouter(service, &submitter)
	wSubmitter := httptest.NewRecorder()
	rSubmitter.ServeHTTP(wSubmitter, httptest.NewRequest(http.MethodGet, "/api/v1/music/edits/"+edit.ID.String(), nil))
	if wSubmitter.Code != http.StatusOK {
		t.Fatalf("expected submitter 200, got %d: %s", wSubmitter.Code, wSubmitter.Body.String())
	}

	rOther := newMusicHTTPRouter(service, &otherUser)
	wOther := httptest.NewRecorder()
	rOther.ServeHTTP(wOther, httptest.NewRequest(http.MethodGet, "/api/v1/music/edits/"+edit.ID.String(), nil))
	if wOther.Code != http.StatusForbidden {
		t.Fatalf("expected other user 403, got %d: %s", wOther.Code, wOther.Body.String())
	}

	rModerator := newMusicHTTPRouter(service, &moderator)
	wModerator := httptest.NewRecorder()
	rModerator.ServeHTTP(wModerator, httptest.NewRequest(http.MethodGet, "/api/v1/music/edits/"+edit.ID.String(), nil))
	if wModerator.Code != http.StatusOK {
		t.Fatalf("expected moderator 200, got %d: %s", wModerator.Code, wModerator.Body.String())
	}
}

func TestRegisterRoutesArtistBookmarksAreIdempotent(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	artist := model.Artist{Name: "Bookmarked Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	r := newMusicHTTPRouter(service, &user)

	postBody := `{"artist_id":"` + artist.ID.String() + `"}`
	firstPost := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, "/api/v1/music/bookmarks/artists", bytes.NewBufferString(postBody))
	firstReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(firstPost, firstReq)

	if firstPost.Code != http.StatusCreated {
		t.Fatalf("expected first post 201, got %d: %s", firstPost.Code, firstPost.Body.String())
	}

	secondPost := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, "/api/v1/music/bookmarks/artists", bytes.NewBufferString(postBody))
	secondReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(secondPost, secondReq)

	if secondPost.Code != http.StatusCreated {
		t.Fatalf("expected second post 201, got %d: %s", secondPost.Code, secondPost.Body.String())
	}

	listRecorder := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/music/bookmarks/artists", nil)
	r.ServeHTTP(listRecorder, listReq)

	if listRecorder.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d: %s", listRecorder.Code, listRecorder.Body.String())
	}

	var listResp struct {
		Data []struct {
			ID       string `json:"id"`
			ArtistID string `json:"artist_id"`
			UserID   string `json:"user_id"`
		} `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listResp.Data) != 1 || listResp.Meta.Total != 1 {
		t.Fatalf("expected one artist bookmark, got %#v %#v", listResp.Data, listResp.Meta)
	}
	if listResp.Data[0].ArtistID != artist.ID.String() || listResp.Data[0].UserID != user.ID.String() {
		t.Fatalf("unexpected artist bookmark payload: %#v", listResp.Data[0])
	}
}

func TestRegisterRoutesAlbumBookmarksDeleteIsIdempotent(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	album := model.Album{Title: "Bookmarked Album", EntryStatus: "open", Status: "open"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	r := newMusicHTTPRouter(service, &user)

	postBody := `{"album_id":"` + album.ID.String() + `"}`
	postRecorder := httptest.NewRecorder()
	postReq := httptest.NewRequest(http.MethodPost, "/api/v1/music/bookmarks/albums", bytes.NewBufferString(postBody))
	postReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(postRecorder, postReq)

	if postRecorder.Code != http.StatusCreated {
		t.Fatalf("expected post 201, got %d: %s", postRecorder.Code, postRecorder.Body.String())
	}

	firstDelete := httptest.NewRecorder()
	firstDeleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/music/bookmarks/albums/"+album.ID.String(), nil)
	r.ServeHTTP(firstDelete, firstDeleteReq)

	if firstDelete.Code != http.StatusOK {
		t.Fatalf("expected first delete 200, got %d: %s", firstDelete.Code, firstDelete.Body.String())
	}

	secondDelete := httptest.NewRecorder()
	secondDeleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/music/bookmarks/albums/"+album.ID.String(), nil)
	r.ServeHTTP(secondDelete, secondDeleteReq)

	if secondDelete.Code != http.StatusOK {
		t.Fatalf("expected second delete 200, got %d: %s", secondDelete.Code, secondDelete.Body.String())
	}

	listRecorder := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/music/bookmarks/albums", nil)
	r.ServeHTTP(listRecorder, listReq)

	if listRecorder.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d: %s", listRecorder.Code, listRecorder.Body.String())
	}

	var listResp struct {
		Data []any `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listResp.Data) != 0 || listResp.Meta.Total != 0 {
		t.Fatalf("expected empty album bookmarks after delete, got %#v %#v", listResp.Data, listResp.Meta)
	}
}

func TestRegisterRoutesSongBookmarksRequireCurrentUser(t *testing.T) {
	service, db, _ := newMusicHTTPTestService(t)
	song := model.Song{Title: "Bookmarked Song", AudioURL: "/audio/song.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	r := newMusicHTTPRouter(service, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/music/bookmarks/songs", bytes.NewBufferString(`{"song_id":"`+song.ID.String()+`"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterRoutesSongBookmarksListIncludesSongDetails(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	artist := model.Artist{Name: "Song Bookmark Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	album := model.Album{Title: "Song Bookmark Album", EntryStatus: "open", Status: "open"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	if err := db.Model(&album).Association("Artists").Append(&artist); err != nil {
		t.Fatalf("append album artist: %v", err)
	}
	song := model.Song{Title: "cellophane", AudioURL: "/audio/song.mp3", Status: "open", AlbumID: &album.ID}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	if err := db.Model(&song).Association("Artists").Append(&artist); err != nil {
		t.Fatalf("append song artist: %v", err)
	}
	if _, err := service.BookmarkSong(user, song.ID); err != nil {
		t.Fatalf("bookmark song: %v", err)
	}

	r := newMusicHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/bookmarks/songs", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []struct {
			ID   string `json:"id"`
			Song struct {
				ID    string `json:"id"`
				Title string `json:"title"`
				Album struct {
					Title string `json:"title"`
				} `json:"album"`
				Artists []struct {
					Name string `json:"name"`
				} `json:"artists"`
			} `json:"song"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 song bookmark, got %#v", resp.Data)
	}
	if resp.Data[0].Song.Title != "cellophane" {
		t.Fatalf("expected song title in bookmark payload, got %#v", resp.Data[0].Song)
	}
	if resp.Data[0].Song.Album.Title != "Song Bookmark Album" {
		t.Fatalf("expected album title in bookmark payload, got %#v", resp.Data[0].Song.Album)
	}
	if len(resp.Data[0].Song.Artists) != 1 || resp.Data[0].Song.Artists[0].Name != "Song Bookmark Artist" {
		t.Fatalf("expected song artists in bookmark payload, got %#v", resp.Data[0].Song.Artists)
	}
}

func TestRegisterRoutesPlaylistsArePrivateAndSongsAreDeduplicated(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	otherModel := model.User{Username: "playlist-other", Email: "playlist-other@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&otherModel).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}
	otherUser := authctx.CurrentUser{ID: otherModel.UUID, Username: otherModel.Username, Role: authctx.RoleUser}
	song := model.Song{Title: "Playlist Song", AudioURL: "/audio/playlist-song.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}

	userRouter := newMusicHTTPRouter(service, &user)
	createRecorder := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/music/playlists", bytes.NewBufferString(`{"name":"My Playlist"}`))
	createReq.Header.Set("Content-Type", "application/json")
	userRouter.ServeHTTP(createRecorder, createReq)

	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("expected create playlist 201, got %d: %s", createRecorder.Code, createRecorder.Body.String())
	}

	var createResp struct {
		Data struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			UserID string `json:"user_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create playlist response: %v", err)
	}
	if createResp.Data.Name != "My Playlist" || createResp.Data.UserID != user.ID.String() || createResp.Data.ID == "" {
		t.Fatalf("unexpected playlist payload: %#v", createResp.Data)
	}

	addSongBody := `{"song_id":"` + song.ID.String() + `"}`
	firstAdd := httptest.NewRecorder()
	firstAddReq := httptest.NewRequest(http.MethodPost, "/api/v1/music/playlists/"+createResp.Data.ID+"/songs", bytes.NewBufferString(addSongBody))
	firstAddReq.Header.Set("Content-Type", "application/json")
	userRouter.ServeHTTP(firstAdd, firstAddReq)
	if firstAdd.Code != http.StatusCreated {
		t.Fatalf("expected first add song 201, got %d: %s", firstAdd.Code, firstAdd.Body.String())
	}

	secondAdd := httptest.NewRecorder()
	secondAddReq := httptest.NewRequest(http.MethodPost, "/api/v1/music/playlists/"+createResp.Data.ID+"/songs", bytes.NewBufferString(addSongBody))
	secondAddReq.Header.Set("Content-Type", "application/json")
	userRouter.ServeHTTP(secondAdd, secondAddReq)
	if secondAdd.Code != http.StatusCreated {
		t.Fatalf("expected second add song 201, got %d: %s", secondAdd.Code, secondAdd.Body.String())
	}

	songsRecorder := httptest.NewRecorder()
	songsReq := httptest.NewRequest(http.MethodGet, "/api/v1/music/playlists/"+createResp.Data.ID+"/songs", nil)
	userRouter.ServeHTTP(songsRecorder, songsReq)
	if songsRecorder.Code != http.StatusOK {
		t.Fatalf("expected list songs 200, got %d: %s", songsRecorder.Code, songsRecorder.Body.String())
	}

	var songsResp struct {
		Data []struct {
			ID         string `json:"id"`
			PlaylistID string `json:"playlist_id"`
			SongID     string `json:"song_id"`
		} `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(songsRecorder.Body.Bytes(), &songsResp); err != nil {
		t.Fatalf("decode playlist songs response: %v", err)
	}
	if len(songsResp.Data) != 1 || songsResp.Meta.Total != 1 {
		t.Fatalf("expected one playlist song, got %#v %#v", songsResp.Data, songsResp.Meta)
	}
	if songsResp.Data[0].SongID != song.ID.String() || songsResp.Data[0].PlaylistID != createResp.Data.ID {
		t.Fatalf("unexpected playlist song payload: %#v", songsResp.Data[0])
	}

	otherRouter := newMusicHTTPRouter(service, &otherUser)
	otherList := httptest.NewRecorder()
	otherListReq := httptest.NewRequest(http.MethodGet, "/api/v1/music/playlists", nil)
	otherRouter.ServeHTTP(otherList, otherListReq)
	if otherList.Code != http.StatusOK {
		t.Fatalf("expected other user playlist list 200, got %d: %s", otherList.Code, otherList.Body.String())
	}

	var otherListResp struct {
		Data []any `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(otherList.Body.Bytes(), &otherListResp); err != nil {
		t.Fatalf("decode other user playlist list: %v", err)
	}
	if len(otherListResp.Data) != 0 || otherListResp.Meta.Total != 0 {
		t.Fatalf("expected private playlists to be hidden, got %#v %#v", otherListResp.Data, otherListResp.Meta)
	}
}

func TestRegisterRoutesDeletePlaylistSongIsIdempotent(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	song := model.Song{Title: "Delete Playlist Song", AudioURL: "/audio/delete-playlist-song.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	r := newMusicHTTPRouter(service, &user)

	createRecorder := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/music/playlists", bytes.NewBufferString(`{"name":"Delete Songs"}`))
	createReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(createRecorder, createReq)
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("expected create playlist 201, got %d: %s", createRecorder.Code, createRecorder.Body.String())
	}

	var createResp struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create playlist response: %v", err)
	}

	addRecorder := httptest.NewRecorder()
	addReq := httptest.NewRequest(http.MethodPost, "/api/v1/music/playlists/"+createResp.Data.ID+"/songs", bytes.NewBufferString(`{"song_id":"`+song.ID.String()+`"}`))
	addReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(addRecorder, addReq)
	if addRecorder.Code != http.StatusCreated {
		t.Fatalf("expected add song 201, got %d: %s", addRecorder.Code, addRecorder.Body.String())
	}

	firstDelete := httptest.NewRecorder()
	firstDeleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/music/playlists/"+createResp.Data.ID+"/songs/"+song.ID.String(), nil)
	r.ServeHTTP(firstDelete, firstDeleteReq)
	if firstDelete.Code != http.StatusOK {
		t.Fatalf("expected first delete 200, got %d: %s", firstDelete.Code, firstDelete.Body.String())
	}

	secondDelete := httptest.NewRecorder()
	secondDeleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/music/playlists/"+createResp.Data.ID+"/songs/"+song.ID.String(), nil)
	r.ServeHTTP(secondDelete, secondDeleteReq)
	if secondDelete.Code != http.StatusOK {
		t.Fatalf("expected second delete 200, got %d: %s", secondDelete.Code, secondDelete.Body.String())
	}
}
