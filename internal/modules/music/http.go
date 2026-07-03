package music

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

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
	group.GET("/artists/:artistId", h.getArtist)
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
	group.GET("/playlists", h.listPlaylists)
	group.POST("/playlists", h.createPlaylist)
	group.DELETE("/playlists/:id", h.deletePlaylist)
	group.GET("/playlists/:id/songs", h.listPlaylistSongs)
	group.POST("/playlists/:id/songs", h.addPlaylistSong)
	group.DELETE("/playlists/:id/songs/:songId", h.deletePlaylistSong)
	group.GET("/recommend/albums", h.getRecommendedAlbums)
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

	db := h.service.db.Model(&model.Artist{}).Where("COALESCE(entry_status, '') <> ?", "closed")
	if query != "" {
		like := "%" + strings.ToLower(query) + "%"
		db = db.Where("LOWER(name) LIKE ?", like)
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		httpx.Error(c, err)
		return
	}

	var artists []model.Artist
	if err := db.Order("name ASC").Limit(pageSize).Offset(httpx.Offset(page, pageSize)).Find(&artists).Error; err != nil {
		httpx.Error(c, err)
		return
	}

	httpx.List(c, artists, page, pageSize, total)
}

func (h *Handler) getArtist(c *gin.Context) {
	artistID, err := parseMusicID(c.Param("artistId"), "artistId")
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var artist model.Artist
	if err := h.service.db.Preload("Albums.Artists").First(&artist, "id = ?", artistID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("music.artist_not_found", "Artist not found"))
			return
		}
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, artist)
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
	resolveAlbumMediaURLs(&album)
	httpx.OK(c, http.StatusOK, album)
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
	bookmarks, total, err := h.service.ListArtistBookmarks(user, page, pageSize)
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
	bookmarks, total, err := h.service.ListAlbumBookmarks(user, page, pageSize)
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
	bookmarks, total, err := h.service.ListSongBookmarks(user, page, pageSize)
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
	playlists, total, err := h.service.ListPlaylists(user, page, pageSize)
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
			SongCount:   0,
		})
	}
	httpx.List(c, rows, page, pageSize, total)
}

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
	playlist, err := h.service.CreatePlaylist(user, req.Name)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, PlaylistSummaryResponse{
		ID:          playlist.ID,
		UserID:      playlist.UserID,
		Name:        playlist.Name,
		Description: playlist.Description,
		SongCount:   0,
	})
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

func (h *Handler) listPlaylistSongs(c *gin.Context) {
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
