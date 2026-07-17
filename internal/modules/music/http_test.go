package music

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		&model.ArtistMember{},
		&model.ArtistAlias{},
		&model.ArtistMerge{},
		&model.Album{},
		&model.Song{},
		&model.ArtistBookmark{},
		&model.AlbumBookmark{},
		&model.SongBookmark{},
		&model.Playlist{},
		&model.PlaylistSong{},
		&model.MusicListeningHistory{},
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

func TestRegisterRoutesCreatesArtistThroughMusicV1(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/music/artists", bytes.NewBufferString(`{"name":"New Music Artist","bio":"artist bio","image_url":"/uploads/artist.jpg","nationality":"JP","birth_year":1990}`))
	req.Header.Set("Content-Type", "application/json")

	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data model.Artist `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.ID == uuid.Nil || resp.Data.Name != "New Music Artist" || resp.Data.Bio != "artist bio" || resp.Data.Nationality != "JP" || resp.Data.BirthYear != 1990 || resp.Data.EntryStatus != "open" {
		t.Fatalf("unexpected artist response: %#v", resp.Data)
	}

	var persisted model.Artist
	if err := db.First(&persisted, "id = ?", resp.Data.ID).Error; err != nil {
		t.Fatalf("load persisted artist: %v", err)
	}
	if persisted.Name != "New Music Artist" {
		t.Fatalf("unexpected persisted artist: %#v", persisted)
	}
}

func TestRegisterRoutesUpdatesArtistThroughMusicV1(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	artist := model.Artist{Name: "Before Artist", Bio: "before", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/music/artists/"+artist.ID.String(), bytes.NewBufferString(`{"name":"After Artist","bio":"after","image_url":"/uploads/after.jpg","nationality":"KR","birth_year":1991,"death_year":2026}`))
	req.Header.Set("Content-Type", "application/json")

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data model.Artist `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.Name != "After Artist" || resp.Data.Bio != "after" || resp.Data.ImageURL == "" || resp.Data.Nationality != "KR" || resp.Data.BirthYear != 1991 || resp.Data.DeathYear != 2026 {
		t.Fatalf("unexpected artist response: %#v", resp.Data)
	}

	var persisted model.Artist
	if err := db.First(&persisted, "id = ?", artist.ID).Error; err != nil {
		t.Fatalf("load persisted artist: %v", err)
	}
	if persisted.Name != "After Artist" || persisted.Bio != "after" {
		t.Fatalf("unexpected persisted artist: %#v", persisted)
	}
}

func TestRegisterRoutesArtistSearchMatchesAliasAndReturnsPrimaryArtist(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	artist := model.Artist{
		Name:        "Ye",
		LegalName:   "Kanye Omari West",
		EntryStatus: "open",
	}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	if err := db.Create(&model.ArtistAlias{
		ArtistID: artist.ID,
		Alias:    "kanye",
	}).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/artists?q=kanye", nil)

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []model.Artist `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Name != "Ye" {
		t.Fatalf("expected alias search to return primary artist Ye, got %#v", resp.Data)
	}
	if len(resp.Data[0].Aliases) != 1 || resp.Data[0].Aliases[0].Alias != "kanye" {
		t.Fatalf("expected aliases preloaded in artist response, got %#v", resp.Data[0].Aliases)
	}
}

func TestRegisterRoutesGetArtistReturnsGroupedMembersForGroupArtist(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	memberCurrent := model.Artist{Name: "Current Member", EntryStatus: "open"}
	memberFormer := model.Artist{Name: "Former Member", EntryStatus: "open"}
	memberFuture := model.Artist{Name: "Future Member", EntryStatus: "open"}
	memberLeavingSoon := model.Artist{Name: "Leaving Soon Member", EntryStatus: "open"}
	group := model.Artist{Name: "Unit Group", EntryStatus: "open", ArtistForm: "group"}
	if err := db.Create(&memberCurrent).Error; err != nil {
		t.Fatalf("create current member: %v", err)
	}
	if err := db.Create(&memberFormer).Error; err != nil {
		t.Fatalf("create former member: %v", err)
	}
	if err := db.Create(&memberFuture).Error; err != nil {
		t.Fatalf("create future member: %v", err)
	}
	if err := db.Create(&memberLeavingSoon).Error; err != nil {
		t.Fatalf("create leaving soon member: %v", err)
	}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("create group artist: %v", err)
	}
	if err := db.Create(&model.ArtistMember{
		GroupArtistID:  group.ID,
		MemberArtistID: memberCurrent.ID,
		JoinDate:       mustDatePtr(t, "2021-01-01"),
	}).Error; err != nil {
		t.Fatalf("create current membership: %v", err)
	}
	if err := db.Create(&model.ArtistMember{
		GroupArtistID:  group.ID,
		MemberArtistID: memberFormer.ID,
		JoinDate:       mustDatePtr(t, "2019-01-01"),
		LeaveDate:      mustDatePtr(t, "2020-12-31"),
	}).Error; err != nil {
		t.Fatalf("create former membership: %v", err)
	}
	futureJoin := time.Now().AddDate(0, 0, 7).Format("2006-01-02")
	futureLeave := time.Now().AddDate(0, 0, 7).Format("2006-01-02")
	if err := db.Create(&model.ArtistMember{
		GroupArtistID:  group.ID,
		MemberArtistID: memberFuture.ID,
		JoinDate:       mustDatePtr(t, futureJoin),
	}).Error; err != nil {
		t.Fatalf("create future membership: %v", err)
	}
	if err := db.Create(&model.ArtistMember{
		GroupArtistID:  group.ID,
		MemberArtistID: memberLeavingSoon.ID,
		JoinDate:       mustDatePtr(t, "2022-01-01"),
		LeaveDate:      mustDatePtr(t, futureLeave),
	}).Error; err != nil {
		t.Fatalf("create leaving soon membership: %v", err)
	}
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/artists/"+group.ID.String(), nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			ID           string `json:"id"`
			ArtistForm   string `json:"artist_form"`
			MemberGroups struct {
				Current []struct {
					ArtistID string `json:"artist_id"`
				} `json:"current"`
				Former []struct {
					ArtistID string `json:"artist_id"`
				} `json:"former"`
			} `json:"member_groups"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.ArtistForm != "group" {
		t.Fatalf("expected group artist form, got %#v", resp.Data)
	}
	if len(resp.Data.MemberGroups.Current) != 2 {
		t.Fatalf("unexpected current member groups: %#v", resp.Data.MemberGroups.Current)
	}
	if len(resp.Data.MemberGroups.Former) != 1 || resp.Data.MemberGroups.Former[0].ArtistID != memberFormer.ID.String() {
		t.Fatalf("unexpected former member groups: %#v", resp.Data.MemberGroups.Former)
	}
	foundLeavingSoon := false
	for _, item := range resp.Data.MemberGroups.Current {
		if item.ArtistID == memberFuture.ID.String() {
			t.Fatalf("future member should not be current: %#v", resp.Data.MemberGroups.Current)
		}
		if item.ArtistID == memberLeavingSoon.ID.String() {
			foundLeavingSoon = true
		}
	}
	if !foundLeavingSoon {
		t.Fatalf("member with future leave date should still be current: %#v", resp.Data.MemberGroups.Current)
	}
}

