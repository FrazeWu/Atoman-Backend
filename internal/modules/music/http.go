package music

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/recommendation"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func resolveMusicMediaURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return trimmed
	}
	if strings.HasPrefix(trimmed, "/uploads/") {
		base := strings.TrimRight(os.Getenv("PUBLIC_UPLOADS_BASE_URL"), "/")
		if base == "" {
			return trimmed
		}
		if strings.HasSuffix(base, "/uploads") {
			return base + strings.TrimPrefix(trimmed, "/uploads")
		}
		return base + trimmed
	}
	if strings.HasPrefix(trimmed, "uploads/") {
		base := strings.TrimRight(os.Getenv("PUBLIC_UPLOADS_BASE_URL"), "/")
		if base == "" {
			return "/" + trimmed
		}
		if strings.HasSuffix(base, "/uploads") {
			return base + "/" + strings.TrimPrefix(trimmed, "uploads/")
		}
		return base + "/" + strings.TrimLeft(trimmed, "/")
	}
	if os.Getenv("STORAGE_TYPE") == "s3" {
		s3Prefix := strings.TrimRight(os.Getenv("S3_URL_PREFIX"), "/")
		if s3Prefix != "" {
			return s3Prefix + "/" + strings.TrimLeft(trimmed, "/")
		}
	}
	return trimmed
}

func resolveAlbumMediaURLs(album *model.Album) {
	album.CoverURL = resolveMusicMediaURL(album.CoverURL)
	for i := range album.Songs {
		album.Songs[i].AudioURL = resolveMusicMediaURL(album.Songs[i].AudioURL)
		album.Songs[i].CoverURL = resolveMusicMediaURL(album.Songs[i].CoverURL)
	}
}

func hydrateAlbumStats(db *gorm.DB, albums []model.Album) error {
	if len(albums) == 0 {
		return nil
	}

	albumIDs := make([]uuid.UUID, 0, len(albums))
	albumIndex := make(map[uuid.UUID]int, len(albums))
	for i := range albums {
		albumIDs = append(albumIDs, albums[i].ID)
		albumIndex[albums[i].ID] = i
	}

	var bookmarkRows []struct {
		AlbumID uuid.UUID
		Count   int64
	}
	if err := db.Model(&model.AlbumBookmark{}).
		Select("album_id, COUNT(*) AS count").
		Where("album_id IN ?", albumIDs).
		Group("album_id").
		Scan(&bookmarkRows).Error; err != nil {
		return err
	}

	for _, row := range bookmarkRows {
		if idx, ok := albumIndex[row.AlbumID]; ok {
			albums[idx].BookmarkCount = row.Count
		}
	}

	for i := range albums {
		var playCount int64
		for _, song := range albums[i].Songs {
			playCount += song.PlayCount
		}
		albums[i].PlayCount = playCount
	}

	return nil
}

func hydrateArtistStats(db *gorm.DB, artists []model.Artist) error {
	if len(artists) == 0 {
		return nil
	}

	artistIDs := make([]uuid.UUID, 0, len(artists))
	artistIndex := make(map[uuid.UUID]int, len(artists))
	for i := range artists {
		artistIDs = append(artistIDs, artists[i].ID)
		artistIndex[artists[i].ID] = i
	}

	var bookmarkRows []struct {
		ArtistID uuid.UUID
		Count    int64
	}
	if err := db.Model(&model.ArtistBookmark{}).
		Select("artist_id, COUNT(*) AS count").
		Where("artist_id IN ?", artistIDs).
		Group("artist_id").
		Scan(&bookmarkRows).Error; err != nil {
		return err
	}
	for _, row := range bookmarkRows {
		if idx, ok := artistIndex[row.ArtistID]; ok {
			artists[idx].BookmarkCount = row.Count
		}
	}

	var playRows []struct {
		ArtistID  uuid.UUID
		PlayCount int64
	}
	if err := db.Table("song_artists").
		Select("song_artists.artist_id AS artist_id, COALESCE(SUM(\"Songs\".play_count), 0) AS play_count").
		Joins("JOIN \"Songs\" ON \"Songs\".id = song_artists.song_id").
		Where("song_artists.artist_id IN ?", artistIDs).
		Group("song_artists.artist_id").
		Scan(&playRows).Error; err != nil {
		return err
	}
	for _, row := range playRows {
		if idx, ok := artistIndex[row.ArtistID]; ok {
			artists[idx].PlayCount = row.PlayCount
		}
	}

	return nil
}

