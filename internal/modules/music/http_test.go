package music

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
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
	db := testdb.OpenPostgres(t, "music_http")
	testdb.Migrate(t, db,
		&model.User{},
		&model.Artist{},
		&model.ArtistMember{},
		&model.ArtistAlias{},
		&model.ArtistMerge{},
		&model.Album{},
		&model.AlbumArtist{},
		&model.Song{},
		&model.SongArtist{},
		&model.ArtistBookmark{},
		&model.AlbumBookmark{},
		&model.PlaylistBookmark{},
		&model.Playlist{},
		&model.PlaylistSong{},
		&model.MusicListeningHistory{},
		&model.MusicPlaybackSession{},
		&model.MusicPlaybackProgress{},
		&model.MusicSearchInteraction{},
		&model.AlbumImportSession{},
		&model.MusicAssetUploadSession{},
		&model.AlbumImportFile{},
		&model.AlbumImportJob{},
		&model.MusicEdit{},
		&model.MusicEditVote{},
		&model.MusicEditDecision{},
		&model.MusicEditChange{},
		&model.MusicSongLyric{},
		&model.MusicSongLyricLine{},
		&model.MusicSongLyricVersion{},
		&model.MusicLyricAnnotation{},
		&model.MusicLyricAnnotationVote{},
		&model.Notification{},
		&model.AuditLog{},
		&model.Revision{},
	)

	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	return NewService(db), db, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: authctx.RoleUser}
}

func TestRegisterRoutesAlbumDetailReturnsArtistCredits(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	artist := model.Artist{Name: "Credited Artist", EntryStatus: "open"}
	album := model.Album{Title: "Credited Album", EntryStatus: "open", Status: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	for _, credit := range []model.AlbumArtist{
		{AlbumID: album.ID, ArtistID: artist.ID, Role: "primary", Position: 1},
		{AlbumID: album.ID, ArtistID: artist.ID, Role: "custom", CustomRole: "Mix Engineer", Position: 1},
	} {
		if err := db.Create(&credit).Error; err != nil {
			t.Fatalf("create credit: %v", err)
		}
	}

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/music/albums/"+album.ID.String(), nil)
	newMusicHTTPRouter(service, &user).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data struct {
			Artists []struct {
				ID string `json:"id"`
			} `json:"artists"`
			Credits []struct {
				ArtistID   string `json:"artist_id"`
				Role       string `json:"role"`
				CustomRole string `json:"custom_role"`
				Artist     *struct {
					Name string `json:"name"`
				} `json:"artist"`
			} `json:"artist_credits"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode album detail: %v", err)
	}
	if len(payload.Data.Artists) != 1 || len(payload.Data.Credits) != 2 {
		t.Fatalf("unexpected artist response: %#v", payload.Data)
	}
	hasCustomRole := false
	for _, credit := range payload.Data.Credits {
		hasCustomRole = hasCustomRole || (credit.CustomRole == "Mix Engineer" && credit.Artist != nil)
	}
	if !hasCustomRole {
		t.Fatalf("expected custom role and artist data, got %#v", payload.Data.Credits)
	}
}

func TestRegisterRoutesAlbumDetailReturnsSongAudioSpecification(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	album := model.Album{Title: "Technical Album", EntryStatus: "open", Status: "open"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	song := model.Song{
		Title:              "Technical Track",
		AlbumID:            &album.ID,
		AudioURL:           "https://cdn.test/technical-track.mp3",
		Status:             "open",
		SourceContainer:    "flac",
		SourceCodec:        "flac",
		SourceBitrateKbps:  2763,
		SourceSampleRateHz: 96000,
		SourceBitDepth:     24,
		SourceChannels:     2,
		SourceSizeBytes:    95 * 1024 * 1024,
		SourceLossless:     true,
	}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/music/albums/"+album.ID.String(), nil)
	newMusicHTTPRouter(service, &user).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data struct {
			Songs []struct {
				SourceContainer    string `json:"source_container"`
				SourceCodec        string `json:"source_codec"`
				SourceBitrateKbps  int    `json:"source_bitrate_kbps"`
				SourceSampleRateHz int    `json:"source_sample_rate_hz"`
				SourceBitDepth     int    `json:"source_bit_depth"`
				SourceChannels     int    `json:"source_channels"`
				SourceSizeBytes    int64  `json:"source_size_bytes"`
				SourceLossless     bool   `json:"source_lossless"`
			} `json:"songs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode album detail: %v", err)
	}
	if len(payload.Data.Songs) != 1 {
		t.Fatalf("expected one song, got %#v", payload.Data.Songs)
	}
	got := payload.Data.Songs[0]
	if got.SourceContainer != "flac" || got.SourceCodec != "flac" || got.SourceBitrateKbps != 2763 || got.SourceSampleRateHz != 96000 || got.SourceBitDepth != 24 || got.SourceChannels != 2 || got.SourceSizeBytes != 95*1024*1024 || !got.SourceLossless {
		t.Fatalf("unexpected song audio specification: %#v", got)
	}
}

func TestRegisterRoutesAlbumListReturnsOnlyAlbumEntities(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	artist := model.Artist{Name: "Release Type Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}

	albums := []model.Album{
		{Title: "Studio Album", AlbumType: "album", EntryStatus: "open", Status: "open"},
		{Title: "Artist EP", AlbumType: "ep", EntryStatus: "open", Status: "open"},
	}
	for index := range albums {
		if err := db.Create(&albums[index]).Error; err != nil {
			t.Fatalf("create album: %v", err)
		}
		if err := db.Create(&model.AlbumArtist{AlbumID: albums[index].ID, ArtistID: artist.ID, Role: "primary", Position: 1}).Error; err != nil {
			t.Fatalf("create album artist: %v", err)
		}
	}

	response := httptest.NewRecorder()
	path := "/api/v1/music/albums?artist_id=" + artist.ID.String()
	newMusicHTTPRouter(service, &user).ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected album list to return 200, got %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data []struct {
			Title string `json:"title"`
		} `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode albums: %v", err)
	}
	gotTitles := make([]string, 0, len(payload.Data))
	for _, album := range payload.Data {
		gotTitles = append(gotTitles, album.Title)
	}
	slices.Sort(gotTitles)
	if !slices.Equal(gotTitles, []string{"Artist EP", "Studio Album"}) || payload.Meta.Total != 2 {
		t.Fatalf("unexpected albums: titles=%v total=%d", gotTitles, payload.Meta.Total)
	}
}

func TestRegisterRoutesSongListFiltersArtistAndStandaloneReleaseTypes(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	artist := model.Artist{Name: "Song List Artist", EntryStatus: "open"}
	otherArtist := model.Artist{Name: "Other Song Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	if err := db.Create(&otherArtist).Error; err != nil {
		t.Fatalf("create other artist: %v", err)
	}

	studioAlbum := model.Album{
		Title: "Studio Album", AlbumType: "album", ReleaseDate: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
		SourcesJSON: `[{"type":"url","url":"https://example.test/studio"}]`, EntryStatus: "open", Status: "open",
	}
	if err := db.Create(&studioAlbum).Error; err != nil {
		t.Fatalf("create studio album: %v", err)
	}

	olderType := "leak"
	newerType := "single"
	olderSong := model.Song{Title: "Older Song", ReleaseType: &olderType, ReleaseDate: time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC), AudioURL: "/audio/older.mp3", Status: "open"}
	newerSong := model.Song{Title: "Newer Song", ReleaseType: &newerType, ReleaseDate: time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC), AudioURL: "/audio/newer.mp3", Status: "open"}
	studioSong := model.Song{Title: "Studio Album Track", AlbumID: &studioAlbum.ID, ReleaseDate: studioAlbum.ReleaseDate, AudioURL: "/audio/studio.mp3", Status: "open"}
	otherSong := model.Song{Title: "Other Song", ReleaseType: &newerType, ReleaseDate: newerSong.ReleaseDate, AudioURL: "/audio/other.mp3", Status: "open"}
	for _, song := range []*model.Song{&olderSong, &newerSong, &studioSong, &otherSong} {
		if err := db.Create(song).Error; err != nil {
			t.Fatalf("create song: %v", err)
		}
	}
	credits := []model.SongArtist{
		{SongID: olderSong.ID, ArtistID: artist.ID, Role: "primary", Position: 1},
		{SongID: olderSong.ID, ArtistID: artist.ID, Role: "custom", CustomRole: "Composer", Position: 1},
		{SongID: newerSong.ID, ArtistID: artist.ID, Role: "primary", Position: 1},
		{SongID: studioSong.ID, ArtistID: artist.ID, Role: "primary", Position: 1},
		{SongID: otherSong.ID, ArtistID: otherArtist.ID, Role: "primary", Position: 1},
	}
	for index := range credits {
		if err := db.Create(&credits[index]).Error; err != nil {
			t.Fatalf("create song artist: %v", err)
		}
	}

	response := httptest.NewRecorder()
	path := "/api/v1/music/songs?artist_id=" + artist.ID.String() + "&release_type=single,leak&sort=-release_date"
	newMusicHTTPRouter(service, &user).ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected artist song list to return 200, got %d: %s", response.Code, response.Body.String())
	}

	var payload struct {
		Data []struct {
			Title string `json:"title"`
		} `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode song list response: %v", err)
	}
	if payload.Meta.Total != 2 || len(payload.Data) != 2 {
		t.Fatalf("expected the artist's unique single and leak songs, got total=%d data=%#v", payload.Meta.Total, payload.Data)
	}
	if payload.Data[0].Title != newerSong.Title || payload.Data[1].Title != olderSong.Title {
		t.Fatalf("unexpected release date order: %#v", payload.Data)
	}

	legacyResponse := httptest.NewRecorder()
	legacyPath := "/api/v1/music/songs?artist_id=" + artist.ID.String() + "&release_type=song"
	newMusicHTTPRouter(service, &user).ServeHTTP(legacyResponse, httptest.NewRequest(http.MethodGet, legacyPath, nil))
	if legacyResponse.Code != http.StatusBadRequest {
		t.Fatalf("expected removed release_type=song to return 400, got %d: %s", legacyResponse.Code, legacyResponse.Body.String())
	}

	detailResponse := httptest.NewRecorder()
	detailPath := "/api/v1/music/songs/" + studioSong.ID.String()
	newMusicHTTPRouter(service, &user).ServeHTTP(detailResponse, httptest.NewRequest(http.MethodGet, detailPath, nil))
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("expected album track detail 200, got %d: %s", detailResponse.Code, detailResponse.Body.String())
	}
	var detailPayload struct {
		Data struct {
			Song struct {
				Sources          []model.MusicSource `json:"sources"`
				EffectiveSources []model.MusicSource `json:"effective_sources"`
			} `json:"song"`
		} `json:"data"`
	}
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detailPayload); err != nil {
		t.Fatalf("decode album track detail: %v", err)
	}
	if len(detailPayload.Data.Song.Sources) != 0 || len(detailPayload.Data.Song.EffectiveSources) != 1 || detailPayload.Data.Song.EffectiveSources[0].URL != "https://example.test/studio" {
		t.Fatalf("unexpected effective song sources: %#v", detailPayload.Data.Song)
	}
}