func TestRegisterRoutesGetArtistStillReturnsArtistWhenArtistMembersTableMissing(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	artist := model.Artist{Name: "Legacy Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	if err := db.Migrator().DropTable(&model.ArtistMember{}); err != nil {
		t.Fatalf("drop artist_members table: %v", err)
	}
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/artists/"+artist.ID.String(), nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			ID           string `json:"id"`
			MemberGroups struct {
				Current []struct{} `json:"current"`
				Former  []struct{} `json:"former"`
			} `json:"member_groups"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.ID != artist.ID.String() {
		t.Fatalf("expected artist %s, got %#v", artist.ID.String(), resp.Data)
	}
	if len(resp.Data.MemberGroups.Current) != 0 || len(resp.Data.MemberGroups.Former) != 0 {
		t.Fatalf("expected empty member groups when table is missing, got %#v", resp.Data.MemberGroups)
	}
}

func mustDatePtr(t *testing.T, value string) *time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		t.Fatalf("parse date %q: %v", value, err)
	}
	return &parsed
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

func TestRegisterRoutesMusicStatsUseRealCounts(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	artist := model.Artist{Name: "Stats Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	album := model.Album{Title: "Stats Album", EntryStatus: "open", Status: "open"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	if err := db.Model(&album).Association("Artists").Append(&artist); err != nil {
		t.Fatalf("append album artist: %v", err)
	}

	song1 := model.Song{Title: "Song A", AudioURL: "/audio/a.mp3", Status: "open", AlbumID: &album.ID, PlayCount: 7}
	song2 := model.Song{Title: "Song B", AudioURL: "/audio/b.mp3", Status: "open", AlbumID: &album.ID, PlayCount: 5}
	if err := db.Create(&song1).Error; err != nil {
		t.Fatalf("create song1: %v", err)
	}
	if err := db.Create(&song2).Error; err != nil {
		t.Fatalf("create song2: %v", err)
	}
	if err := db.Model(&song1).Association("Artists").Append(&artist); err != nil {
		t.Fatalf("append song1 artist: %v", err)
	}
	if err := db.Model(&song2).Association("Artists").Append(&artist); err != nil {
		t.Fatalf("append song2 artist: %v", err)
	}

	if err := db.Create(&model.ArtistBookmark{UserID: user.ID, ArtistID: artist.ID}).Error; err != nil {
		t.Fatalf("create artist bookmark: %v", err)
	}
	if err := db.Create(&model.AlbumBookmark{UserID: user.ID, AlbumID: album.ID}).Error; err != nil {
		t.Fatalf("create album bookmark: %v", err)
	}

	r := newMusicHTTPRouter(service, &user)

	artistListW := httptest.NewRecorder()
	artistListReq := httptest.NewRequest(http.MethodGet, "/api/v1/music/artists", nil)
	r.ServeHTTP(artistListW, artistListReq)
	if artistListW.Code != http.StatusOK {
		t.Fatalf("expected artist list 200, got %d: %s", artistListW.Code, artistListW.Body.String())
	}
	var artistListResp struct {
		Data []struct {
			ID            string `json:"id"`
			PlayCount     int64  `json:"play_count"`
			BookmarkCount int64  `json:"bookmark_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(artistListW.Body.Bytes(), &artistListResp); err != nil {
		t.Fatalf("decode artist list response: %v", err)
	}
	if len(artistListResp.Data) != 1 || artistListResp.Data[0].PlayCount != 12 || artistListResp.Data[0].BookmarkCount != 1 {
		t.Fatalf("unexpected artist stats response: %#v", artistListResp.Data)
	}

	albumDetailW := httptest.NewRecorder()
	albumDetailReq := httptest.NewRequest(http.MethodGet, "/api/v1/music/albums/"+album.ID.String(), nil)
	r.ServeHTTP(albumDetailW, albumDetailReq)
	if albumDetailW.Code != http.StatusOK {
		t.Fatalf("expected album detail 200, got %d: %s", albumDetailW.Code, albumDetailW.Body.String())
	}
	var albumDetailResp struct {
		Data struct {
			ID            string `json:"id"`
			PlayCount     int64  `json:"play_count"`
			BookmarkCount int64  `json:"bookmark_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(albumDetailW.Body.Bytes(), &albumDetailResp); err != nil {
		t.Fatalf("decode album detail response: %v", err)
	}
	if albumDetailResp.Data.PlayCount != 12 || albumDetailResp.Data.BookmarkCount != 1 {
		t.Fatalf("unexpected album stats response: %#v", albumDetailResp.Data)
	}
}

func TestRegisterRoutesRecordSongPlayIncrementsCount(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	song := model.Song{Title: "Play Me", AudioURL: "/audio/play-me.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/music/plays", bytes.NewBufferString(`{"song_id":"`+song.ID.String()+`"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var updated model.Song
	if err := db.First(&updated, "id = ?", song.ID).Error; err != nil {
		t.Fatalf("reload song: %v", err)
	}
	if updated.PlayCount != 1 {
		t.Fatalf("expected play_count=1, got %d", updated.PlayCount)
	}

	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, "/api/v1/music/plays", bytes.NewBufferString(`{"song_id":"`+song.ID.String()+`"}`))
	secondReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(second, secondReq)
	if second.Code != http.StatusOK {
		t.Fatalf("expected second play 200, got %d: %s", second.Code, second.Body.String())
	}

	var history model.MusicListeningHistory
	if err := db.Where("user_id = ? AND song_id = ?", user.ID, song.ID).First(&history).Error; err != nil {
		t.Fatalf("load listening history: %v", err)
	}
	if history.PlayCount != 2 {
		t.Fatalf("expected one history row with play_count=2, got %#v", history)
	}
}

func TestRegisterRoutesReordersPlaylistSongs(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	playlist := model.Playlist{UserID: user.ID, Name: "Ordered Playlist"}
	if err := db.Create(&playlist).Error; err != nil {
		t.Fatalf("create playlist: %v", err)
	}
	songs := []model.Song{
		{Title: "First", AudioURL: "/audio/first.mp3", Status: "open"},
		{Title: "Second", AudioURL: "/audio/second.mp3", Status: "open"},
		{Title: "Third", AudioURL: "/audio/third.mp3", Status: "open"},
	}
	if err := db.Create(&songs).Error; err != nil {
		t.Fatalf("create songs: %v", err)
	}
	for _, song := range songs {
		if _, err := service.AddPlaylistSong(user, playlist.ID, song.ID); err != nil {
			t.Fatalf("add playlist song: %v", err)
		}
	}
	r := newMusicHTTPRouter(service, &user)

	body, _ := json.Marshal(map[string]any{"song_ids": []string{
		songs[2].ID.String(),
		songs[0].ID.String(),
		songs[1].ID.String(),
	}})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/music/playlists/"+playlist.ID.String()+"/songs/order", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected reorder 200, got %d: %s", w.Code, w.Body.String())
	}

	listW := httptest.NewRecorder()
	r.ServeHTTP(listW, httptest.NewRequest(http.MethodGet, "/api/v1/music/playlists/"+playlist.ID.String()+"/songs?page_size=20", nil))
	if listW.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d: %s", listW.Code, listW.Body.String())
	}
	var response struct {
		Data []struct {
			Position int `json:"position"`
			Song     struct {
				ID string `json:"id"`
			} `json:"song"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listW.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode playlist songs: %v", err)
	}
	if len(response.Data) != 3 || response.Data[0].Song.ID != songs[2].ID.String() || response.Data[0].Position != 1 || response.Data[2].Position != 3 {
		t.Fatalf("unexpected reordered songs: %#v", response.Data)
	}
}

func TestRegisterRoutesRecordSongPlayWithoutUserDoesNotCreateHistory(t *testing.T) {
	service, db, _ := newMusicHTTPTestService(t)
	song := model.Song{Title: "Anonymous Play", AudioURL: "/audio/anonymous.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	r := newMusicHTTPRouter(service, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/music/plays", bytes.NewBufferString(`{"song_id":"`+song.ID.String()+`"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var histories int64
	if err := db.Model(&model.MusicListeningHistory{}).Count(&histories).Error; err != nil {
		t.Fatalf("count histories: %v", err)
	}
	if histories != 0 {
		t.Fatalf("expected no anonymous history, got %d", histories)
	}
}

func TestRegisterRoutesListsCurrentUserListeningHistory(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	song := model.Song{Title: "Recent Song", AudioURL: "/audio/recent.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	if err := service.RecordSongPlay(&user.ID, song.ID); err != nil {
		t.Fatalf("record play: %v", err)
	}
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/music/history?page_size=20", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Data []struct {
			Song struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"song"`
			PlayCount int64 `json:"play_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(response.Data) != 1 || response.Data[0].Song.ID != song.ID.String() || response.Data[0].PlayCount != 1 {
		t.Fatalf("unexpected history response: %#v", response.Data)
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

func TestRegisterRoutesCreatesAlbumImportMultipartPartUpload(t *testing.T) {
	service, _, user := newMusicHTTPTestService(t)
	store := &fakeAlbumImportMultipartStore{
		uploadID:  "upload-http-1",
		signedURL: "https://storage.test/upload-part-2",
	}
	service.albumImportMultipart = store
	r := newMusicHTTPRouter(service, &user)
	multipartState := startAlbumImportMultipartThroughHTTP(t, r)

	body, _ := json.Marshal(CreateAlbumImportMultipartPartInput{
		PartSize: albumImportMultipartPartSize,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/music/imports/albums/"+multipartState.ImportID+"/multipart/parts/2", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data AlbumImportMultipartPartUploadDTO `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.PartNumber != 2 || resp.Data.UploadURL != "https://storage.test/upload-part-2" {
		t.Fatalf("unexpected part upload response: %#v", resp.Data)
	}
	if store.presignKey != multipartState.ObjectKey || store.presignUploadID != "upload-http-1" || store.presignPartNumber != 2 {
		t.Fatalf("unexpected presign call: %#v", store)
	}
}

func TestRegisterRoutesCompletesAlbumImportMultipartPart(t *testing.T) {
	service, _, user := newMusicHTTPTestService(t)
	service.albumImportMultipart = &fakeAlbumImportMultipartStore{uploadID: "upload-http-1"}
	r := newMusicHTTPRouter(service, &user)
	multipartState := startAlbumImportMultipartThroughHTTP(t, r)

	body, _ := json.Marshal(CompleteAlbumImportMultipartPartInput{
		ETag: "etag-1",
		Size: albumImportMultipartPartSize,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/music/imports/albums/"+multipartState.ImportID+"/multipart/parts/1/complete", bytes.NewReader(body))
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
	if len(resp.Data.CompletedParts) != 1 {
		t.Fatalf("expected one completed part, got %#v", resp.Data.CompletedParts)
	}
	part := resp.Data.CompletedParts[0]
	if part.PartNumber != 1 || part.ETag != "etag-1" || part.Size != albumImportMultipartPartSize {
		t.Fatalf("unexpected completed part: %#v", part)
	}
}

func TestRegisterRoutesCompletesAlbumImportMultipart(t *testing.T) {
	service, _, user := newMusicHTTPTestService(t)
	store := &fakeAlbumImportMultipartStore{
		uploadID: "upload-http-1",
		objectBody: newImportTestZipArchive(t, map[string]string{
			"01 - Untitled.mp3": "",
		}),
	}
	service.albumImportMultipart = store
	r := newMusicHTTPRouter(service, &user)
	multipartState := startAlbumImportMultipartThroughHTTP(t, r)

	partBody, _ := json.Marshal(CompleteAlbumImportMultipartPartInput{
		ETag: "etag-1",
		Size: albumImportMultipartPartSize,
	})
	partRecorder := httptest.NewRecorder()
	partReq := httptest.NewRequest(http.MethodPost, "/api/v1/music/imports/albums/"+multipartState.ImportID+"/multipart/parts/1/complete", bytes.NewReader(partBody))
	partReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(partRecorder, partReq)
	if partRecorder.Code != http.StatusOK {
		t.Fatalf("expected part complete 200, got %d: %s", partRecorder.Code, partRecorder.Body.String())
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/music/imports/albums/"+multipartState.ImportID+"/multipart/complete", nil)

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
	if resp.Data.ImportID != multipartState.ImportID || resp.Data.Status != AlbumImportStatusReady {
		t.Fatalf("unexpected final complete response: %#v", resp.Data)
	}
	if resp.Data.ArchiveName != "Untrue.zip" || len(resp.Data.DerivedTracks) != 1 {
		t.Fatalf("expected derived ready import response, got %#v", resp.Data)
	}
	if store.completeKey != multipartState.ObjectKey || len(store.deletedKeys) != 1 {
		t.Fatalf("expected completed object cleanup, got %#v", store)
	}
}

func TestRegisterRoutesAlbumImportMultipartRejectsInvalidPartNumber(t *testing.T) {
	for _, partNumber := range []string{"0", "-1"} {
		t.Run(partNumber, func(t *testing.T) {
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

			body, _ := json.Marshal(CreateAlbumImportMultipartPartInput{
				PartSize: albumImportMultipartPartSize,
			})
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/music/imports/albums/"+createResp.Data.ImportID+"/multipart/parts/"+partNumber, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "validation.invalid_request") {
				t.Fatalf("expected validation.invalid_request, got %s", w.Body.String())
			}
		})
	}
}

func startAlbumImportMultipartThroughHTTP(t *testing.T, r *gin.Engine) AlbumImportMultipartDTO {
	t.Helper()

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
	startRecorder := httptest.NewRecorder()
	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/music/imports/albums/"+createResp.Data.ImportID+"/multipart", bytes.NewReader(startBody))
	startReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(startRecorder, startReq)
	if startRecorder.Code != http.StatusOK {
		t.Fatalf("expected start multipart 200, got %d: %s", startRecorder.Code, startRecorder.Body.String())
	}

	var startResp struct {
		Data AlbumImportMultipartDTO `json:"data"`
	}
	if err := json.Unmarshal(startRecorder.Body.Bytes(), &startResp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	return startResp.Data
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

func TestMusicRecommendationLatestModeReturnsNewestAlbumFirst(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	older := model.Album{Title: "Older Album", EntryStatus: "open", Status: "open", HotScore: 10}
	newer := model.Album{Title: "Newer Album", EntryStatus: "open", Status: "open", HotScore: 1}
	if err := db.Create(&older).Error; err != nil {
		t.Fatalf("create older album: %v", err)
	}
	if err := db.Create(&newer).Error; err != nil {
		t.Fatalf("create newer album: %v", err)
	}
	if err := db.Model(&older).Update("created_at", time.Now().Add(-24*time.Hour)).Error; err != nil {
		t.Fatalf("age older album: %v", err)
	}

	r := newMusicHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/recommend/albums?mode=latest", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []struct {
			ID         string `json:"id"`
			ScoreLabel string `json:"score_label"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) < 2 || resp.Data[0].ID != newer.ID.String() {
		t.Fatalf("expected newest album first, got %#v", resp.Data)
	}
	if resp.Data[0].ScoreLabel != "最新" {
		t.Fatalf("expected latest score label, got %q", resp.Data[0].ScoreLabel)
	}
}

func TestRegisterRoutesDiscoverAcceptsLatestMode(t *testing.T) {
	service, _, _ := newMusicHTTPTestService(t)
	r := newMusicHTTPRouter(service, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/discover?mode=latest&page_size=10", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
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
	song := model.Song{Title: "Recommend Song", AudioURL: "/audio/recommend-song.mp3", Status: "open", AlbumID: &album.ID, PlayCount: 3}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	if err := db.Create(&model.AlbumBookmark{UserID: user.ID, AlbumID: album.ID}).Error; err != nil {
		t.Fatalf("create album bookmark: %v", err)
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
			ID            string `json:"id"`
			Title         string `json:"title"`
			Summary       string `json:"summary"`
			ImageURL      string `json:"image_url"`
			TargetPath    string `json:"target_path"`
			ScoreLabel    string `json:"score_label"`
			PlayCount     int64  `json:"play_count"`
			BookmarkCount int64  `json:"bookmark_count"`
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
	if first.PlayCount != 3 || first.BookmarkCount != 1 {
		t.Fatalf("expected recommendation stats, got %#v", first)
	}
	if resp.Meta.Total == 0 {
		t.Fatalf("expected total > 0, got %#v", resp.Meta)
	}
}

func TestMusicRecommendationArtistsReturnsData(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	artist := model.Artist{
		Name:        "Recommend Artist",
		EntryStatus: "open",
	}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	song := model.Song{Title: "Recommend Artist Song", AudioURL: "/audio/recommend-artist-song.mp3", Status: "open", PlayCount: 4}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	if err := db.Model(&song).Association("Artists").Append(&artist); err != nil {
		t.Fatalf("append song artist: %v", err)
	}
	if err := db.Create(&model.ArtistBookmark{UserID: user.ID, ArtistID: artist.ID}).Error; err != nil {
		t.Fatalf("create artist bookmark: %v", err)
	}
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/recommend/artists?mode=hot", nil)

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []struct {
			ID            string `json:"id"`
			Title         string `json:"title"`
			Summary       string `json:"summary"`
			ImageURL      string `json:"image_url"`
			TargetPath    string `json:"target_path"`
			ScoreLabel    string `json:"score_label"`
			PlayCount     int64  `json:"play_count"`
			BookmarkCount int64  `json:"bookmark_count"`
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
	if first.TargetPath != "/music/artist/"+artist.ID.String() {
		t.Fatalf("expected target path %s, got %s", "/music/artist/"+artist.ID.String(), first.TargetPath)
	}
	if first.PlayCount != 4 || first.BookmarkCount != 1 {
		t.Fatalf("expected recommendation stats, got %#v", first)
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

func TestRegisterRoutesArtistBookmarksSupportPopularSort(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	hotArtist := model.Artist{Name: "Hot Artist", EntryStatus: "open"}
	coldArtist := model.Artist{Name: "Cold Artist", EntryStatus: "open"}
	if err := db.Create(&hotArtist).Error; err != nil {
		t.Fatalf("create hot artist: %v", err)
	}
	if err := db.Create(&coldArtist).Error; err != nil {
		t.Fatalf("create cold artist: %v", err)
	}
	hotSong := model.Song{Title: "Hot Song", AudioURL: "/audio/hot.mp3", Status: "open", PlayCount: 100}
	coldSong := model.Song{Title: "Cold Song", AudioURL: "/audio/cold.mp3", Status: "open", PlayCount: 1}
	if err := db.Create(&hotSong).Error; err != nil {
		t.Fatalf("create hot song: %v", err)
	}
	if err := db.Create(&coldSong).Error; err != nil {
		t.Fatalf("create cold song: %v", err)
	}
	if err := db.Model(&hotSong).Association("Artists").Append(&hotArtist); err != nil {
		t.Fatalf("append hot artist: %v", err)
	}
	if err := db.Model(&coldSong).Association("Artists").Append(&coldArtist); err != nil {
		t.Fatalf("append cold artist: %v", err)
	}
	if _, err := service.BookmarkArtist(user, coldArtist.ID); err != nil {
		t.Fatalf("bookmark cold artist: %v", err)
	}
	if _, err := service.BookmarkArtist(user, hotArtist.ID); err != nil {
		t.Fatalf("bookmark hot artist: %v", err)
	}

	r := newMusicHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/bookmarks/artists?sort=popular", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []struct {
			ArtistID string `json:"artist_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) < 2 {
		t.Fatalf("expected 2 bookmarks, got %s", w.Body.String())
	}
	if resp.Data[0].ArtistID != hotArtist.ID.String() {
		t.Fatalf("expected hot artist first, got %#v", resp.Data)
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

func TestRegisterRoutesUpdatesOwnPlaylistThroughMusicV1(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	otherModel := model.User{Username: "playlist-patch-other", Email: "playlist-patch-other@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&otherModel).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}
	otherUser := authctx.CurrentUser{ID: otherModel.UUID, Username: otherModel.Username, Role: authctx.RoleUser}

	userRouter := newMusicHTTPRouter(service, &user)
	playlist := createMusicPlaylistViaAPI(t, userRouter, `{"name":"Original Playlist","description":"old","cover_url":"/uploads/old.jpg","is_public":false}`)

	ownerPatch := httptest.NewRecorder()
	ownerPatchReq := httptest.NewRequest(http.MethodPatch, "/api/v1/music/playlists/"+playlist.ID, bytes.NewBufferString(`{"name":"Updated Playlist","description":"new","cover_url":"/uploads/new.jpg","is_public":true}`))
	ownerPatchReq.Header.Set("Content-Type", "application/json")
	userRouter.ServeHTTP(ownerPatch, ownerPatchReq)
	if ownerPatch.Code != http.StatusOK {
		t.Fatalf("expected owner patch 200, got %d: %s", ownerPatch.Code, ownerPatch.Body.String())
	}

	var ownerResp struct {
		Data struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			CoverURL    string `json:"cover_url"`
			IsPublic    bool   `json:"is_public"`
			UserID      string `json:"user_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(ownerPatch.Body.Bytes(), &ownerResp); err != nil {
		t.Fatalf("decode owner patch response: %v", err)
	}
	if ownerResp.Data.Name != "Updated Playlist" || ownerResp.Data.Description != "new" || ownerResp.Data.CoverURL == "" || !ownerResp.Data.IsPublic || ownerResp.Data.UserID != user.ID.String() {
		t.Fatalf("unexpected playlist response: %#v", ownerResp.Data)
	}

	otherRouter := newMusicHTTPRouter(service, &otherUser)
	otherPatch := httptest.NewRecorder()
	otherPatchReq := httptest.NewRequest(http.MethodPatch, "/api/v1/music/playlists/"+playlist.ID, bytes.NewBufferString(`{"name":"Taken Playlist"}`))
	otherPatchReq.Header.Set("Content-Type", "application/json")
	otherRouter.ServeHTTP(otherPatch, otherPatchReq)
	if otherPatch.Code != http.StatusNotFound {
		t.Fatalf("expected other user patch 404, got %d: %s", otherPatch.Code, otherPatch.Body.String())
	}

	var persisted model.Playlist
	if err := db.First(&persisted, "id = ?", playlist.ID).Error; err != nil {
		t.Fatalf("load persisted playlist: %v", err)
	}
	if persisted.Name != "Updated Playlist" {
		t.Fatalf("other user changed playlist: %#v", persisted)
	}
}

func TestRegisterRoutesProtectsFavoritePlaylist(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	favorite := model.Playlist{UserID: user.ID, Name: "最爱", IsFavorite: true}
	if err := db.Create(&favorite).Error; err != nil {
		t.Fatalf("create favorite playlist: %v", err)
	}
	router := newMusicHTTPRouter(service, &user)

	listRecorder := httptest.NewRecorder()
	router.ServeHTTP(listRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/music/playlists", nil))
	if listRecorder.Code != http.StatusOK || !strings.Contains(listRecorder.Body.String(), `"is_favorite":true`) {
		t.Fatalf("expected favorite metadata in list response, got %d: %s", listRecorder.Code, listRecorder.Body.String())
	}

	patchRecorder := httptest.NewRecorder()
	patchRequest := httptest.NewRequest(http.MethodPatch, "/api/v1/music/playlists/"+favorite.ID.String(), bytes.NewBufferString(`{"name":"Renamed","is_public":true}`))
	patchRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(patchRecorder, patchRequest)
	if patchRecorder.Code != http.StatusConflict {
		t.Fatalf("expected favorite patch 409, got %d: %s", patchRecorder.Code, patchRecorder.Body.String())
	}

	deleteRecorder := httptest.NewRecorder()
	router.ServeHTTP(deleteRecorder, httptest.NewRequest(http.MethodDelete, "/api/v1/music/playlists/"+favorite.ID.String(), nil))
	if deleteRecorder.Code != http.StatusConflict {
		t.Fatalf("expected favorite delete 409, got %d: %s", deleteRecorder.Code, deleteRecorder.Body.String())
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

func createMusicPlaylistViaAPI(t *testing.T, router *gin.Engine, body string) struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CoverURL    string `json:"cover_url"`
	IsPublic    bool   `json:"is_public"`
	UserID      string `json:"user_id"`
} {
	t.Helper()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/music/playlists", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected create playlist 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			CoverURL    string `json:"cover_url"`
			IsPublic    bool   `json:"is_public"`
			UserID      string `json:"user_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode create playlist response: %v", err)
	}
	return resp.Data
}

func TestRegisterRoutesDiscoverReturnsMixedItems(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)

	artist := model.Artist{
		Name:        "Discover Artist",
		ImageURL:    "/uploads/discover-artist.jpg",
		Bio:         "artist bio",
		EntryStatus: "open",
	}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}

	album := model.Album{
		Title:       "Discover Album",
		CoverURL:    "/uploads/discover-album.jpg",
		EntryStatus: "open",
		Status:      "open",
		HotScore:    8.5,
	}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	if err := db.Model(&album).Association("Artists").Append(&artist); err != nil {
		t.Fatalf("append album artist: %v", err)
	}

	song := model.Song{
		Title:     "Discover Song",
		AudioURL:  "/audio/discover-song.mp3",
		Status:    "open",
		AlbumID:   &album.ID,
		PlayCount: 3,
	}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	if err := db.Model(&song).Association("Artists").Append(&artist); err != nil {
		t.Fatalf("append song artist: %v", err)
	}
	if err := db.Create(&model.AlbumBookmark{UserID: user.ID, AlbumID: album.ID}).Error; err != nil {
		t.Fatalf("create album bookmark: %v", err)
	}
	if err := db.Create(&model.ArtistBookmark{UserID: user.ID, ArtistID: artist.ID}).Error; err != nil {
		t.Fatalf("create artist bookmark: %v", err)
	}

	userRouter := newMusicHTTPRouter(service, &user)
	playlist := createMusicPlaylistViaAPI(t, userRouter, `{"name":"Discover Playlist","description":"playlist desc","cover_url":"/uploads/discover-playlist.jpg","is_public":true}`)
	playlistID, err := uuid.Parse(playlist.ID)
	if err != nil {
		t.Fatalf("parse playlist id: %v", err)
	}
	if err := db.Create(&model.PlaylistSong{PlaylistID: playlistID, SongID: song.ID}).Error; err != nil {
		t.Fatalf("create playlist song: %v", err)
	}

	r := newMusicHTTPRouter(service, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/discover?page_size=10", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []struct {
			Type          string `json:"type"`
			ID            string `json:"id"`
			Title         string `json:"title"`
			Summary       string `json:"summary"`
			ImageURL      string `json:"image_url"`
			TargetPath    string `json:"target_path"`
			PlayCount     int64  `json:"play_count"`
			BookmarkCount int64  `json:"bookmark_count"`
			SongCount     int64  `json:"song_count"`
			OwnerUserID   string `json:"owner_user_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode discover response: %v", err)
	}
	if len(resp.Data) < 3 {
		t.Fatalf("expected at least 3 discover items, got %#v", resp.Data)
	}
	wantTypes := []string{"album", "artist", "playlist"}
	for i, want := range wantTypes {
		if resp.Data[i].Type != want {
			t.Fatalf("expected discover type order %v, got %#v", wantTypes, resp.Data[:3])
		}
	}
	if resp.Data[0].PlayCount != 3 || resp.Data[0].BookmarkCount != 1 {
		t.Fatalf("expected album discover item stats to be present, got %#v", resp.Data[0])
	}
	if resp.Data[1].PlayCount != 3 || resp.Data[1].BookmarkCount != 1 {
		t.Fatalf("expected artist discover item stats to be present, got %#v", resp.Data[1])
	}
	if resp.Data[2].ID != playlistID.String() || resp.Data[2].SongCount != 1 || resp.Data[2].OwnerUserID != user.ID.String() {
		t.Fatalf("unexpected playlist discover item: %#v", resp.Data[2])
	}
}

func TestRegisterRoutesPublicPlaylistsReturnsDiscoverablePlaylists(t *testing.T) {
	service, _, user := newMusicHTTPTestService(t)
	userRouter := newMusicHTTPRouter(service, &user)

	older := createMusicPlaylistViaAPI(t, userRouter, `{"name":"Older Public Playlist","description":"older","cover_url":"/uploads/public-old.jpg","is_public":true}`)
	newer := createMusicPlaylistViaAPI(t, userRouter, `{"name":"Newest Public Playlist","description":"newer","cover_url":"/uploads/public-new.jpg","is_public":true}`)
	_ = createMusicPlaylistViaAPI(t, userRouter, `{"name":"Private Playlist","description":"hidden","cover_url":"/uploads/private.jpg","is_public":false}`)

	r := newMusicHTTPRouter(service, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/playlists/public?page_size=10", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			CoverURL  string `json:"cover_url"`
			IsPublic  bool   `json:"is_public"`
			UserID    string `json:"user_id"`
			SongCount int64  `json:"song_count"`
		} `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode public playlists response: %v", err)
	}
	if resp.Meta.Total != 2 || len(resp.Data) != 2 {
		t.Fatalf("expected 2 public playlists, got %#v %#v", resp.Data, resp.Meta)
	}
	if resp.Data[0].ID != newer.ID || resp.Data[1].ID != older.ID {
		t.Fatalf("expected public playlists ordered by created_at desc, got %#v", resp.Data)
	}
	if !resp.Data[0].IsPublic || !resp.Data[1].IsPublic || resp.Data[0].CoverURL == "" {
		t.Fatalf("expected public playlists only, got %#v", resp.Data)
	}
}

func TestRegisterRoutesDiscoverHidesPrivatePlaylistsFromAnonymousUsers(t *testing.T) {
	service, _, user := newMusicHTTPTestService(t)
	userRouter := newMusicHTTPRouter(service, &user)

	publicPlaylist := createMusicPlaylistViaAPI(t, userRouter, `{"name":"Visible Public Playlist","description":"public","cover_url":"/uploads/visible-public.jpg","is_public":true}`)
	_ = createMusicPlaylistViaAPI(t, userRouter, `{"name":"Hidden Private Playlist","description":"private","cover_url":"/uploads/hidden-private.jpg","is_public":false}`)

	r := newMusicHTTPRouter(service, nil)

	discoverW := httptest.NewRecorder()
	discoverReq := httptest.NewRequest(http.MethodGet, "/api/v1/music/discover?page_size=20", nil)
	r.ServeHTTP(discoverW, discoverReq)
	if discoverW.Code != http.StatusOK {
		t.Fatalf("expected discover 200, got %d: %s", discoverW.Code, discoverW.Body.String())
	}

	var discoverResp struct {
		Data []struct {
			Type  string `json:"type"`
			Title string `json:"title"`
		} `json:"data"`
	}
	if err := json.Unmarshal(discoverW.Body.Bytes(), &discoverResp); err != nil {
		t.Fatalf("decode discover response: %v", err)
	}
	for _, item := range discoverResp.Data {
		if item.Type == "playlist" && item.Title == "Hidden Private Playlist" {
			t.Fatalf("private playlist should not appear in discover response: %#v", discoverResp.Data)
		}
	}

	publicW := httptest.NewRecorder()
	publicReq := httptest.NewRequest(http.MethodGet, "/api/v1/music/playlists/public?page_size=20", nil)
	r.ServeHTTP(publicW, publicReq)
	if publicW.Code != http.StatusOK {
		t.Fatalf("expected public playlists 200, got %d: %s", publicW.Code, publicW.Body.String())
	}

	var publicResp struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(publicW.Body.Bytes(), &publicResp); err != nil {
		t.Fatalf("decode public playlist response: %v", err)
	}
	for _, item := range publicResp.Data {
		if item.Name == "Hidden Private Playlist" {
			t.Fatalf("private playlist should not appear in public playlist response: %#v", publicResp.Data)
		}
	}
	foundPublic := false
	for _, item := range publicResp.Data {
		if item.Name == publicPlaylist.Name {
			foundPublic = true
		}
	}
	if !foundPublic {
		t.Fatalf("expected public playlist in public response: %#v", publicResp.Data)
	}
}

func TestRegisterRoutesAnonymousCanReadPublicPlaylistDetailAndSongs(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	userRouter := newMusicHTTPRouter(service, &user)

	song := model.Song{Title: "Public Playlist Song", AudioURL: "/audio/public-playlist-song.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}

	playlist := createMusicPlaylistViaAPI(t, userRouter, `{"name":"Readable Public Playlist","description":"public desc","cover_url":"/uploads/readable-public.jpg","is_public":true}`)

	addW := httptest.NewRecorder()
	addReq := httptest.NewRequest(http.MethodPost, "/api/v1/music/playlists/"+playlist.ID+"/songs", bytes.NewBufferString(`{"song_id":"`+song.ID.String()+`"}`))
	addReq.Header.Set("Content-Type", "application/json")
	userRouter.ServeHTTP(addW, addReq)
	if addW.Code != http.StatusCreated {
		t.Fatalf("expected add song 201, got %d: %s", addW.Code, addW.Body.String())
	}

	anonRouter := newMusicHTTPRouter(service, nil)

	detailW := httptest.NewRecorder()
	detailReq := httptest.NewRequest(http.MethodGet, "/api/v1/music/playlists/"+playlist.ID, nil)
	anonRouter.ServeHTTP(detailW, detailReq)
	if detailW.Code != http.StatusOK {
		t.Fatalf("expected anonymous detail 200, got %d: %s", detailW.Code, detailW.Body.String())
	}
	var detailResp struct {
		Data struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			CoverURL    string `json:"cover_url"`
			IsPublic    bool   `json:"is_public"`
		} `json:"data"`
	}
	if err := json.Unmarshal(detailW.Body.Bytes(), &detailResp); err != nil {
		t.Fatalf("decode detail response: %v", err)
	}
	if detailResp.Data.ID != playlist.ID || !detailResp.Data.IsPublic || detailResp.Data.CoverURL == "" {
		t.Fatalf("unexpected public playlist detail: %#v", detailResp.Data)
	}

	songsW := httptest.NewRecorder()
	songsReq := httptest.NewRequest(http.MethodGet, "/api/v1/music/playlists/"+playlist.ID+"/songs", nil)
	anonRouter.ServeHTTP(songsW, songsReq)
	if songsW.Code != http.StatusOK {
		t.Fatalf("expected anonymous songs 200, got %d: %s", songsW.Code, songsW.Body.String())
	}
	var songsResp struct {
		Data []struct {
			SongID string `json:"song_id"`
		} `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(songsW.Body.Bytes(), &songsResp); err != nil {
		t.Fatalf("decode songs response: %v", err)
	}
	if songsResp.Meta.Total != 1 || len(songsResp.Data) != 1 || songsResp.Data[0].SongID != song.ID.String() {
		t.Fatalf("unexpected public playlist songs response: %#v %#v", songsResp.Data, songsResp.Meta)
	}
}