type Handler struct {
	service *Service
}

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service}
	group.POST("/imports/albums", h.createAlbumImportSession)
	group.POST("/imports/albums/:sessionId/upload", h.uploadAlbumImportArchive)
	group.POST("/imports/albums/:sessionId/multipart", h.startAlbumImportMultipart)
	group.POST("/imports/albums/:sessionId/multipart/parts/:partNumber", h.createAlbumImportMultipartPartUpload)
	group.POST("/imports/albums/:sessionId/multipart/parts/:partNumber/complete", h.completeAlbumImportMultipartPart)
	group.POST("/imports/albums/:sessionId/multipart/complete", h.completeAlbumImportMultipart)
	group.GET("/imports/albums/:sessionId", h.getAlbumImportSession)
	group.POST("/imports/albums/:sessionId/commit", h.commitAlbumImportSession)
	group.GET("/artists", h.listArtists)
	group.POST("/artists", h.createArtist)
	group.GET("/artists/:artistId", h.getArtist)
	group.PATCH("/artists/:artistId", h.updateArtist)
	group.GET("/albums", h.listAlbums)
	group.GET("/albums/:albumId", h.getAlbum)
	group.GET("/bookmarks/artists", h.listArtistBookmarks)
	group.POST("/bookmarks/artists", h.createArtistBookmark)
	group.DELETE("/bookmarks/artists/:artistId", h.deleteArtistBookmark)
	group.GET("/bookmarks/albums", h.listAlbumBookmarks)
	group.POST("/bookmarks/albums", h.createAlbumBookmark)
	group.DELETE("/bookmarks/albums/:albumId", h.deleteAlbumBookmark)
	group.GET("/bookmarks/songs", h.listSongBookmarks)
	group.POST("/bookmarks/songs", h.createSongBookmark)
	group.DELETE("/bookmarks/songs/:songId", h.deleteSongBookmark)
	group.GET("/discover", h.discover)
	group.GET("/playlists", h.listPlaylists)
	group.GET("/playlists/public", h.listPublicPlaylists)
	group.POST("/playlists", h.createPlaylist)
	group.GET("/playlists/:id", h.getPlaylist)
	group.PATCH("/playlists/:id", h.updatePlaylist)
	group.DELETE("/playlists/:id", h.deletePlaylist)
	group.GET("/playlists/:id/songs", h.listPlaylistSongs)
	group.POST("/playlists/:id/songs", h.addPlaylistSong)
	group.PUT("/playlists/:id/songs/order", h.reorderPlaylistSongs)
	group.DELETE("/playlists/:id/songs/:songId", h.deletePlaylistSong)
	group.POST("/plays", h.recordSongPlay)
	group.GET("/history", h.listListeningHistory)
	group.GET("/recommend/albums", h.getRecommendedAlbums)
	group.GET("/recommend/artists", h.getRecommendedArtists)
	group.POST("/edits", h.submitEdit)
	group.GET("/edits", h.listEdits)
	group.GET("/edits/:editId", h.getEdit)
	group.POST("/edits/:editId/votes", h.voteEdit)
	group.POST("/edits/:editId/approve", h.approveEdit)
	group.POST("/edits/:editId/reject", h.rejectEdit)
	group.POST("/edits/:editId/cancel", h.cancelEdit)
}