func TestRegisterRoutesPlaylistBookmarksMatchFrontendContract(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	owner := model.User{Username: "playlist-owner", DisplayName: "Playlist Owner", Email: "playlist-owner@example.com", Password: "hash", Role: "user", IsActive: true}
	other := model.User{Username: "playlist-other", Email: "playlist-other@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}
	playlist := model.Playlist{UserID: owner.UUID, Name: "Shared Playlist", IsPublic: true}
	if err := db.Create(&playlist).Error; err != nil {
		t.Fatalf("create playlist: %v", err)
	}
	song := model.Song{Title: "Playlist Bookmark Song", AudioURL: "/audio/bookmark.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	if err := db.Create(&model.PlaylistSong{PlaylistID: playlist.ID, SongID: song.ID}).Error; err != nil {
		t.Fatalf("create playlist song: %v", err)
	}

	userRouter := newMusicHTTPRouter(service, &user)
	otherUser := authctx.CurrentUser{ID: other.UUID, Username: other.Username, Role: authctx.RoleUser}
	otherRouter := newMusicHTTPRouter(service, &otherUser)
	path := "/api/v1/music/bookmarks/playlists"
	body := `{"playlist_id":"` + playlist.ID.String() + `"}`

	for attempt := 0; attempt < 2; attempt++ {
		response := performMusicJSONRequest(t, userRouter, http.MethodPost, path, body)
		if response.Code != http.StatusCreated {
			t.Fatalf("expected post attempt %d to return 201, got %d: %s", attempt+1, response.Code, response.Body.String())
		}
	}
	otherPost := performMusicJSONRequest(t, otherRouter, http.MethodPost, path, body)
	if otherPost.Code != http.StatusCreated {
		t.Fatalf("expected other user post 201, got %d: %s", otherPost.Code, otherPost.Body.String())
	}

	list := performMusicJSONRequest(t, userRouter, http.MethodGet, path, "")
	if list.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d: %s", list.Code, list.Body.String())
	}
	var listResp struct {
		Data []struct {
			PlaylistID string `json:"playlist_id"`
			UserID     string `json:"user_id"`
			Playlist   struct {
				ID            string `json:"id"`
				Name          string `json:"name"`
				OwnerUsername string `json:"owner_username"`
				SongCount     int64  `json:"song_count"`
			} `json:"playlist"`
		} `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode playlist bookmarks: %v", err)
	}
	if listResp.Meta.Total != 1 || len(listResp.Data) != 1 {
		t.Fatalf("expected only current user's bookmark, got %#v", listResp)
	}
	bookmark := listResp.Data[0]
	if bookmark.PlaylistID != playlist.ID.String() || bookmark.UserID != user.ID.String() || bookmark.Playlist.ID != playlist.ID.String() || bookmark.Playlist.Name != playlist.Name || bookmark.Playlist.OwnerUsername != owner.Username || bookmark.Playlist.SongCount != 1 {
		t.Fatalf("unexpected playlist bookmark payload: %#v", bookmark)
	}

	deleted := performMusicJSONRequest(t, userRouter, http.MethodDelete, path+"/"+playlist.ID.String(), "")
	if deleted.Code != http.StatusOK {
		t.Fatalf("expected delete 200, got %d: %s", deleted.Code, deleted.Body.String())
	}
	otherList := performMusicJSONRequest(t, otherRouter, http.MethodGet, path, "")
	if otherList.Code != http.StatusOK || !strings.Contains(otherList.Body.String(), playlist.ID.String()) {
		t.Fatalf("current user delete removed other user's bookmark: %d %s", otherList.Code, otherList.Body.String())
	}
	missingDelete := performMusicJSONRequest(t, userRouter, http.MethodDelete, path+"/"+playlist.ID.String(), "")
	if missingDelete.Code != http.StatusOK {
		t.Fatalf("expected missing bookmark delete to stay idempotent, got %d: %s", missingDelete.Code, missingDelete.Body.String())
	}
	missingPost := performMusicJSONRequest(t, userRouter, http.MethodPost, path, `{"playlist_id":"`+uuid.NewString()+`"}`)
	if missingPost.Code != http.StatusNotFound {
		t.Fatalf("expected nonexistent playlist post 404, got %d: %s", missingPost.Code, missingPost.Body.String())
	}
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

func TestRegisterRoutesMusicSearchAndSongDetailHideNonPublicSongs(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	visible := model.Song{Title: "Visible Search Song", AudioURL: "/visible.mp3", Status: "open"}
	draft := model.Song{Title: "Hidden Search Song", AudioURL: "/draft.mp3", Status: "draft", LifecycleStatus: model.MusicLifecycleDraft}
	if err := db.Create(&visible).Error; err != nil {
		t.Fatalf("create visible song: %v", err)
	}
	if err := db.Create(&draft).Error; err != nil {
		t.Fatalf("create draft song: %v", err)
	}
	router := newMusicHTTPRouter(service, &user)

	search := performMusicJSONRequest(t, router, http.MethodGet, "/api/v1/music/search?q=Search+Song", "")
	if search.Code != http.StatusOK || !strings.Contains(search.Body.String(), visible.ID.String()) || strings.Contains(search.Body.String(), draft.ID.String()) || !strings.Contains(search.Body.String(), `"totals":{"album":0,"artist":0,"playlist":0,"song":1}`) {
		t.Fatalf("search exposed non-public song: %d %s", search.Code, search.Body.String())
	}
	interaction := performMusicJSONRequest(t, router, http.MethodPost, "/api/v1/music/search/interactions", `{"query":"Search Song","entity_type":"song","entity_id":"`+visible.ID.String()+`"}`)
	if interaction.Code != http.StatusNoContent {
		t.Fatalf("expected search interaction 204, got %d: %s", interaction.Code, interaction.Body.String())
	}
	var stored model.MusicSearchInteraction
	if err := db.Where("user_id = ? AND entity_id = ?", user.ID, visible.ID).First(&stored).Error; err != nil || stored.Query != "Search Song" {
		t.Fatalf("search interaction not stored: %#v, %v", stored, err)
	}
	detail := performMusicJSONRequest(t, router, http.MethodGet, "/api/v1/music/songs/"+draft.ID.String(), "")
	assertMusicHTTPError(t, detail, http.StatusNotFound, "music.song_not_found")
}

func TestRegisterRoutesMusicSongDetailHidesInvisibleArtistCredits(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	foreignOwner := uuid.New()
	artist := model.Artist{
		Name:            "Private Artist",
		EntryStatus:     "draft",
		LifecycleStatus: model.MusicLifecycleDraft,
		CreatedBy:       &foreignOwner,
	}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create private artist: %v", err)
	}
	song := model.Song{Title: "Public Song With Private Credit", AudioURL: "/public.mp3", Status: "open", LifecycleStatus: model.MusicLifecycleActive, UploadedBy: &user.ID}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create public song: %v", err)
	}
	if err := db.Model(&song).Association("Artists").Append(&artist); err != nil {
		t.Fatalf("link private artist: %v", err)
	}

	response := performMusicJSONRequest(t, newMusicHTTPRouter(service, nil), http.MethodGet, "/api/v1/music/songs/"+song.ID.String(), "")
	if response.Code != http.StatusOK {
		t.Fatalf("expected public song detail 200, got %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), artist.ID.String()) || strings.Contains(response.Body.String(), artist.Name) {
		t.Fatalf("song detail exposed invisible artist: %s", response.Body.String())
	}
}
func TestRegisterRoutesMusicSearchMatchesLyricsAndArtistAliases(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	artist := model.Artist{Name: "Primary Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	if err := db.Create(&model.ArtistAlias{ArtistID: artist.ID, Alias: "Needle Alias"}).Error; err != nil {
		t.Fatalf("create artist alias: %v", err)
	}
	exact := model.Song{Title: "Needle", AudioURL: "/needle.mp3", Status: "open"}
	prefix := model.Song{Title: "Needle Live", AudioURL: "/needle-live.mp3", Status: "open"}
	lyrics := model.Song{Title: "Lyric Match", AudioURL: "/lyric-match.mp3", Status: "open"}
	for _, song := range []*model.Song{&exact, &prefix, &lyrics} {
		if err := db.Create(song).Error; err != nil {
			t.Fatalf("create song: %v", err)
		}
		if err := db.Model(song).Association("Artists").Append(&artist); err != nil {
			t.Fatalf("link song artist: %v", err)
		}
	}
	if err := db.Create(&model.MusicSongLyric{SongID: lyrics.ID, Content: "a lyric contains the needle", UpdatedBy: user.ID}).Error; err != nil {
		t.Fatalf("create lyrics: %v", err)
	}
	router := newMusicHTTPRouter(service, &user)

	search := performMusicJSONRequest(t, router, http.MethodGet, "/api/v1/music/search?q=Needle&type=song", "")
	if search.Code != http.StatusOK {
		t.Fatalf("song search status = %d, body=%s", search.Code, search.Body.String())
	}
	var songs struct {
		Data struct {
			Songs []model.Song `json:"songs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(search.Body.Bytes(), &songs); err != nil {
		t.Fatalf("decode song search: %v", err)
	}
	if len(songs.Data.Songs) != 3 || songs.Data.Songs[0].ID != exact.ID || songs.Data.Songs[1].ID != prefix.ID {
		t.Fatalf("expected exact then prefix song search order, got %#v", songs.Data.Songs)
	}

	aliasSearch := performMusicJSONRequest(t, router, http.MethodGet, "/api/v1/music/search?q=Needle+Alias", "")
	if aliasSearch.Code != http.StatusOK || !strings.Contains(aliasSearch.Body.String(), artist.ID.String()) || !strings.Contains(aliasSearch.Body.String(), exact.ID.String()) {
		t.Fatalf("artist alias search did not return related entities: %d %s", aliasSearch.Code, aliasSearch.Body.String())
	}
}

func TestRegisterRoutesMusicLaterPlaylistRejectsMissingSong(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	router := newMusicHTTPRouter(service, &user)
	response := performMusicJSONRequest(t, router, http.MethodPost, "/api/v1/music/playlists/later/"+uuid.NewString(), "")
	assertMusicHTTPError(t, response, http.StatusNotFound, "music.song_not_found")
	var count int64
	if err := db.Model(&model.Playlist{}).Where("user_id = ? AND kind = ?", user.ID, "later").Count(&count).Error; err != nil {
		t.Fatalf("count later playlists: %v", err)
	}
	if count != 0 {
		t.Fatalf("missing song created later playlist")
	}
}

