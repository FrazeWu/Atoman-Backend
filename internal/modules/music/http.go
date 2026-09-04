package music

import (
	"errors"
	"io"
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"
	"atoman/internal/platform/ratelimit"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Handler struct {
	service     *Service
	playLimiter *ratelimit.Limiter
}

// Use JSON extraction so search remains available while older databases catch up
// with the artist disambiguation column migration.
const artistDisambiguationSearchExpression = `COALESCE(to_jsonb("Artists")->>'disambiguation', '')`

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service, playLimiter: ratelimit.New()}
	group.Use(musicOperationLog())
	group.POST("/uploads", h.createMusicAssetUpload)
	group.GET("/uploads/:uploadId", h.getMusicAssetUpload)
	group.POST("/uploads/:uploadId/parts/:partNumber", h.createMusicAssetUploadPart)
	group.POST("/uploads/:uploadId/parts/:partNumber/complete", h.completeMusicAssetUploadPart)
	group.POST("/uploads/:uploadId/complete", h.completeMusicAssetUpload)
	group.DELETE("/uploads/:uploadId", h.cancelMusicAssetUpload)
	group.POST("/imports/albums", h.createAlbumImportSession)
	group.GET("/imports/albums", h.listAlbumImportSessions)
	group.POST("/imports/albums/:sessionId/upload", h.uploadAlbumImportArchive)
	group.POST("/imports/albums/:sessionId/multipart", h.startAlbumImportMultipart)
	group.POST("/imports/albums/:sessionId/multipart/parts/:partNumber", h.createAlbumImportMultipartPartUpload)
	group.POST("/imports/albums/:sessionId/multipart/parts/:partNumber/complete", h.completeAlbumImportMultipartPart)
	group.POST("/imports/albums/:sessionId/multipart/complete", h.completeAlbumImportMultipart)
	group.POST("/imports/albums/:sessionId/files", h.registerAlbumImportFiles)
	group.POST("/imports/albums/:sessionId/files/:fileId/parts/:partNumber", h.createAlbumImportFilePartUpload)
	group.POST("/imports/albums/:sessionId/files/:fileId/parts/:partNumber/complete", h.completeAlbumImportFilePart)
	group.POST("/imports/albums/:sessionId/files/:fileId/complete", h.completeAlbumImportFile)
	group.POST("/imports/albums/:sessionId/files/:fileId/retry", h.retryAlbumImportFile)
	group.POST("/imports/albums/:sessionId/files/:fileId/replace", h.replaceAlbumImportFile)
	group.DELETE("/imports/albums/:sessionId/files/:fileId", h.deleteAlbumImportFile)
	group.POST("/imports/albums/:sessionId/complete", h.completeAlbumImportSession)
	group.DELETE("/imports/albums/:sessionId", h.cancelAlbumImportSession)
	group.DELETE("/imports/albums/:sessionId/record", h.deleteAlbumImportRecord)
	group.GET("/imports/albums/:sessionId", h.getAlbumImportSession)
	group.POST("/imports/albums/:sessionId/repair", h.repairAlbumImportSession)
	group.POST("/imports/albums/:sessionId/commit", h.commitAlbumImportSession)
	group.POST("/entries/:entityType/:entityId/state-requests", h.createMusicEntryStateRequest)
	group.POST("/entries/:entityType/:entityId/state/emergency", h.emergencyMusicEntryState)
	group.GET("/state-requests", h.listMusicEntryStateRequests)
	group.POST("/state-requests/:requestId/decision", h.reviewMusicEntryStateRequest)
	group.DELETE("/state-requests/:requestId", h.cancelMusicEntryStateRequest)
	group.GET("/artists", h.listArtists)
	group.GET("/search", h.search)
	group.POST("/search/interactions", h.recordSearchInteraction)
	group.GET("/library", h.library)
	group.POST("/artists", h.createArtist)
	group.GET("/artists/:artistId/album-link-suggestions", h.albumLinkSuggestions)
	group.GET("/artists/:artistId", h.getArtist)
	group.GET("/albums", h.listAlbums)
	group.GET("/songs", h.listSongs)
	group.GET("/albums/:albumId", h.getAlbum)
	group.GET("/songs/:songId", h.getSongDetail)
	group.GET("/songs/:songId/apple-preview", h.getAppleSongPreview)
	group.PUT("/albums/:albumId/rating", h.setAlbumRating)
	group.DELETE("/albums/:albumId/rating", h.deleteAlbumRating)
	group.PUT("/songs/:songId/rating", h.setSongRating)
	group.DELETE("/songs/:songId/rating", h.deleteSongRating)
	group.POST("/songs/:songId/audio-replacements", h.createSongAudioReplacement)
	group.POST("/songs/:songId/convert-to-album", h.convertSongToAlbum)
	group.POST("/albums/:albumId/convert-to-song", h.convertAlbumToSong)
	group.POST("/albums/:albumId/merge", h.submitAlbumMerge)
	group.POST("/albums/:albumId/merge/preview", h.previewAlbumMerge)
	group.GET("/bookmarks/artists", h.listArtistBookmarks)
	group.POST("/bookmarks/artists", h.createArtistBookmark)
	group.DELETE("/bookmarks/artists/:artistId", h.deleteArtistBookmark)
	group.GET("/bookmarks/albums", h.listAlbumBookmarks)
	group.POST("/bookmarks/albums", h.createAlbumBookmark)
	group.DELETE("/bookmarks/albums/:albumId", h.deleteAlbumBookmark)
	group.GET("/bookmarks/playlists", h.listPlaylistBookmarks)
	group.POST("/bookmarks/playlists", h.createPlaylistBookmark)
	group.DELETE("/bookmarks/playlists/:playlistId", h.deletePlaylistBookmark)
	group.GET("/home", h.home)
	group.POST("/recommendation-events", h.recordMusicRecommendationEvents)
	group.GET("/playlists", h.listPlaylists)
	group.POST("/playlists/later/:songId", h.addToLaterPlaylist)
	group.DELETE("/playlists/later/:songId", h.deleteFromLaterPlaylist)
	group.GET("/playlists/public", h.listPublicPlaylists)
	group.POST("/playlists", h.createPlaylist)
	group.GET("/playlists/:id", h.getPlaylist)
	group.PATCH("/playlists/:id", h.updatePlaylist)
	group.DELETE("/playlists/:id", h.deletePlaylist)
	group.GET("/playlists/:id/songs", h.listPlaylistSongs)
	group.GET("/playlists/:id/songs/status", h.playlistSongStatus)
	group.POST("/playlists/:id/songs", h.addPlaylistSong)
	group.PUT("/playlists/:id/songs/order", h.reorderPlaylistSongs)
	group.DELETE("/playlists/:id/songs/:songId", h.deletePlaylistSong)
	group.POST("/plays", h.recordSongPlay)
	group.GET("/playback-progress", h.getPlaybackProgress)
	group.PUT("/playback-progress", h.savePlaybackProgress)
	group.GET("/playback-session", h.getPlaybackSession)
	group.PUT("/playback-session", h.savePlaybackSession)
	group.GET("/history", h.listListeningHistory)
	group.DELETE("/history", h.clearListeningHistory)
	group.GET("/recommend/albums", h.getRecommendedAlbums)
	group.GET("/recommend/artists", h.getRecommendedArtists)
	group.GET("/songs/:songId/lyrics", h.getSongLyrics)
	group.GET("/lyrics/annotations/pending", h.listPendingLyricAnnotations)
	group.PUT("/songs/:songId/lyrics", h.saveSongLyrics)
	group.GET("/songs/:songId/lyrics/versions", h.listSongLyricVersions)
	group.POST("/songs/:songId/lyrics/versions/:version/revert", h.revertSongLyrics)
	group.POST("/songs/:songId/lyrics/annotations", h.createLyricAnnotation)
	group.PATCH("/songs/:songId/lyrics/annotations/:annotationId", h.updateLyricAnnotation)
	group.DELETE("/songs/:songId/lyrics/annotations/:annotationId", h.deleteLyricAnnotation)
	group.POST("/songs/:songId/lyrics/annotations/:annotationId/votes", h.voteLyricAnnotation)
}

// albumLinkSuggestions godoc
// @Summary 获取关联专辑建议
// @Description 根据艺术家的 MusicBrainz 来源识别已收录的专辑，并返回目录外发行作为参考。
// @Tags music
// @Produce json
// @Param artistId path string true "艺术家 ID"
// @Success 200 {object} AlbumLinkSuggestionResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/music/artists/{artistId}/album-link-suggestions [get]
func (h *Handler) albumLinkSuggestions(c *gin.Context) {
	artistID, err := parseMusicID(c.Param("artistId"), "artistId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	viewer, hasViewer := currentMusicUser(c)
	response, err := h.service.AlbumLinkSuggestions(c.Request.Context(), musicViewer(viewer, hasViewer), artistID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, response)
}

func currentMusicUser(c *gin.Context) (authctx.CurrentUser, bool) {
	user, ok := authctx.Current(c)
	if !ok {
		return authctx.CurrentUser{}, false
	}
	return user, true
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