func (h *Handler) listArtists(c *gin.Context) {
	page, pageSize := httpx.PageParams(c)
	query := strings.TrimSpace(c.Query("q"))

	db := h.service.db.Model(&model.Artist{}).Distinct("\"Artists\".*").Where("COALESCE(\"Artists\".entry_status, '') <> ?", "closed")
	if query != "" {
		like := "%" + strings.ToLower(query) + "%"
		db = db.
			Joins("LEFT JOIN artist_aliases ON artist_aliases.artist_id = \"Artists\".id").
			Where("LOWER(\"Artists\".name) LIKE ? OR LOWER(COALESCE(\"Artists\".legal_name, '')) LIKE ? OR LOWER(COALESCE(artist_aliases.alias, '')) LIKE ?", like, like, like)
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		httpx.Error(c, err)
		return
	}

	var artists []model.Artist
	if err := db.Preload("Aliases").Order("name ASC").Limit(pageSize).Offset(httpx.Offset(page, pageSize)).Find(&artists).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	if err := hydrateArtistStats(h.service.db, artists); err != nil {
		httpx.Error(c, err)
		return
	}

	for i := range artists {
		artists[i].ImageURL = resolveMusicMediaURL(artists[i].ImageURL)
	}

	httpx.List(c, artists, page, pageSize, total)
}

func (h *Handler) createArtist(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req CreateArtistRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	artist, err := h.service.CreateArtist(user, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	artist.ImageURL = resolveMusicMediaURL(artist.ImageURL)
	httpx.OK(c, http.StatusCreated, artist)
}

func (h *Handler) getArtist(c *gin.Context) {
	artistID, err := parseMusicID(c.Param("artistId"), "artistId")
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var artist model.Artist
	query := h.service.db.Preload("Aliases").Preload("Albums.Artists")
	if h.service.db.Migrator().HasTable(&model.ArtistMember{}) {
		query = query.Preload("MemberRelations.MemberArtist")
	}
	if err := query.First(&artist, "id = ?", artistID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("music.artist_not_found", "Artist not found"))
			return
		}
		httpx.Error(c, err)
		return
	}
	artistRows := []model.Artist{artist}
	if err := hydrateArtistStats(h.service.db, artistRows); err != nil {
		httpx.Error(c, err)
		return
	}
	artist = artistRows[0]

	artist.ImageURL = resolveMusicMediaURL(artist.ImageURL)
	for i := range artist.Albums {
		artist.Albums[i].CoverURL = resolveMusicMediaURL(artist.Albums[i].CoverURL)
		for j := range artist.Albums[i].Artists {
			artist.Albums[i].Artists[j].ImageURL = resolveMusicMediaURL(artist.Albums[i].Artists[j].ImageURL)
		}
	}
	for i := range artist.MemberRelations {
		if artist.MemberRelations[i].MemberArtist != nil {
			artist.MemberRelations[i].MemberArtist.ImageURL = resolveMusicMediaURL(artist.MemberRelations[i].MemberArtist.ImageURL)
		}
	}

	httpx.OK(c, http.StatusOK, buildArtistDetailResponse(artist))
}

