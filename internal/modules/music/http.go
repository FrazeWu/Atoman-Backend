package music

import (
	"errors"
	"io"
	"net/http"
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

type Handler struct {
	service *Service
}

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service}
	group.GET("/artists", h.listArtists)
	group.GET("/artists/:artistId", h.getArtist)
	group.GET("/albums", h.listAlbums)
	group.GET("/albums/:albumId", h.getAlbum)
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