func TestRegisterRoutesDeleteMusicLaterSongIsIdempotent(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	song := model.Song{Title: "Later Song", AudioURL: "/later.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	router := newMusicHTTPRouter(service, &user)
	path := "/api/v1/music/playlists/later/" + song.ID.String()

	add := performMusicJSONRequest(t, router, http.MethodPost, path, "")
	if add.Code != http.StatusOK {
		t.Fatalf("expected add 200, got %d: %s", add.Code, add.Body.String())
	}

	for attempt := 1; attempt <= 2; attempt++ {
		remove := performMusicJSONRequest(t, router, http.MethodDelete, path, "")
		if remove.Code != http.StatusOK || !strings.Contains(remove.Body.String(), `"deleted":true`) {
			t.Fatalf("expected delete attempt %d to succeed, got %d: %s", attempt, remove.Code, remove.Body.String())
		}
	}

	var remaining int64
	if err := db.Model(&model.PlaylistSong{}).Where("song_id = ?", song.ID).Count(&remaining).Error; err != nil {
		t.Fatalf("count later playlist songs: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("expected later song to be removed, got %d rows", remaining)
	}
}

func TestRegisterRoutesMusicLyricsLifecycleMatchesFrontendContract(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	song := model.Song{Title: "HTTP Lyrics", AudioURL: "/lyrics.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	userRouter := newMusicHTTPRouter(service, &user)
	anonRouter := newMusicHTTPRouter(service, nil)
	basePath := "/api/v1/music/songs/" + song.ID.String() + "/lyrics"

	anonGet := performMusicJSONRequest(t, anonRouter, http.MethodGet, basePath, "")
	if anonGet.Code != http.StatusOK {
		t.Fatalf("expected anonymous GET 200, got %d: %s", anonGet.Code, anonGet.Body.String())
	}

	anonPut := performMusicJSONRequest(t, anonRouter, http.MethodPut, basePath, `{"content":"[00:01.00]hello","format":"lrc"}`)
	assertMusicHTTPError(t, anonPut, http.StatusUnauthorized, "auth.unauthorized")

	put := performMusicJSONRequest(t, userRouter, http.MethodPut, basePath, `{"content":"[00:01.00]hello\n[00:02.50]world","translation":"[00:01.00]\u4f60\u597d\n[00:02.50]\u4e16\u754c","format":"lrc","edit_summary":"initial"}`)
	if put.Code != http.StatusOK {
		t.Fatalf("expected PUT 200, got %d: %s", put.Code, put.Body.String())
	}
	var saved struct {
		Data MusicLyricsDTO `json:"data"`
	}
	if err := json.Unmarshal(put.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode saved lyrics: %v", err)
	}
	if saved.Data.Format != "lrc" || saved.Data.Translation == "" || len(saved.Data.Lines) != 2 || saved.Data.Lines[0].Translation != "\u4f60\u597d" {
		t.Fatalf("unexpected saved lyrics: %#v", saved.Data)
	}

	get := performMusicJSONRequest(t, anonRouter, http.MethodGet, basePath, "")
	if get.Code != http.StatusOK {
		t.Fatalf("expected GET 200, got %d: %s", get.Code, get.Body.String())
	}
	var read struct {
		Data MusicLyricsDTO `json:"data"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &read); err != nil {
		t.Fatalf("decode read lyrics: %v", err)
	}
	if read.Data.Content != saved.Data.Content || read.Data.Translation != saved.Data.Translation || read.Data.Format != "lrc" {
		t.Fatalf("read lyrics differ from saved lyrics: %#v", read.Data)
	}

	annotationPath := basePath + "/annotations"
	create := performMusicJSONRequest(t, userRouter, http.MethodPost, annotationPath, `{"line_key":"`+saved.Data.Lines[0].LineKey+`","selected_text":"hello","start_offset":0,"end_offset":5,"body":"first note"}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("expected annotation POST 201, got %d: %s", create.Code, create.Body.String())
	}
	var created struct {
		Data MusicLyricAnnotationDTO `json:"data"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created annotation: %v", err)
	}
	if created.Data.ID == uuid.Nil || created.Data.LineKey != saved.Data.Lines[0].LineKey || created.Data.Body != "first note" {
		t.Fatalf("unexpected created annotation: %#v", created.Data)
	}

	itemPath := annotationPath + "/" + created.Data.ID.String()
	patch := performMusicJSONRequest(t, userRouter, http.MethodPatch, itemPath, `{"body":"updated note"}`)
	if patch.Code != http.StatusOK {
		t.Fatalf("expected annotation PATCH 200, got %d: %s", patch.Code, patch.Body.String())
	}
	var updated struct {
		Data MusicLyricAnnotationDTO `json:"data"`
	}
	if err := json.Unmarshal(patch.Body.Bytes(), &updated); err != nil || updated.Data.Body != "updated note" {
		t.Fatalf("unexpected patched annotation: %#v, %v", updated.Data, err)
	}

	votesPath := itemPath + "/votes"
	up := performMusicJSONRequest(t, userRouter, http.MethodPost, votesPath, `{"vote":"up"}`)
	if up.Code != http.StatusOK {
		t.Fatalf("expected vote up 200, got %d: %s", up.Code, up.Body.String())
	}
	var voted struct {
		Data MusicLyricAnnotationDTO `json:"data"`
	}
	if err := json.Unmarshal(up.Body.Bytes(), &voted); err != nil || voted.Data.Upvotes != 1 || voted.Data.ViewerVote != "up" {
		t.Fatalf("unexpected upvote response: %#v, %v", voted.Data, err)
	}
	none := performMusicJSONRequest(t, userRouter, http.MethodPost, votesPath, `{"vote":"none"}`)
	if none.Code != http.StatusOK {
		t.Fatalf("expected vote none 200, got %d: %s", none.Code, none.Body.String())
	}
	if err := json.Unmarshal(none.Body.Bytes(), &voted); err != nil || voted.Data.Upvotes != 0 || voted.Data.ViewerVote != "none" {
		t.Fatalf("unexpected cleared vote response: %#v, %v", voted.Data, err)
	}

	deleted := performMusicJSONRequest(t, userRouter, http.MethodDelete, itemPath, "")
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"deleted":true`) {
		t.Fatalf("expected annotation DELETE 200, got %d: %s", deleted.Code, deleted.Body.String())
	}
}

func TestRegisterRoutesMusicLyricsImportsStructuredLRCAndCreatesRevision(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	song := model.Song{Title: "Imported Lyrics", AudioURL: "/imported-lyrics.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatal(err)
	}
	router := newMusicHTTPRouter(service, &user)
	basePath := "/api/v1/music/songs/" + song.ID.String() + "/lyrics"
	body := `{"target":"import","base_version":0,"translation_included":true,"language":"zh-CN","lines":[{"text":"Closed on Sunday","translation":"周日歇业","time_ms":29890},{"text":"Chick-Fil-A","translation":"福乐鸡","time_ms":150520}],"edit_summary":"导入双语歌词"}`

	response := performMusicJSONRequest(t, router, http.MethodPut, basePath, body)
	if response.Code != http.StatusOK {
		t.Fatalf("import lyrics: %d %s", response.Code, response.Body.String())
	}
	var saved struct {
		Data MusicLyricsDTO `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Data.Version != 1 || saved.Data.Format != "lrc" || saved.Data.TranslationLanguage != "zh-CN" || len(saved.Data.Lines) != 2 {
		t.Fatalf("unexpected imported lyrics: %#v", saved.Data)
	}
	if saved.Data.Lines[0].TimeMS == nil || *saved.Data.Lines[0].TimeMS != 29890 || saved.Data.Lines[0].Translation != "周日歇业" {
		t.Fatalf("unexpected imported first line: %#v", saved.Data.Lines[0])
	}

	versions := performMusicJSONRequest(t, router, http.MethodGet, basePath+"/versions", "")
	if versions.Code != http.StatusOK {
		t.Fatalf("list versions: %d %s", versions.Code, versions.Body.String())
	}
	var listed struct {
		Data []MusicSongLyricsVersionDTO `json:"data"`
	}
	if err := json.Unmarshal(versions.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Data) != 1 || listed.Data[0].Target != "import" || listed.Data[0].Version != 1 {
		t.Fatalf("expected one import revision: %#v", listed.Data)
	}
}

func TestRegisterRoutesListsPendingLyricAnnotationsForCurrentUser(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	album := model.Album{Title: "Pending Annotation Album", EntryStatus: "open", Status: "open"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatal(err)
	}
	song := model.Song{Title: "Pending Annotation Song", AudioURL: "/pending.mp3", Status: "open", AlbumID: &album.ID}
	if err := db.Create(&song).Error; err != nil {
		t.Fatal(err)
	}
	lyrics, err := service.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "hello", Format: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	annotation, err := service.CreateLyricAnnotation(user, song.ID, CreateAnnotationInput{
		LineID: lyrics.Lines[0].ID, SelectedText: "hello", StartOffset: 0, EndOffset: 5, Body: "pending",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.MusicLyricAnnotation{}).Where("id = ?", annotation.ID).Update("status", "needs_rebind").Error; err != nil {
		t.Fatal(err)
	}

	path := "/api/v1/music/lyrics/annotations/pending"
	anon := performMusicJSONRequest(t, newMusicHTTPRouter(service, nil), http.MethodGet, path, "")
	assertMusicHTTPError(t, anon, http.StatusUnauthorized, "auth.unauthorized")

	response := performMusicJSONRequest(t, newMusicHTTPRouter(service, &user), http.MethodGet, path, "")
	if response.Code != http.StatusOK {
		t.Fatalf("expected pending annotations 200, got %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data []PendingLyricAnnotationDTO `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode pending annotations: %v", err)
	}
	if len(payload.Data) != 1 || payload.Data[0].AnnotationID != annotation.ID.String() || payload.Data[0].SongID != song.ID.String() || payload.Data[0].AlbumID != album.ID.String() {
		t.Fatalf("unexpected pending annotations: %#v", payload.Data)
	}
}

func TestRegisterRoutesMusicLyricsRejectsInvalidRequestsAndCrossSongAccess(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	song := model.Song{Title: "Lyrics Validation", AudioURL: "/validation.mp3", Status: "open"}
	otherSong := model.Song{Title: "Other Song", AudioURL: "/other.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	if err := db.Create(&otherSong).Error; err != nil {
		t.Fatalf("create other song: %v", err)
	}
	r := newMusicHTTPRouter(service, &user)
	basePath := "/api/v1/music/songs/" + song.ID.String() + "/lyrics"

	invalidID := performMusicJSONRequest(t, r, http.MethodGet, "/api/v1/music/songs/not-a-uuid/lyrics", "")
	assertMusicHTTPError(t, invalidID, http.StatusBadRequest, "validation.invalid_request")
	missing := performMusicJSONRequest(t, r, http.MethodGet, "/api/v1/music/songs/"+uuid.NewString()+"/lyrics", "")
	assertMusicHTTPError(t, missing, http.StatusNotFound, "music.song_not_found")

	put := performMusicJSONRequest(t, r, http.MethodPut, basePath, `{"content":"hello world","format":"plain"}`)
	if put.Code != http.StatusOK {
		t.Fatalf("seed lyrics: %d: %s", put.Code, put.Body.String())
	}
	var lyrics struct {
		Data MusicLyricsDTO `json:"data"`
	}
	if err := json.Unmarshal(put.Body.Bytes(), &lyrics); err != nil {
		t.Fatalf("decode seed lyrics: %v", err)
	}

	invalidAnchor := performMusicJSONRequest(t, r, http.MethodPost, basePath+"/annotations", `{"line_id":"`+lyrics.Data.Lines[0].ID.String()+`","selected_text":"wrong","start_offset":0,"end_offset":5,"body":"note"}`)
	assertMusicHTTPError(t, invalidAnchor, http.StatusBadRequest, "validation.invalid_request")

	create := performMusicJSONRequest(t, r, http.MethodPost, basePath+"/annotations", `{"line_id":"`+lyrics.Data.Lines[0].ID.String()+`","selected_text":"hello","start_offset":0,"end_offset":5,"body":"note"}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("create annotation: %d: %s", create.Code, create.Body.String())
	}
	var annotation struct {
		Data MusicLyricAnnotationDTO `json:"data"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &annotation); err != nil {
		t.Fatalf("decode annotation: %v", err)
	}
	createSecond := performMusicJSONRequest(t, r, http.MethodPost, basePath+"/annotations", `{"line_id":"`+lyrics.Data.Lines[0].ID.String()+`","selected_text":"world","start_offset":6,"end_offset":11,"body":"second note"}`)
	if createSecond.Code != http.StatusCreated {
		t.Fatalf("create second annotation: %d: %s", createSecond.Code, createSecond.Body.String())
	}
	var secondAnnotation struct {
		Data MusicLyricAnnotationDTO `json:"data"`
	}
	if err := json.Unmarshal(createSecond.Body.Bytes(), &secondAnnotation); err != nil {
		t.Fatalf("decode second annotation: %v", err)
	}
	otherItemPath := "/api/v1/music/songs/" + otherSong.ID.String() + "/lyrics/annotations/" + annotation.Data.ID.String()
	assertMusicHTTPError(t, performMusicJSONRequest(t, r, http.MethodPatch, otherItemPath, `{"body":"cross song"}`), http.StatusNotFound, "music.annotation_not_found")
	assertMusicHTTPError(t, performMusicJSONRequest(t, r, http.MethodPost, otherItemPath+"/votes", `{"vote":"up"}`), http.StatusNotFound, "music.annotation_not_found")
	assertMusicHTTPError(t, performMusicJSONRequest(t, r, http.MethodDelete, otherItemPath, ""), http.StatusNotFound, "music.annotation_not_found")

	conflict := performMusicJSONRequest(t, r, http.MethodPut, basePath, `{"content":"goodbye earth","format":"plain","annotation_resolutions":[]}`)
	assertMusicHTTPError(t, conflict, http.StatusConflict, "music.annotation_anchor_conflict")
	var conflictBody struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(conflict.Body.Bytes(), &conflictBody); err != nil || conflictBody.Error.Details == nil {
		t.Fatalf("expected recognizable conflict details object: %s, %v", conflict.Body.String(), err)
	}
	wantAnnotationIDs := []string{annotation.Data.ID.String(), secondAnnotation.Data.ID.String()}
	slices.Sort(wantAnnotationIDs)
	gotAnnotationIDs, ok := conflictBody.Error.Details["annotation_ids"].([]any)
	if !ok || len(gotAnnotationIDs) != len(wantAnnotationIDs) {
		t.Fatalf("unexpected conflict annotation IDs: %#v", conflictBody.Error.Details)
	}
	for index, wantID := range wantAnnotationIDs {
		if gotAnnotationIDs[index] != wantID {
			t.Fatalf("unexpected conflict annotation IDs: got %#v want %#v", gotAnnotationIDs, wantAnnotationIDs)
		}
	}

	badAnnotationID := performMusicJSONRequest(t, r, http.MethodPatch, basePath+"/annotations/not-a-uuid", `{"body":"x"}`)
	assertMusicHTTPError(t, badAnnotationID, http.StatusBadRequest, "validation.invalid_request")
}

func TestRegisterRoutesMusicLyricsRebindsAnnotationAnchor(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	song := model.Song{Title: "Rebind HTTP", AudioURL: "/rebind.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatal(err)
	}
	r := newMusicHTTPRouter(service, &user)
	basePath := "/api/v1/music/songs/" + song.ID.String() + "/lyrics"
	put := performMusicJSONRequest(t, r, http.MethodPut, basePath, `{"content":"first line\nsecond line","format":"plain"}`)
	if put.Code != http.StatusOK {
		t.Fatalf("seed lyrics: %d: %s", put.Code, put.Body.String())
	}
	var lyrics struct {
		Data MusicLyricsDTO `json:"data"`
	}
	if err := json.Unmarshal(put.Body.Bytes(), &lyrics); err != nil {
		t.Fatalf("decode lyrics: %v", err)
	}
	created := performMusicJSONRequest(t, r, http.MethodPost, basePath+"/annotations", `{"line_key":"`+lyrics.Data.Lines[0].LineKey+`","selected_text":"first","start_offset":0,"end_offset":5,"body":"keep body"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create annotation: %d: %s", created.Code, created.Body.String())
	}
	var annotation struct {
		Data MusicLyricAnnotationDTO `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &annotation); err != nil {
		t.Fatalf("decode annotation: %v", err)
	}
	if err := db.Model(&model.MusicLyricAnnotation{}).Where("id = ?", annotation.Data.ID).Update("status", "needs_rebind").Error; err != nil {
		t.Fatal(err)
	}

	itemPath := basePath + "/annotations/" + annotation.Data.ID.String()
	rebound := performMusicJSONRequest(t, r, http.MethodPatch, itemPath, `{"line_key":"`+lyrics.Data.Lines[1].LineKey+`","selected_text":"second","start_offset":0,"end_offset":6}`)
	if rebound.Code != http.StatusOK {
		t.Fatalf("rebind annotation: %d: %s", rebound.Code, rebound.Body.String())
	}
	var updated struct {
		Data MusicLyricAnnotationDTO `json:"data"`
	}
	if err := json.Unmarshal(rebound.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode rebound annotation: %v", err)
	}
	if updated.Data.Body != "keep body" || updated.Data.LineID != lyrics.Data.Lines[1].ID || updated.Data.Status != "active" {
		t.Fatalf("unexpected rebound annotation: %#v", updated.Data)
	}
}

func TestRegisterRoutesMusicLyricsRejectsInvalidAnnotationRebindPayload(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	song := model.Song{Title: "Rebind Validation", AudioURL: "/rebind-validation.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatal(err)
	}
	r := newMusicHTTPRouter(service, &user)
	basePath := "/api/v1/music/songs/" + song.ID.String() + "/lyrics"
	lyrics, err := service.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "hello", Format: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	annotation, err := service.CreateLyricAnnotation(user, song.ID, CreateAnnotationInput{LineKey: lyrics.Lines[0].LineKey, SelectedText: "hello", StartOffset: 0, EndOffset: 5, Body: "note"})
	if err != nil {
		t.Fatal(err)
	}
	path := basePath + "/annotations/" + annotation.ID.String()
	assertMusicHTTPError(t, performMusicJSONRequest(t, r, http.MethodPatch, path, `{}`), http.StatusBadRequest, "validation.invalid_request")
	assertMusicHTTPError(t, performMusicJSONRequest(t, r, http.MethodPatch, path, `{"line_key":"`+lyrics.Lines[0].LineKey+`"}`), http.StatusBadRequest, "validation.invalid_request")
	assertMusicHTTPError(t, performMusicJSONRequest(t, r, http.MethodPatch, path, `{"body":"   "}`), http.StatusBadRequest, "validation.invalid_request")
	assertMusicHTTPError(t, performMusicJSONRequest(t, r, http.MethodPatch, path, `{"line_key":"`+lyrics.Lines[0].LineKey+`","selected_text":"wrong","start_offset":0,"end_offset":5}`), http.StatusBadRequest, "validation.invalid_request")
}

func TestRegisterRoutesMusicLyricsVersionsAndRevert(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	song := model.Song{Title: "Version HTTP", AudioURL: "/versions.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatal(err)
	}
	userRouter := newMusicHTTPRouter(service, &user)
	anonRouter := newMusicHTTPRouter(service, nil)
	basePath := "/api/v1/music/songs/" + song.ID.String() + "/lyrics"
	for _, content := range []string{"first", "second"} {
		response := performMusicJSONRequest(t, userRouter, http.MethodPut, basePath, `{"content":"`+content+`","format":"plain"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("save %s: %d %s", content, response.Code, response.Body.String())
		}
	}

	list := performMusicJSONRequest(t, anonRouter, http.MethodGet, basePath+"/versions", "")
	if list.Code != http.StatusOK {
		t.Fatalf("anonymous list: %d %s", list.Code, list.Body.String())
	}
	var listed struct {
		Data []MusicSongLyricsVersionDTO `json:"data"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil || len(listed.Data) != 2 || listed.Data[0].Version != 2 {
		t.Fatalf("unexpected list: %#v, %v", listed.Data, err)
	}

	assertMusicHTTPError(t, performMusicJSONRequest(t, anonRouter, http.MethodPost, basePath+"/versions/1/revert", `{}`), http.StatusUnauthorized, "auth.unauthorized")
	assertMusicHTTPError(t, performMusicJSONRequest(t, userRouter, http.MethodPost, basePath+"/versions/zero/revert", `{}`), http.StatusBadRequest, "validation.invalid_request")
	assertMusicHTTPError(t, performMusicJSONRequest(t, userRouter, http.MethodPost, basePath+"/versions/0/revert", `{}`), http.StatusBadRequest, "validation.invalid_request")
	assertMusicHTTPError(t, performMusicJSONRequest(t, userRouter, http.MethodPost, basePath+"/versions/99/revert", `{}`), http.StatusNotFound, "music.lyrics_version_not_found")
	revert := performMusicJSONRequest(t, userRouter, http.MethodPost, basePath+"/versions/1/revert", `{"edit_summary":"back"}`)
	if revert.Code != http.StatusOK {
		t.Fatalf("revert: %d %s", revert.Code, revert.Body.String())
	}
	var reverted struct {
		Data MusicLyricsDTO `json:"data"`
	}
	if err := json.Unmarshal(revert.Body.Bytes(), &reverted); err != nil || reverted.Data.Version != 3 || reverted.Data.Content != "first" || reverted.Data.EditSummary != "back" {
		t.Fatalf("unexpected revert: %#v, %v", reverted.Data, err)
	}
}

func TestRegisterRoutesMusicLyricsVoteRequiresVoteField(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	song := model.Song{Title: "Vote HTTP", AudioURL: "/vote.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatal(err)
	}
	lyrics, _ := service.SaveSongLyrics(user, song.ID, SaveLyricsInput{Content: "hello", Format: "plain"})
	annotation, _ := service.CreateLyricAnnotation(user, song.ID, CreateAnnotationInput{LineID: lyrics.Lines[0].ID, SelectedText: "hello", StartOffset: 0, EndOffset: 5, Body: "note"})
	router := newMusicHTTPRouter(service, &user)
	path := "/api/v1/music/songs/" + song.ID.String() + "/lyrics/annotations/" + annotation.ID.String() + "/votes"
	assertMusicHTTPError(t, performMusicJSONRequest(t, router, http.MethodPost, path, ""), http.StatusBadRequest, "validation.invalid_request")
	assertMusicHTTPError(t, performMusicJSONRequest(t, router, http.MethodPost, path, `{}`), http.StatusBadRequest, "validation.invalid_request")
	clear := performMusicJSONRequest(t, router, http.MethodPost, path, `{"vote":"none"}`)
	if clear.Code != http.StatusOK {
		t.Fatalf("explicit none should clear vote: %d %s", clear.Code, clear.Body.String())
	}
}

func performMusicJSONRequest(t *testing.T, r http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	r.ServeHTTP(w, req)
	return w
}

func assertMusicHTTPError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("expected status %d, got %d: %s", status, w.Code, w.Body.String())
	}
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if response.Error.Code != code {
		t.Fatalf("expected error code %q, got %q: %s", code, response.Error.Code, w.Body.String())
	}
}

func TestRegisterRoutesMergesAlbumsForAdmin(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	user.Role = authctx.RoleAdmin
	target := model.Album{Title: "HTTP Target Album", EntryStatus: "open", Status: "open"}
	source := model.Album{Title: "HTTP Source Album", EntryStatus: "open", Status: "open"}
	if err := db.Create(&target).Error; err != nil {
		t.Fatalf("create target album: %v", err)
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source album: %v", err)
	}
	r := newMusicHTTPRouter(service, &user)
	body := `{"source_album_id":"` + source.ID.String() + `","confirmed":true,"song_matches":[]}`
	w := performMusicJSONRequest(t, r, http.MethodPost, "/api/v1/music/albums/"+target.ID.String()+"/merge", body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Merged     bool      `json:"merged"`
			RedirectTo uuid.UUID `json:"redirect_to"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Data.Merged || resp.Data.RedirectTo != target.ID {
		t.Fatalf("unexpected album merge response: %#v", resp.Data)
	}
	var mergedSource model.Album
	if err := db.First(&mergedSource, "id = ?", source.ID).Error; err != nil {
		t.Fatalf("load merged source album: %v", err)
	}
	if mergedSource.RedirectTo == nil || *mergedSource.RedirectTo != target.ID || mergedSource.Status != "closed" {
		t.Fatalf("unexpected merged source album: %#v", mergedSource)
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

func TestHydrateArtistDisplayImagesUsesAlbumCoverWithoutPersistingIt(t *testing.T) {
	service, db, _ := newMusicHTTPTestService(t)
	missingImageArtist := model.Artist{Name: "Album Cover Artist", EntryStatus: "open"}
	portraitArtist := model.Artist{Name: "Portrait Artist", ImageURL: "/uploads/portrait.jpg", EntryStatus: "open"}
	if err := db.Create(&missingImageArtist).Error; err != nil {
		t.Fatalf("create artist without portrait: %v", err)
	}
	if err := db.Create(&portraitArtist).Error; err != nil {
		t.Fatalf("create artist with portrait: %v", err)
	}

	coverAlbum := model.Album{Title: "Fallback Cover", CoverURL: "/uploads/album-cover.jpg", EntryStatus: "open", Status: "open"}
	portraitAlbum := model.Album{Title: "Ignored Cover", CoverURL: "/uploads/other-cover.jpg", EntryStatus: "open", Status: "open"}
	if err := db.Create(&coverAlbum).Error; err != nil {
		t.Fatalf("create fallback album: %v", err)
	}
	if err := db.Create(&portraitAlbum).Error; err != nil {
		t.Fatalf("create portrait album: %v", err)
	}
	if err := db.Model(&coverAlbum).Association("Artists").Append(&missingImageArtist); err != nil {
		t.Fatalf("associate fallback album: %v", err)
	}
	if err := db.Model(&portraitAlbum).Association("Artists").Append(&portraitArtist); err != nil {
		t.Fatalf("associate portrait album: %v", err)
	}

	artists := []model.Artist{missingImageArtist, portraitArtist}
	if err := hydrateArtistDisplayImages(service.db, artists); err != nil {
		t.Fatalf("hydrate display images: %v", err)
	}
	if artists[0].ImageURL != "/uploads/album-cover.jpg" {
		t.Fatalf("expected album cover fallback, got %q", artists[0].ImageURL)
	}
	if artists[1].ImageURL != "/uploads/portrait.jpg" {
		t.Fatalf("expected dedicated portrait to win, got %q", artists[1].ImageURL)
	}

	var persisted model.Artist
	if err := db.First(&persisted, "id = ?", missingImageArtist.ID).Error; err != nil {
		t.Fatalf("reload artist: %v", err)
	}
	if persisted.ImageURL != "" {
		t.Fatalf("display fallback must not persist as artist image, got %q", persisted.ImageURL)
	}
}

func TestRegisterRoutesCreatesArtistThroughMusicV1(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	r := newMusicHTTPRouter(service, &user)
	member := model.Artist{Name: "Existing Member", ArtistForm: "person", EntryStatus: "open"}
	if err := db.Create(&member).Error; err != nil {
		t.Fatalf("create member artist: %v", err)
	}
	secondMember := model.Artist{Name: "Second Existing Member", ArtistForm: "person", EntryStatus: "open"}
	if err := db.Create(&secondMember).Error; err != nil {
		t.Fatalf("create second member artist: %v", err)
	}

	w := httptest.NewRecorder()
	body := `{
		"name":"New Music Group",
		"legal_name":"New Music Group LLC",
		"stage_names":[{"name":"NMG","is_primary":true,"start_date_text":"2020","end_date_text":""}],
		"bio":"artist bio",
		"image_url":"/uploads/artist.jpg",
		"nationality":"JP",
		"birth_place":"Tokyo",
		"birth_date":"1990-05-21",
		"artist_form":"group",
		"active_start_date":"2020-01-01",
		"active_end_date":"2026-08-07",
		"members":[{"artist_id":"` + member.ID.String() + `","join_date":"2020-01-01","leave_date":""},{"artist_id":"` + secondMember.ID.String() + `","join_date":"2020-01-01","leave_date":""}],
		"sources":[{"title":"artist source"}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/music/artists", bytes.NewBufferString(body))
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
	if resp.Data.ID == uuid.Nil || resp.Data.Name != "New Music Group" || resp.Data.LegalName != "New Music Group LLC" || resp.Data.Bio != "artist bio" || resp.Data.Nationality != "JP" || resp.Data.BirthPlace != "Tokyo" || resp.Data.BirthYear != 1990 || resp.Data.ArtistForm != "group" || resp.Data.EntryStatus != artistEntryDraft {
		t.Fatalf("unexpected artist response: %#v", resp.Data)
	}

	var persisted model.Artist
	if err := db.First(&persisted, "id = ?", resp.Data.ID).Error; err != nil {
		t.Fatalf("load persisted artist: %v", err)
	}
	if persisted.Name != "New Music Group" || persisted.StageNamesJSON == "" || persisted.ActiveStartDate.Format("2006-01-02") != "2020-01-01" || persisted.ActiveEndDate.Format("2006-01-02") != "2026-08-07" {
		t.Fatalf("unexpected persisted artist: %#v", persisted)
	}
	var relation model.ArtistMember
	if err := db.First(&relation, "group_artist_id = ? AND member_artist_id = ?", persisted.ID, member.ID).Error; err != nil {
		t.Fatalf("load persisted member relation: %v", err)
	}
	var revision model.Revision
	if err := db.Where("content_type = ? AND content_id = ?", "artist", persisted.ID).First(&revision).Error; err != nil {
		t.Fatalf("load initial revision: %v", err)
	}
	if revision.VersionNumber != 1 || revision.EditType != "creation" || revision.Status != "approved" || !revision.IsCurrent {
		t.Fatalf("unexpected initial revision: %#v", revision)
	}
}

func TestRegisterRoutesDoesNotExposeDirectArtistUpdates(t *testing.T) {
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

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected direct artist update route to be unavailable, got %d: %s", w.Code, w.Body.String())
	}

	var persisted model.Artist
	if err := db.First(&persisted, "id = ?", artist.ID).Error; err != nil {
		t.Fatalf("load persisted artist: %v", err)
	}
	if persisted.Name != "Before Artist" || persisted.Bio != "before" {
		t.Fatalf("direct update changed artist without an edit record: %#v", persisted)
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

func TestRegisterRoutesGetArtistIncludesAlbumSongs(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	artist := model.Artist{Name: "Track Count Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	album := model.Album{Title: "Track Count Album", Status: "open", EntryStatus: "open", Artists: []model.Artist{artist}}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	songs := []model.Song{
		{Title: "Track One", AudioURL: "/audio/one.mp3", Status: "open", AlbumID: &album.ID},
		{Title: "Track Two", AudioURL: "/audio/two.mp3", Status: "open", AlbumID: &album.ID},
	}
	if err := db.Create(&songs).Error; err != nil {
		t.Fatalf("create songs: %v", err)
	}

	router := newMusicHTTPRouter(service, &user)
	response := performMusicJSONRequest(t, router, http.MethodGet, "/api/v1/music/artists/"+artist.ID.String(), "")
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}

	var payload struct {
		Data struct {
			Albums []struct {
				ID    string `json:"id"`
				Songs []struct {
					ID string `json:"id"`
				} `json:"songs"`
			} `json:"albums"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode artist response: %v", err)
	}
	if len(payload.Data.Albums) != 1 || payload.Data.Albums[0].ID != album.ID.String() {
		t.Fatalf("unexpected artist albums: %#v", payload.Data.Albums)
	}
	if len(payload.Data.Albums[0].Songs) != len(songs) {
		t.Fatalf("album songs = %d, want %d", len(payload.Data.Albums[0].Songs), len(songs))
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

func TestRegisterRoutesPlaybackProgressRoundTrip(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	song := model.Song{Title: "Resume Song", AudioURL: "/resume.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	router := newMusicHTTPRouter(service, &user)

	save := performMusicJSONRequest(t, router, http.MethodPut, "/api/v1/music/playback-progress", `{"song_id":"`+song.ID.String()+`","position_seconds":42.5,"duration_seconds":180,"completed":false}`)
	if save.Code != http.StatusOK {
		t.Fatalf("save status = %d, body=%s", save.Code, save.Body.String())
	}

	load := performMusicJSONRequest(t, router, http.MethodGet, "/api/v1/music/playback-progress", "")
	if load.Code != http.StatusOK {
		t.Fatalf("load status = %d, body=%s", load.Code, load.Body.String())
	}
	var payload struct {
		Data model.MusicPlaybackProgress `json:"data"`
	}
	if err := json.Unmarshal(load.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode playback progress: %v", err)
	}
	if payload.Data.SongID != song.ID || payload.Data.PositionSeconds != 42.5 || payload.Data.DurationSeconds != 180 || payload.Data.Completed || payload.Data.Song == nil || payload.Data.Song.ID != song.ID {
		t.Fatalf("unexpected playback progress: %#v", payload.Data)
	}
}

func TestRegisterRoutesPlaybackSessionRoundTrip(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	first := model.Song{Title: "First Queue Song", AudioURL: "/first.mp3", Status: "open"}
	second := model.Song{Title: "Second Queue Song", AudioURL: "/second.mp3", Status: "open"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first song: %v", err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("create second song: %v", err)
	}
	router := newMusicHTTPRouter(service, &user)
	body := `{"song_ids":["` + second.ID.String() + `","` + first.ID.String() + `"],"current_song_id":"` + first.ID.String() + `","position_seconds":23,"playback_mode":"random"}`
	save := performMusicJSONRequest(t, router, http.MethodPut, "/api/v1/music/playback-session", body)
	if save.Code != http.StatusOK {
		t.Fatalf("save session status = %d, body=%s", save.Code, save.Body.String())
	}
	load := performMusicJSONRequest(t, router, http.MethodGet, "/api/v1/music/playback-session", "")
	if load.Code != http.StatusOK {
		t.Fatalf("load session status = %d, body=%s", load.Code, load.Body.String())
	}
	var payload struct {
		Data PlaybackSessionResponse `json:"data"`
	}
	if err := json.Unmarshal(load.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode playback session: %v", err)
	}
	if payload.Data.CurrentSongID != first.ID || payload.Data.PositionSeconds != 23 || payload.Data.PlaybackMode != "random" || len(payload.Data.Queue) != 2 || payload.Data.Queue[0].ID != second.ID || payload.Data.Queue[1].ID != first.ID {
		t.Fatalf("unexpected playback session: %#v", payload.Data)
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

func TestRegisterRoutesRateLimitsAnonymousPlayReports(t *testing.T) {
	service, db, _ := newMusicHTTPTestService(t)
	song := model.Song{Title: "Rate Limited Play", AudioURL: "/audio/rate-limited.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	r := newMusicHTTPRouter(service, nil)

	for attempt := 0; attempt < 12; attempt++ {
		response := performMusicJSONRequest(t, r, http.MethodPost, "/api/v1/music/plays", `{"song_id":"`+song.ID.String()+`"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("expected play report %d to return 200, got %d: %s", attempt+1, response.Code, response.Body.String())
		}
	}
	limited := performMusicJSONRequest(t, r, http.MethodPost, "/api/v1/music/plays", `{"song_id":"`+song.ID.String()+`"}`)
	assertMusicHTTPError(t, limited, http.StatusTooManyRequests, "music.play_rate_limited")
	if limited.Header().Get("Retry-After") == "" {
		t.Fatal("expected rate-limited response to include Retry-After")
	}

	var persisted model.Song
	if err := db.First(&persisted, "id = ?", song.ID).Error; err != nil {
		t.Fatalf("reload song: %v", err)
	}
	if persisted.PlayCount != 12 {
		t.Fatalf("expected only allowed reports to count, got %d", persisted.PlayCount)
	}
}

func TestRegisterRoutesRejectsPlayReportsForNonPublicSongs(t *testing.T) {
	service, db, _ := newMusicHTTPTestService(t)
	song := model.Song{Title: "Draft Play", AudioURL: "/audio/draft.mp3", Status: "draft", LifecycleStatus: model.MusicLifecycleRetired}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	response := performMusicJSONRequest(t, newMusicHTTPRouter(service, nil), http.MethodPost, "/api/v1/music/plays", `{"song_id":"`+song.ID.String()+`"}`)
	assertMusicHTTPError(t, response, http.StatusNotFound, "music.song_not_found")
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
	service.albumImportMultipart = &fakeAlbumImportMultipartStore{}
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
	if resp.Data.Status != AlbumImportStatusQueued {
		t.Fatalf("expected queued session, got %#v", resp.Data)
	}
	if resp.Data.ArchiveName != "Untrue.zip" {
		t.Fatalf("expected archive name persisted, got %#v", resp.Data.ArchiveName)
	}
}

func TestArchiveUploadRouteRejectsMultipartBodyOverLimitBeforeStorage(t *testing.T) {
	t.Setenv(albumImportMaxFileBytesEnv, "64")
	service, db, user := newMusicHTTPTestService(t)
	store := &fakeAlbumImportMultipartStore{}
	service.albumImportMultipart = store
	session, err := service.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("archive", "album.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("x"), 1024*1024+128)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := newMusicHTTPRouter(service, &user)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/music/imports/albums/"+session.ID.String()+"/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest && w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected oversized request rejection, got %d: %s", w.Code, w.Body.String())
	}
	var jobs, files int64
	if err := db.Model(&model.AlbumImportJob{}).Where("import_id = ?", session.ID).Count(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.AlbumImportFile{}).Where("import_id = ?", session.ID).Count(&files).Error; err != nil {
		t.Fatal(err)
	}
	if len(store.objectBody) != 0 || jobs != 0 || files != 0 {
		t.Fatalf("handler must reject before storage: body=%d jobs=%d files=%d", len(store.objectBody), jobs, files)
	}
}

func TestRegisterRoutesAlbumImportGetRequiresCurrentUser(t *testing.T) {
	service, _, owner := newMusicHTTPTestService(t)
	session, err := service.CreateAlbumImportSession(owner, CreateAlbumImportSessionInput{Status: AlbumImportStatusPendingUpload})
	if err != nil {
		t.Fatalf("create album import session: %v", err)
	}

	response := performMusicJSONRequest(t, newMusicHTTPRouter(service, nil), http.MethodGet, "/api/v1/music/imports/albums/"+session.ID.String(), "")
	assertMusicHTTPError(t, response, http.StatusUnauthorized, "auth.unauthorized")
}

func TestRegisterRoutesAlbumImportGetRejectsAnotherUser(t *testing.T) {
	service, _, owner := newMusicHTTPTestService(t)
	session, err := service.CreateAlbumImportSession(owner, CreateAlbumImportSessionInput{Status: AlbumImportStatusPendingUpload})
	if err != nil {
		t.Fatalf("create album import session: %v", err)
	}
	other := authctx.CurrentUser{ID: uuid.New(), Username: "other", Role: authctx.RoleUser}

	response := performMusicJSONRequest(t, newMusicHTTPRouter(service, &other), http.MethodGet, "/api/v1/music/imports/albums/"+session.ID.String(), "")
	assertMusicHTTPError(t, response, http.StatusNotFound, "music.import_not_found")
}

func TestRegisterRoutesAlbumImportGetRejectsLegacySessionWithoutOwner(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	legacy := model.AlbumImportSession{Status: AlbumImportStatusPendingUpload, PayloadJSON: "{}"}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("create legacy album import session: %v", err)
	}

	response := performMusicJSONRequest(t, newMusicHTTPRouter(service, &user), http.MethodGet, "/api/v1/music/imports/albums/"+legacy.ID.String(), "")
	assertMusicHTTPError(t, response, http.StatusNotFound, "music.import_not_found")
}

func TestRegisterRoutesAlbumImportWriteRejectsAnotherUser(t *testing.T) {
	service, _, owner := newMusicHTTPTestService(t)
	store := &fakeAlbumImportMultipartStore{uploadID: "upload-owner"}
	service.albumImportMultipart = store
	session, err := service.CreateAlbumImportSession(owner, CreateAlbumImportSessionInput{Status: AlbumImportStatusPendingUpload})
	if err != nil {
		t.Fatalf("create album import session: %v", err)
	}
	other := authctx.CurrentUser{ID: uuid.New(), Username: "other", Role: authctx.RoleUser}
	body := `{"fileName":"album.zip","fileSize":1024,"contentType":"application/zip"}`

	response := performMusicJSONRequest(t, newMusicHTTPRouter(service, &other), http.MethodPost, "/api/v1/music/imports/albums/"+session.ID.String()+"/multipart", body)
	assertMusicHTTPError(t, response, http.StatusNotFound, "music.import_not_found")
	if store.createCalls != 0 {
		t.Fatalf("expected no multipart object for another user, got %d creates", store.createCalls)
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
		Size: 64 * 1024 * 1024,
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
	if resp.Data.ImportID != multipartState.ImportID || resp.Data.Status != AlbumImportStatusQueued {
		t.Fatalf("unexpected final complete response: %#v", resp.Data)
	}
	if resp.Data.ArchiveName != "Untrue.zip" || len(resp.Data.DerivedTracks) != 0 || len(resp.Data.Files) != 1 || resp.Data.Files[0].UploadStatus != AlbumImportFileUploadStatusUploaded {
		t.Fatalf("expected queued archive import response, got %#v", resp.Data)
	}
	if store.completeKey != multipartState.ObjectKey || store.openCalls != 0 || len(store.deletedKeys) != 0 {
		t.Fatalf("expected completed source object retained for worker, got %#v", store)
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
	service, db, user := newMusicHTTPTestService(t)
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
	seedReadyImportMedia(t, db, uuid.MustParse(createResp.Data.ImportID), "https://cdn.test/http-cover.jpg", "Track One")

	commitBody, _ := json.Marshal(CommitAlbumImportSessionInput{
		Artist: AlbumImportArtistPayload{
			Name:        "HTTP Artist",
			LegalName:   "HTTP Legal Artist",
			ImageURL:    "/artist.jpg",
			Nationality: "CN",
			BirthDate:   "1990-01-01",
			StageNames: []ArtistStageNamePayload{
				{Name: "HTTP Artist", IsPrimary: true},
			},
			BirthPlace: "Shanghai",
		},
		Album: AlbumImportAlbumPayload{
			Title:       "HTTP Album",
			CoverURL:    "https://cdn.test/http-cover.jpg",
			ReleaseDate: "2026-01-01",
			ReleaseYear: 2026,
			Tracks: []AlbumImportTrackPayload{
				{Title: "Track One", TrackNumber: 1},
			},
		},
		ArtistSource: "artist source",
		AlbumSource:  "album source",
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
			ID        string `json:"id"`
			CoverURL  string `json:"cover_url"`
			SongCount int64  `json:"song_count"`
			Songs     []struct {
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
	if listResp.Data[0].SongCount != 1 || len(listResp.Data[0].Songs) != 0 {
		t.Fatalf("expected list summary with one song, got %#v", listResp.Data[0])
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

func TestRegisterRoutesDiscoverEndpointIsRemoved(t *testing.T) {
	service, _, _ := newMusicHTTPTestService(t)
	r := newMusicHTTPRouter(service, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/discover?mode=latest&page_size=10", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
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
	birthDate := time.Date(1994, time.October, 27, 0, 0, 0, 0, time.UTC)
	artist := model.Artist{
		Name:        "Recommend Artist",
		EntryStatus: "open",
		BirthYear:   1994,
		BirthDate:   &birthDate,
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
			ID            string    `json:"id"`
			Title         string    `json:"title"`
			Summary       string    `json:"summary"`
			ImageURL      string    `json:"image_url"`
			TargetPath    string    `json:"target_path"`
			ScoreLabel    string    `json:"score_label"`
			PlayCount     int64     `json:"play_count"`
			BookmarkCount int64     `json:"bookmark_count"`
			BirthYear     int       `json:"birth_year"`
			BirthDate     time.Time `json:"birth_date"`
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
	if first.BirthYear != 1994 || !first.BirthDate.Equal(birthDate) {
		t.Fatalf("expected artist birth data, got %#v", first)
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

func TestRegisterRoutesDoesNotExposeMusicEditReviewRoutes(t *testing.T) {
	service, _, user := newMusicHTTPTestService(t)
	r := newMusicHTTPRouter(service, &user)
	editID := uuid.NewString()

	paths := []string{
		"/api/v1/music/edits",
		"/api/v1/music/edits/" + editID,
		"/api/v1/music/edits/" + editID + "/votes",
		"/api/v1/music/edits/" + editID + "/approve",
		"/api/v1/music/edits/" + editID + "/reject",
		"/api/v1/music/edits/" + editID + "/cancel",
	}
	for _, path := range paths {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected %s to be unmounted, got %d: %s", path, w.Code, w.Body.String())
		}
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

func TestRegisterRoutesPlaylistSongStatusReturnsOnlyRequestedMembers(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	playlist := model.Playlist{UserID: user.ID, Name: "最爱", Kind: "favorite"}
	if err := db.Create(&playlist).Error; err != nil {
		t.Fatalf("create playlist: %v", err)
	}
	member := model.Song{Title: "Member", AudioURL: "/audio/member.mp3", Status: "open"}
	plain := model.Song{Title: "Plain", AudioURL: "/audio/plain.mp3", Status: "open"}
	if err := db.Create(&member).Error; err != nil {
		t.Fatalf("create member song: %v", err)
	}
	if err := db.Create(&plain).Error; err != nil {
		t.Fatalf("create plain song: %v", err)
	}
	if _, err := service.AddPlaylistSong(user, playlist.ID, member.ID); err != nil {
		t.Fatalf("add playlist song: %v", err)
	}

	router := newMusicHTTPRouter(service, &user)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/music/playlists/"+playlist.ID.String()+"/songs/status?song_ids="+member.ID.String()+","+plain.ID.String(), nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), member.ID.String()) || strings.Contains(recorder.Body.String(), plain.ID.String()) {
		t.Fatalf("unexpected playlist status: %s", recorder.Body.String())
	}
}

func TestRegisterRoutesPlaylistSongStatusRequiresCurrentUser(t *testing.T) {
	service, _, _ := newMusicHTTPTestService(t)
	router := newMusicHTTPRouter(service, nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/music/playlists/"+uuid.NewString()+"/songs/status?song_ids="+uuid.NewString(), nil))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestRegisterRoutesPlaylistSongStatusRejectsInvalidSongID(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	playlist := model.Playlist{UserID: user.ID, Name: "Playlist", Kind: "user"}
	if err := db.Create(&playlist).Error; err != nil {
		t.Fatalf("create playlist: %v", err)
	}
	router := newMusicHTTPRouter(service, &user)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/music/playlists/"+playlist.ID.String()+"/songs/status?song_ids=not-a-uuid", nil))

	assertMusicHTTPError(t, recorder, http.StatusBadRequest, "validation.invalid_request")
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

	detailRecorder := httptest.NewRecorder()
	detailRequest := httptest.NewRequest(http.MethodGet, "/api/v1/music/playlists/"+createResp.Data.ID, nil)
	userRouter.ServeHTTP(detailRecorder, detailRequest)
	if detailRecorder.Code != http.StatusOK {
		t.Fatalf("expected playlist detail 200, got %d: %s", detailRecorder.Code, detailRecorder.Body.String())
	}
	var detailResponse struct {
		Data struct {
			SongCount int64 `json:"song_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(detailRecorder.Body.Bytes(), &detailResponse); err != nil {
		t.Fatalf("decode playlist detail: %v", err)
	}
	if detailResponse.Data.SongCount != 1 {
		t.Fatalf("expected playlist detail song_count 1, got %d", detailResponse.Data.SongCount)
	}

	otherRouter := newMusicHTTPRouter(service, &otherUser)
	otherList := httptest.NewRecorder()
	otherListReq := httptest.NewRequest(http.MethodGet, "/api/v1/music/playlists", nil)
	otherRouter.ServeHTTP(otherList, otherListReq)
	if otherList.Code != http.StatusOK {
		t.Fatalf("expected other user playlist list 200, got %d: %s", otherList.Code, otherList.Body.String())
	}

	var otherListResp struct {
		Data []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(otherList.Body.Bytes(), &otherListResp); err != nil {
		t.Fatalf("decode other user playlist list: %v", err)
	}
	if len(otherListResp.Data) != 1 || otherListResp.Meta.Total != 1 || otherListResp.Data[0].Name != "最爱" || otherListResp.Data[0].Kind != "favorite" {
		t.Fatalf("expected only the other user's favorite playlist, got %#v %#v", otherListResp.Data, otherListResp.Meta)
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

func TestRegisterRoutesProtectsSystemPlaylists(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	playlists := []model.Playlist{
		{UserID: user.ID, Name: "最爱", Kind: "favorite"},
		{UserID: user.ID, Name: "稍后播放", Kind: "later"},
	}
	for index := range playlists {
		if err := db.Create(&playlists[index]).Error; err != nil {
			t.Fatalf("create %s playlist: %v", playlists[index].Kind, err)
		}
	}
	router := newMusicHTTPRouter(service, &user)

	listRecorder := httptest.NewRecorder()
	router.ServeHTTP(listRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/music/playlists", nil))
	if listRecorder.Code != http.StatusOK || !strings.Contains(listRecorder.Body.String(), `"kind":"favorite"`) || !strings.Contains(listRecorder.Body.String(), `"kind":"later"`) {
		t.Fatalf("expected system playlist metadata in list response, got %d: %s", listRecorder.Code, listRecorder.Body.String())
	}

	for _, playlist := range playlists {
		patchRecorder := httptest.NewRecorder()
		patchRequest := httptest.NewRequest(http.MethodPatch, "/api/v1/music/playlists/"+playlist.ID.String(), bytes.NewBufferString(`{"name":"Renamed","is_public":true}`))
		patchRequest.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(patchRecorder, patchRequest)
		if patchRecorder.Code != http.StatusConflict {
			t.Fatalf("expected %s patch 409, got %d: %s", playlist.Kind, patchRecorder.Code, patchRecorder.Body.String())
		}

		deleteRecorder := httptest.NewRecorder()
		router.ServeHTTP(deleteRecorder, httptest.NewRequest(http.MethodDelete, "/api/v1/music/playlists/"+playlist.ID.String(), nil))
		if deleteRecorder.Code != http.StatusConflict {
			t.Fatalf("expected %s delete 409, got %d: %s", playlist.Kind, deleteRecorder.Code, deleteRecorder.Body.String())
		}
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

func TestRegisterRoutesMusicHomeOmitsPublicDiscoveryPayload(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/home", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode music home response: %v", err)
	}
	for _, field := range []string{"sections", "discover", "discover_has_more", "discover_meta"} {
		if _, exists := resp.Data[field]; exists {
			t.Fatalf("music home should not include %q: %#v", field, resp.Data)
		}
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

func TestRegisterRoutesMusicHomeHidesPrivatePlaylistsFromAnonymousUsers(t *testing.T) {
	service, _, user := newMusicHTTPTestService(t)
	userRouter := newMusicHTTPRouter(service, &user)

	publicPlaylist := createMusicPlaylistViaAPI(t, userRouter, `{"name":"Visible Public Playlist","description":"public","cover_url":"/uploads/visible-public.jpg","is_public":true}`)
	_ = createMusicPlaylistViaAPI(t, userRouter, `{"name":"Hidden Private Playlist","description":"private","cover_url":"/uploads/hidden-private.jpg","is_public":false}`)

	r := newMusicHTTPRouter(service, nil)

	discoverW := httptest.NewRecorder()
	discoverReq := httptest.NewRequest(http.MethodGet, "/api/v1/music/home", nil)
	r.ServeHTTP(discoverW, discoverReq)
	if discoverW.Code != http.StatusOK {
		t.Fatalf("expected discover 200, got %d: %s", discoverW.Code, discoverW.Body.String())
	}

	var discoverResp struct {
		Data struct {
			Discover []struct {
				Type  string `json:"type"`
				Title string `json:"title"`
			} `json:"discover"`
		} `json:"data"`
	}
	if err := json.Unmarshal(discoverW.Body.Bytes(), &discoverResp); err != nil {
		t.Fatalf("decode discover response: %v", err)
	}
	for _, item := range discoverResp.Data.Discover {
		if item.Type == "playlist" && item.Title == "Hidden Private Playlist" {
			t.Fatalf("private playlist should not appear in music home: %#v", discoverResp.Data.Discover)
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

func TestRegisterRoutesMusicHomeUsesHistoryForUnheardAlbumRecommendations(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)

	artist := model.Artist{Name: "Home Artist"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	playedAlbum := model.Album{Title: "Already Heard", CoverURL: "/uploads/played-cover.jpg", Status: "open", EntryStatus: "open"}
	candidateAlbum := model.Album{Title: "Try This Next", CoverURL: "/uploads/candidate-cover.jpg", Status: "open", EntryStatus: "open"}
	if err := db.Create(&playedAlbum).Error; err != nil {
		t.Fatalf("create played album: %v", err)
	}
	if err := db.Create(&candidateAlbum).Error; err != nil {
		t.Fatalf("create candidate album: %v", err)
	}
	for _, album := range []*model.Album{&playedAlbum, &candidateAlbum} {
		if err := db.Model(album).Association("Artists").Append(&artist); err != nil {
			t.Fatalf("link album artist: %v", err)
		}
	}
	playedSong := model.Song{Title: "Played Song", AudioURL: "/audio/played.mp3", AlbumID: &playedAlbum.ID, Status: "open"}
	if err := db.Create(&playedSong).Error; err != nil {
		t.Fatalf("create played song: %v", err)
	}
	candidateSong := model.Song{Title: "Candidate Song", AudioURL: "/audio/candidate.mp3", AlbumID: &candidateAlbum.ID, Status: "open"}
	if err := db.Create(&candidateSong).Error; err != nil {
		t.Fatalf("create candidate song: %v", err)
	}
	if err := db.Model(&playedSong).Association("Artists").Append(&artist); err != nil {
		t.Fatalf("link song artist: %v", err)
	}
	if err := service.RecordSongPlay(&user.ID, playedSong.ID); err != nil {
		t.Fatalf("record play: %v", err)
	}
	if _, err := service.SavePlaybackProgress(user, SavePlaybackProgressRequest{SongID: playedSong.ID, PositionSeconds: 42.5, DurationSeconds: 180}); err != nil {
		t.Fatalf("save playback progress: %v", err)
	}

	router := newMusicHTTPRouter(service, &user)
	response := performMusicJSONRequest(t, router, http.MethodGet, "/api/v1/music/home", "")
	if response.Code != http.StatusOK {
		t.Fatalf("expected music home to return 200, got %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Data struct {
			Personalized      bool `json:"personalized"`
			ContinueListening *struct {
				PositionSeconds float64 `json:"position_seconds"`
				Song            struct {
					ID string `json:"id"`
				} `json:"song"`
			} `json:"continue_listening"`
			RecentlyPlayed []struct {
				Song struct {
					ID string `json:"id"`
				} `json:"song"`
			} `json:"recently_played"`
			ForYou []struct {
				ID     string `json:"id"`
				Reason string `json:"reason"`
			} `json:"for_you"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode music home: %v", err)
	}
	if !body.Data.Personalized {
		t.Fatal("expected home to be personalized after a recorded play")
	}
	if body.Data.ContinueListening == nil || body.Data.ContinueListening.Song.ID != playedSong.ID.String() || body.Data.ContinueListening.PositionSeconds != 42.5 || len(body.Data.RecentlyPlayed) != 0 {
		t.Fatalf("unexpected continue/recent plays: %#v %#v", body.Data.ContinueListening, body.Data.RecentlyPlayed)
	}
	if len(body.Data.ForYou) != 1 || body.Data.ForYou[0].ID != candidateAlbum.ID.String() {
		t.Fatalf("expected only unheard related album, got %#v", body.Data.ForYou)
	}
	if body.Data.ForYou[0].Reason == "" {
		t.Fatalf("expected an item-level recommendation reason, got %#v", body.Data.ForYou[0])
	}
}

func TestSavePlaybackProgressCompletesNearSongEnd(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	song := model.Song{Title: "Almost Finished", AudioURL: "/almost-finished.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	progress, err := service.SavePlaybackProgress(user, SavePlaybackProgressRequest{SongID: song.ID, PositionSeconds: 178, DurationSeconds: 180})
	if err != nil {
		t.Fatalf("save playback progress: %v", err)
	}
	if !progress.Completed || progress.PositionSeconds != 180 {
		t.Fatalf("expected completed progress at song end, got %#v", progress)
	}
	resumable, err := service.GetPlaybackProgress(user)
	if err != nil {
		t.Fatalf("get playback progress: %v", err)
	}
	if resumable != nil {
		t.Fatalf("completed progress should not be resumable: %#v", resumable)
	}
}

func TestSavePlaybackProgressIgnoresStaleDeviceReport(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	song := model.Song{Title: "Stale Progress Song", AudioURL: "/stale.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	newer := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if _, err := service.SavePlaybackProgress(user, SavePlaybackProgressRequest{SongID: song.ID, PositionSeconds: 80, DurationSeconds: 180, ReportedAt: newer}); err != nil {
		t.Fatalf("save newer progress: %v", err)
	}
	stored, err := service.SavePlaybackProgress(user, SavePlaybackProgressRequest{SongID: song.ID, PositionSeconds: 20, DurationSeconds: 180, ReportedAt: newer.Add(-time.Minute)})
	if err != nil {
		t.Fatalf("save stale progress: %v", err)
	}
	if stored.PositionSeconds != 80 || !stored.ReportedAt.Equal(newer) {
		t.Fatalf("stale progress overwrote newer state: %#v", stored)
	}
}

func TestSavePlaybackSessionIgnoresStaleDeviceReport(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	first := model.Song{Title: "Session First", AudioURL: "/session-first.mp3", Status: "open"}
	second := model.Song{Title: "Session Second", AudioURL: "/session-second.mp3", Status: "open"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first song: %v", err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("create second song: %v", err)
	}
	newer := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if _, err := service.SavePlaybackSession(user, SavePlaybackSessionRequest{SongIDs: []uuid.UUID{first.ID, second.ID}, CurrentSongID: second.ID, PositionSeconds: 55, PlaybackMode: "random", ReportedAt: newer}); err != nil {
		t.Fatalf("save newer session: %v", err)
	}
	stored, err := service.SavePlaybackSession(user, SavePlaybackSessionRequest{SongIDs: []uuid.UUID{second.ID, first.ID}, CurrentSongID: first.ID, PositionSeconds: 5, PlaybackMode: "loop", ReportedAt: newer.Add(-time.Minute)})
	if err != nil {
		t.Fatalf("save stale session: %v", err)
	}
	if stored.CurrentSongID != second.ID || stored.PositionSeconds != 55 || stored.PlaybackMode != "random" || len(stored.Queue) != 2 || stored.Queue[0].ID != first.ID {
		t.Fatalf("stale session overwrote newer state: %#v", stored)
	}
}

func TestGetPlaybackSessionDropsUnavailableSongs(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	first := model.Song{Title: "Available Session Song", AudioURL: "/available-session.mp3", Status: "open"}
	retired := model.Song{Title: "Retired Session Song", AudioURL: "/retired-session.mp3", Status: "open"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first song: %v", err)
	}
	if err := db.Create(&retired).Error; err != nil {
		t.Fatalf("create retired song: %v", err)
	}
	if _, err := service.SavePlaybackSession(user, SavePlaybackSessionRequest{
		SongIDs: []uuid.UUID{first.ID, retired.ID}, CurrentSongID: first.ID, PositionSeconds: 12, PlaybackMode: "loop",
	}); err != nil {
		t.Fatalf("save playback session: %v", err)
	}
	if err := db.Model(&retired).Update("lifecycle_status", model.MusicLifecycleRetired).Error; err != nil {
		t.Fatalf("retire song: %v", err)
	}

	session, err := service.GetPlaybackSession(user)
	if err != nil {
		t.Fatalf("get playback session: %v", err)
	}
	if session == nil || session.CurrentSongID != first.ID || len(session.Queue) != 1 || session.Queue[0].ID != first.ID {
		t.Fatalf("expected available session queue, got %#v", session)
	}
}

func TestGetPlaybackProgressIgnoresUnavailableSong(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	song := model.Song{Title: "Retired Progress Song", AudioURL: "/retired-progress.mp3", Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	if _, err := service.SavePlaybackProgress(user, SavePlaybackProgressRequest{SongID: song.ID, PositionSeconds: 12, DurationSeconds: 180}); err != nil {
		t.Fatalf("save playback progress: %v", err)
	}
	if err := db.Model(&song).Update("lifecycle_status", model.MusicLifecycleRetired).Error; err != nil {
		t.Fatalf("retire song: %v", err)
	}

	progress, err := service.GetPlaybackProgress(user)
	if err != nil {
		t.Fatalf("get playback progress: %v", err)
	}
	if progress != nil {
		t.Fatalf("expected no progress for unavailable song, got %#v", progress)
	}
}

func TestRegisterRoutesMusicCursorPagination(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	older := model.Album{Title: "Older", EntryStatus: "open", Status: "open"}
	newer := model.Album{Title: "Newer", EntryStatus: "open", Status: "open"}
	if err := db.Create(&older).Error; err != nil {
		t.Fatalf("create older album: %v", err)
	}
	if err := db.Create(&newer).Error; err != nil {
		t.Fatalf("create newer album: %v", err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	if err := db.Model(&older).Update("created_at", base).Error; err != nil {
		t.Fatalf("set older album timestamp: %v", err)
	}
	if err := db.Model(&newer).Update("created_at", base.Add(time.Minute)).Error; err != nil {
		t.Fatalf("set newer album timestamp: %v", err)
	}
	olderBookmark := model.AlbumBookmark{UserID: user.ID, AlbumID: older.ID}
	newerBookmark := model.AlbumBookmark{UserID: user.ID, AlbumID: newer.ID}
	if err := db.Create(&olderBookmark).Error; err != nil {
		t.Fatalf("create older bookmark: %v", err)
	}
	if err := db.Create(&newerBookmark).Error; err != nil {
		t.Fatalf("create newer bookmark: %v", err)
	}
	if err := db.Model(&olderBookmark).Update("created_at", base).Error; err != nil {
		t.Fatalf("set older bookmark timestamp: %v", err)
	}
	if err := db.Model(&newerBookmark).Update("created_at", base.Add(time.Minute)).Error; err != nil {
		t.Fatalf("set newer bookmark timestamp: %v", err)
	}
	r := newMusicHTTPRouter(service, &user)

	testCursorPagination := func(path, expectedFirst, expectedSecond string) {
		t.Helper()
		first := httptest.NewRecorder()
		r.ServeHTTP(first, httptest.NewRequest(http.MethodGet, path, nil))
		if first.Code != http.StatusOK {
			t.Fatalf("first cursor request: %d: %s", first.Code, first.Body.String())
		}
		var firstPage struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			Meta struct {
				NextCursor string `json:"next_cursor"`
				Total      *int64 `json:"total"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(first.Body.Bytes(), &firstPage); err != nil {
			t.Fatalf("decode first cursor page: %v", err)
		}
		if len(firstPage.Data) != 1 || firstPage.Data[0].ID != expectedFirst || firstPage.Meta.NextCursor == "" || firstPage.Meta.Total != nil {
			t.Fatalf("unexpected first cursor page: %#v", firstPage)
		}
		nextPath := strings.TrimSuffix(path, "cursor=") + "cursor=" + firstPage.Meta.NextCursor
		second := httptest.NewRecorder()
		r.ServeHTTP(second, httptest.NewRequest(http.MethodGet, nextPath, nil))
		if second.Code != http.StatusOK {
			t.Fatalf("second cursor request: %d: %s", second.Code, second.Body.String())
		}
		var secondPage struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			Meta struct {
				NextCursor string `json:"next_cursor"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(second.Body.Bytes(), &secondPage); err != nil {
			t.Fatalf("decode second cursor page: %v", err)
		}
		if len(secondPage.Data) != 1 || secondPage.Data[0].ID != expectedSecond || secondPage.Meta.NextCursor != "" {
			t.Fatalf("unexpected second cursor page: %#v", secondPage)
		}
	}

	testCursorPagination("/api/v1/music/albums?sort=-created_at&page_size=1&cursor=", newer.ID.String(), older.ID.String())
	testCursorPagination("/api/v1/music/bookmarks/albums?sort=latest&page_size=1&cursor=", newerBookmark.ID.String(), olderBookmark.ID.String())

	unsupportedSort := httptest.NewRecorder()
	validCursor := encodeMusicCreatedAtCursor(newer.CreatedAt, newer.ID)
	r.ServeHTTP(unsupportedSort, httptest.NewRequest(http.MethodGet, "/api/v1/music/albums?sort=hot&cursor="+validCursor, nil))
	if unsupportedSort.Code != http.StatusBadRequest {
		t.Fatalf("expected unsupported cursor sort to return 400, got %d: %s", unsupportedSort.Code, unsupportedSort.Body.String())
	}
	invalidCursor := httptest.NewRecorder()
	r.ServeHTTP(invalidCursor, httptest.NewRequest(http.MethodGet, "/api/v1/music/albums?sort=-created_at&cursor=invalid", nil))
	if invalidCursor.Code != http.StatusBadRequest {
		t.Fatalf("expected malformed cursor to return 400, got %d: %s", invalidCursor.Code, invalidCursor.Body.String())
	}
}