func (h *Handler) updateArtist(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	artistID, err := parseMusicID(c.Param("artistId"), "artistId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var req UpdateArtistRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	artist, err := h.service.UpdateArtist(user, artistID, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	artist.ImageURL = resolveMusicMediaURL(artist.ImageURL)
	httpx.OK(c, http.StatusOK, artist)
}

func buildArtistDetailResponse(artist model.Artist) ArtistDetailResponse {
	now := time.Now()
	resp := ArtistDetailResponse{
		ID:             artist.ID,
		Name:           artist.Name,
		LegalName:      artist.LegalName,
		StageNamesJSON: artist.StageNamesJSON,
		Bio:            artist.Bio,
		ImageURL:       artist.ImageURL,
		Nationality:    artist.Nationality,
		BirthPlace:     artist.BirthPlace,
		BirthDate:      artist.BirthDate,
		BirthYear:      artist.BirthYear,
		DeathYear:      artist.DeathYear,
		ArtistForm:     artist.ArtistForm,
		Members:        artist.Members,
		EntryStatus:    artist.EntryStatus,
		RedirectTo:     artist.RedirectTo,
		Albums:         artist.Albums,
		Aliases:        artist.Aliases,
		PlayCount:      artist.PlayCount,
		BookmarkCount:  artist.BookmarkCount,
		MemberGroups: ArtistMemberGroupsResponse{
			Current: []ArtistMemberGroupItemResponse{},
			Former:  []ArtistMemberGroupItemResponse{},
		},
	}
	if !artist.ActiveStartDate.IsZero() {
		resp.ActiveStartDate = artist.ActiveStartDate.Format("2006-01-02")
	}
	if !artist.ActiveEndDate.IsZero() {
		resp.ActiveEndDate = artist.ActiveEndDate.Format("2006-01-02")
	}
	for _, relation := range artist.MemberRelations {
		if relation.MemberArtist == nil {
			continue
		}
		item := ArtistMemberGroupItemResponse{
			ArtistID: relation.MemberArtist.ID,
			Name:     relation.MemberArtist.Name,
			ImageURL: relation.MemberArtist.ImageURL,
		}
		if relation.JoinDate != nil {
			item.JoinDate = relation.JoinDate.Format("2006-01-02")
		}
		if relation.JoinDate != nil && relation.JoinDate.After(now) {
			continue
		}
		if relation.LeaveDate != nil && !relation.LeaveDate.After(now) {
			item.LeaveDate = relation.LeaveDate.Format("2006-01-02")
			resp.MemberGroups.Former = append(resp.MemberGroups.Former, item)
			continue
		}
		if relation.LeaveDate != nil {
			item.LeaveDate = relation.LeaveDate.Format("2006-01-02")
		}
		resp.MemberGroups.Current = append(resp.MemberGroups.Current, item)
	}
	return resp
}

func (h *Handler) listAlbums(c *gin.Context) {
	page, pageSize := httpx.PageParams(c)
	query := strings.TrimSpace(c.Query("q"))
	artistIDRaw := strings.TrimSpace(c.Query("artist_id"))
	sort := strings.TrimSpace(c.Query("sort"))

	db := h.service.db.Model(&model.Album{}).Where("COALESCE(\"Albums\".entry_status, '') <> ? AND COALESCE(\"Albums\".status, '') <> ?", "closed", "closed")
	joinedArtists := false
	if query != "" {
		like := "%" + strings.ToLower(query) + "%"
		db = db.
			Joins("LEFT JOIN album_artists AS search_album_artists ON search_album_artists.album_id = \"Albums\".id").
			Joins("LEFT JOIN \"Artists\" AS search_artists ON search_artists.id = search_album_artists.artist_id")
		joinedArtists = true
		db = db.Where("LOWER(\"Albums\".title) LIKE ? OR LOWER(search_artists.name) LIKE ?", like, like)
	}
	if artistIDRaw != "" {
		artistID, err := parseMusicID(artistIDRaw, "artist_id")
		if err != nil {
			httpx.Error(c, err)
			return
		}
		db = db.Joins("JOIN album_artists AS filter_album_artists ON filter_album_artists.album_id = \"Albums\".id").Where("filter_album_artists.artist_id = ?", artistID)
		joinedArtists = true
	}

	var total int64
	countDB := db
	if joinedArtists {
		countDB = countDB.Distinct("\"Albums\".id")
	}
	if err := countDB.Count(&total).Error; err != nil {
		httpx.Error(c, err)
		return
	}

	var albums []model.Album
	findDB := db.Preload("Artists").Preload("Songs")
	if joinedArtists {
		findDB = findDB.Distinct("\"Albums\".*")
	}
	for _, order := range albumSortOrders(sort) {
		findDB = findDB.Order(order)
	}
	if err := findDB.Limit(pageSize).Offset(httpx.Offset(page, pageSize)).Find(&albums).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	if err := hydrateAlbumStats(h.service.db, albums); err != nil {
		httpx.Error(c, err)
		return
	}
	for i := range albums {
		resolveAlbumMediaURLs(&albums[i])
	}

	httpx.List(c, albums, page, pageSize, total)
}

func (h *Handler) getAlbum(c *gin.Context) {
	albumID, err := parseMusicID(c.Param("albumId"), "albumId")
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var album model.Album
	if err := h.service.db.Preload("Artists").Preload("Songs").First(&album, "id = ?", albumID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("music.album_not_found", "Album not found"))
			return
		}
		httpx.Error(c, err)
		return
	}
	albumRows := []model.Album{album}
	if err := hydrateAlbumStats(h.service.db, albumRows); err != nil {
		httpx.Error(c, err)
		return
	}
	album = albumRows[0]
	resolveAlbumMediaURLs(&album)
	httpx.OK(c, http.StatusOK, album)
}

// recordSongPlay godoc
// @Summary 记录有效播放
// @Description 播放器在实际播放满 5 秒后调用。匿名用户增加总播放次数，登录用户同时更新个人播放历史。
// @Tags music
// @Accept json
// @Produce json
// @Param input body RecordSongPlayRequest true "播放记录"
// @Success 200 {object} map[string]bool
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Router /api/v1/music/plays [post]
func (h *Handler) recordSongPlay(c *gin.Context) {
	var req RecordSongPlayRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	var userID *uuid.UUID
	if user, ok := authctx.Current(c); ok {
		userID = &user.ID
	}
	if err := h.service.RecordSongPlay(userID, req.SongID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"recorded": true})
}

// listListeningHistory godoc
// @Summary 获取最近播放
// @Description 返回当前用户最近播放的歌曲，每首歌曲保留最近时间和累计播放次数。
// @Tags music
// @Produce json
// @Success 200 {object} ListeningHistoryListResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/music/history [get]
func (h *Handler) listListeningHistory(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	page, pageSize := httpx.PageParams(c)
	rows, total, err := h.service.ListListeningHistory(user, page, pageSize)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	for index := range rows {
		if rows[index].Song != nil {
			rows[index].Song.AudioURL = resolveMusicMediaURL(rows[index].Song.AudioURL)
			rows[index].Song.CoverURL = resolveMusicMediaURL(rows[index].Song.CoverURL)
			if rows[index].Song.Album != nil {
				rows[index].Song.Album.CoverURL = resolveMusicMediaURL(rows[index].Song.Album.CoverURL)
			}
		}
	}
	httpx.List(c, rows, page, pageSize, total)
}

func (h *Handler) getRecommendedAlbums(c *gin.Context) {
	mode, err := parseMusicRecommendationMode(c.Query("mode"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	page, pageSize := httpx.PageParams(c)
	items, total, err := h.service.RecommendAlbumsByMode(mode, page, pageSize)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	for i := range items {
		items[i].ImageURL = resolveMusicMediaURL(items[i].ImageURL)
	}
	httpx.List(c, items, page, pageSize, total)
}

func (h *Handler) getRecommendedArtists(c *gin.Context) {
	mode, err := parseMusicRecommendationMode(c.Query("mode"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	page, pageSize := httpx.PageParams(c)
	items, total, err := h.service.RecommendArtistsByMode(mode, page, pageSize)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	for i := range items {
		items[i].ImageURL = resolveMusicMediaURL(items[i].ImageURL)
	}
	httpx.List(c, items, page, pageSize, total)
}

func currentMusicUser(c *gin.Context) (authctx.CurrentUser, bool) {
	user, ok := authctx.Current(c)
	if !ok {
		return authctx.CurrentUser{}, false
	}
	return user, true
}

func (h *Handler) listArtistBookmarks(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	page, pageSize := httpx.PageParams(c)
	sort := c.DefaultQuery("sort", "latest")
	bookmarks, total, err := h.service.ListArtistBookmarks(user, page, pageSize, sort)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, bookmarks, page, pageSize, total)
}

func (h *Handler) createArtistBookmark(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req CreateArtistBookmarkRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	bookmark, err := h.service.BookmarkArtist(user, req.ArtistID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, bookmark)
}

func (h *Handler) deleteArtistBookmark(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	artistID, err := parseMusicID(c.Param("artistId"), "artistId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.DeleteArtistBookmark(user, artistID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}

func (h *Handler) listAlbumBookmarks(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	page, pageSize := httpx.PageParams(c)
	sort := c.DefaultQuery("sort", "latest")
	bookmarks, total, err := h.service.ListAlbumBookmarks(user, page, pageSize, sort)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, bookmarks, page, pageSize, total)
}

func (h *Handler) createAlbumBookmark(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req CreateAlbumBookmarkRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	bookmark, err := h.service.BookmarkAlbum(user, req.AlbumID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, bookmark)
}

func (h *Handler) deleteAlbumBookmark(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	albumID, err := parseMusicID(c.Param("albumId"), "albumId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.DeleteAlbumBookmark(user, albumID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}

func (h *Handler) listSongBookmarks(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	page, pageSize := httpx.PageParams(c)
	sort := c.DefaultQuery("sort", "latest")
	bookmarks, total, err := h.service.ListSongBookmarks(user, page, pageSize, sort)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, bookmarks, page, pageSize, total)
}

func (h *Handler) createSongBookmark(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req CreateSongBookmarkRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	bookmark, err := h.service.BookmarkSong(user, req.SongID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, bookmark)
}

func (h *Handler) deleteSongBookmark(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	songID, err := parseMusicID(c.Param("songId"), "songId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.DeleteSongBookmark(user, songID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}

func (h *Handler) listPlaylists(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	page, pageSize := httpx.PageParams(c)
	sort := c.DefaultQuery("sort", "latest")
	playlists, total, err := h.service.ListPlaylists(user, page, pageSize, sort)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	playlistIDs := make([]uuid.UUID, 0, len(playlists))
	for _, playlist := range playlists {
		playlistIDs = append(playlistIDs, playlist.ID)
	}
	songCounts, err := h.service.repo.CountPlaylistSongs(playlistIDs)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	rows := make([]PlaylistSummaryResponse, 0, len(playlists))
	for _, playlist := range playlists {
		rows = append(rows, PlaylistSummaryResponse{
			ID:          playlist.ID,
			UserID:      playlist.UserID,
			Name:        playlist.Name,
			Description: playlist.Description,
			CoverURL:    resolveMusicMediaURL(playlist.CoverURL),
			IsPublic:    playlist.IsPublic,
			IsFavorite:  playlist.IsFavorite,
			SongCount:   songCounts[playlist.ID],
		})
	}
	httpx.List(c, rows, page, pageSize, total)
}

// listPublicPlaylists godoc
// @Summary 获取公开歌单列表
// @Description 返回可被发现的公开歌单，匿名用户可访问。
// @Tags music-playlists
// @Produce json
// @Success 200 {object} PlaylistSummaryListResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Router /api/v1/music/playlists/public [get]
func (h *Handler) listPublicPlaylists(c *gin.Context) {
	page, pageSize := httpx.PageParams(c)
	playlists, songCounts, total, err := h.service.ListPublicPlaylists(page, pageSize)
	if err != nil {
		httpx.Error(c, err)
		return
	}

	rows := make([]PlaylistSummaryResponse, 0, len(playlists))
	for _, playlist := range playlists {
		rows = append(rows, PlaylistSummaryResponse{
			ID:          playlist.ID,
			UserID:      playlist.UserID,
			Name:        playlist.Name,
			Description: playlist.Description,
			CoverURL:    resolveMusicMediaURL(playlist.CoverURL),
			IsPublic:    playlist.IsPublic,
			IsFavorite:  playlist.IsFavorite,
			SongCount:   songCounts[playlist.ID],
		})
	}
	httpx.List(c, rows, page, pageSize, total)
}

// discover godoc
// @Summary 获取音乐发现流
// @Description 返回混合发现流，按专辑、艺人、公开歌单的简单规则混排。
// @Tags music-discovery
// @Produce json
// @Success 200 {object} DiscoverListResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Router /api/v1/music/discover [get]
func (h *Handler) discover(c *gin.Context) {
	page, pageSize := httpx.PageParams(c)
	items, total, err := h.service.Discover(page, pageSize)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	for i := range items {
		items[i].ImageURL = resolveMusicMediaURL(items[i].ImageURL)
	}
	httpx.List(c, items, page, pageSize, total)
}

func buildPlaylistSummaryResponse(playlist model.Playlist, songCount int64) PlaylistSummaryResponse {
	return PlaylistSummaryResponse{
		ID:          playlist.ID,
		UserID:      playlist.UserID,
		Name:        playlist.Name,
		Description: playlist.Description,
		CoverURL:    resolveMusicMediaURL(playlist.CoverURL),
		IsPublic:    playlist.IsPublic,
		IsFavorite:  playlist.IsFavorite,
		SongCount:   songCount,
	}
}

// createPlaylist godoc
// @Summary 创建歌单
// @Description 创建歌单，可同时设置简介、封面和是否公开。
// @Tags music-playlists
// @Accept json
// @Produce json
// @Param input body CreatePlaylistRequest true "歌单创建请求"
// @Success 201 {object} PlaylistSummaryResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/music/playlists [post]
func (h *Handler) createPlaylist(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req CreatePlaylistRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	playlist, err := h.service.CreatePlaylist(user, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, buildPlaylistSummaryResponse(playlist, 0))
}

func (h *Handler) deletePlaylist(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	playlistID, err := parseMusicID(c.Param("id"), "id")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.DeletePlaylist(user, playlistID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}

// getPlaylist godoc
// @Summary 获取歌单详情
// @Description 返回歌单详情。公开歌单支持匿名访问，私有歌单仅所有者可访问。
// @Tags music-playlists
// @Produce json
// @Param id path string true "歌单 ID"
// @Success 200 {object} PlaylistSummaryResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Router /api/v1/music/playlists/{id} [get]
func (h *Handler) getPlaylist(c *gin.Context) {
	user, _ := currentMusicUser(c)
	playlistID, err := parseMusicID(c.Param("id"), "id")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	playlist, err := h.service.GetPlaylist(user, playlistID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, buildPlaylistSummaryResponse(playlist, 0))
}

func (h *Handler) updatePlaylist(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	playlistID, err := parseMusicID(c.Param("id"), "id")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var req UpdatePlaylistRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	playlist, err := h.service.UpdatePlaylist(user, playlistID, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, buildPlaylistSummaryResponse(playlist, 0))
}

// listPlaylistSongs godoc
// @Summary 获取歌单歌曲列表
// @Description 返回歌单中的歌曲列表。公开歌单支持匿名访问，私有歌单仅所有者可访问。
// @Tags music-playlists
// @Produce json
// @Param id path string true "歌单 ID"
// @Success 200 {object} PlaylistSongsListResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Router /api/v1/music/playlists/{id}/songs [get]
func (h *Handler) listPlaylistSongs(c *gin.Context) {
	user, _ := currentMusicUser(c)
	playlistID, err := parseMusicID(c.Param("id"), "id")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	page, pageSize := httpx.PageParams(c)
	songs, total, err := h.service.ListPlaylistSongs(user, playlistID, page, pageSize)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, songs, page, pageSize, total)
}

func (h *Handler) addPlaylistSong(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	playlistID, err := parseMusicID(c.Param("id"), "id")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var req AddPlaylistSongRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	playlistSong, err := h.service.AddPlaylistSong(user, playlistID, req.SongID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, playlistSong)
}

// reorderPlaylistSongs godoc
// @Summary 调整歌单歌曲顺序
// @Description 使用完整歌曲 ID 列表更新歌单顺序，支持普通歌单和最爱歌单。
// @Tags music-playlists
// @Accept json
// @Produce json
// @Param id path string true "歌单 ID"
// @Param input body ReorderPlaylistSongsRequest true "完整歌曲顺序"
// @Success 200 {object} map[string]bool
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/music/playlists/{id}/songs/order [put]
func (h *Handler) reorderPlaylistSongs(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	playlistID, err := parseMusicID(c.Param("id"), "id")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var req ReorderPlaylistSongsRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.ReorderPlaylistSongs(user, playlistID, req.SongIDs); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"reordered": true})
}

func (h *Handler) deletePlaylistSong(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	playlistID, err := parseMusicID(c.Param("id"), "id")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	songID, err := parseMusicID(c.Param("songId"), "songId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.DeletePlaylistSong(user, playlistID, songID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}

func (h *Handler) listEdits(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	if !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) {
		httpx.Error(c, apperr.Forbidden("music.edit_forbidden", "Moderator role required"))
		return
	}

	page, pageSize := httpx.PageParams(c)
	edits, total, err := h.service.repo.ListEdits(ListEditsQuery{
		Status:     c.Query("status"),
		EntityType: c.Query("entity_type"),
		Type:       c.Query("type"),
		Page:       page,
		PageSize:   pageSize,
	})
	if err != nil {
		httpx.Error(c, err)
		return
	}

	httpx.List(c, edits, page, pageSize, total)
}

func albumSortOrders(sort string) []string {
	switch sort {
	case "hot":
		return []string{"\"Albums\".hot_score DESC", "\"Albums\".updated_at DESC", "\"Albums\".title ASC"}
	case "random":
		return []string{"RANDOM()"}
	case "-created_at":
		return []string{"\"Albums\".created_at DESC", "\"Albums\".title ASC"}
	case "release_date":
		return []string{"\"Albums\".release_date ASC", "\"Albums\".title ASC"}
	default:
		return []string{"\"Albums\".release_date ASC", "\"Albums\".title ASC"}
	}
}

func parseMusicRecommendationMode(raw string) (recommendation.Mode, error) {
	switch recommendation.Mode(strings.TrimSpace(strings.ToLower(raw))) {
	case recommendation.ModeHot:
		return recommendation.ModeHot, nil
	case recommendation.ModeFeatured:
		return recommendation.ModeFeatured, nil
	case recommendation.ModeDiscover:
		return recommendation.ModeDiscover, nil
	default:
		return "", apperr.BadRequest("validation.invalid_request", "mode must be one of hot, featured, discover")
	}
}

func (h *Handler) submitEdit(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	var req SubmitEditRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}

	edit, err := h.service.SubmitEdit(user, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, edit)
}

func (h *Handler) getEdit(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	editID, err := parseEditID(c.Param("editId"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	edit, err := h.service.repo.GetEdit(editID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("music.edit_not_found", "Edit not found"))
			return
		}
		httpx.Error(c, err)
		return
	}
	if edit.SubmittedBy != user.ID && !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) {
		httpx.Error(c, apperr.Forbidden("music.edit_forbidden", "You cannot view this edit"))
		return
	}
	httpx.OK(c, http.StatusOK, edit)
}

func (h *Handler) voteEdit(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	editID, err := parseEditID(c.Param("editId"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var req VoteRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.Vote(user, editID, req); err != nil {
		httpx.Error(c, err)
		return
	}

	edit, err := h.service.repo.GetEdit(editID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, edit)
}

func (h *Handler) approveEdit(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	editID, err := parseEditID(c.Param("editId"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var req DecisionRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	edit, err := h.service.ApproveEdit(user, editID, req.Reason)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, edit)
}

func (h *Handler) rejectEdit(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	editID, err := parseEditID(c.Param("editId"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var req DecisionRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	edit, err := h.service.RejectEdit(user, editID, req.Reason)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, edit)
}

func (h *Handler) cancelEdit(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	editID, err := parseEditID(c.Param("editId"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var req DecisionRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	edit, err := h.service.CancelEdit(user, editID, req.Reason)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, edit)
}

func parseEditID(raw string) (uuid.UUID, error) {
	return parseMusicID(raw, "editId")
}

func parseMusicID(raw string, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, apperr.BadRequest("validation.invalid_request", field+" must be a valid UUID")
	}
	return id, nil
}

func bindJSON(c *gin.Context, dst any) error {
	if err := c.ShouldBindJSON(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return apperr.BadRequest("validation.invalid_request", "request body must be valid JSON")
	}
	return nil
}
