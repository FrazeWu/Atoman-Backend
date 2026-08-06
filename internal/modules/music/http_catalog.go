package music

import (
	"errors"
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

// hydrateArtistDisplayImages supplies a display-only fallback for artists
// without a dedicated portrait. It never persists the album cover as artist data.
func hydrateArtistDisplayImages(db *gorm.DB, artists []model.Artist) error {
	missingImageIDs := make([]uuid.UUID, 0, len(artists))
	for _, artist := range artists {
		if strings.TrimSpace(artist.ImageURL) == "" {
			missingImageIDs = append(missingImageIDs, artist.ID)
		}
	}
	if len(missingImageIDs) == 0 {
		return nil
	}

	var coverRows []struct {
		ArtistID uuid.UUID
		CoverURL string
	}
	if err := db.Table("album_artists").
		Select("album_artists.artist_id AS artist_id, \"Albums\".cover_url AS cover_url").
		Joins("JOIN \"Albums\" ON \"Albums\".id = album_artists.album_id").
		Where("album_artists.artist_id IN ?", missingImageIDs).
		Where("TRIM(COALESCE(\"Albums\".cover_url, '')) <> ''").
		Where("COALESCE(\"Albums\".entry_status, '') <> ? AND COALESCE(\"Albums\".status, '') <> ?", "closed", "closed").
		Order("album_artists.artist_id ASC").
		Order("\"Albums\".release_date DESC").
		Order("\"Albums\".created_at DESC").
		Scan(&coverRows).Error; err != nil {
		return err
	}

	coversByArtistID := make(map[uuid.UUID]string, len(coverRows))
	for _, row := range coverRows {
		if _, exists := coversByArtistID[row.ArtistID]; !exists {
			coversByArtistID[row.ArtistID] = row.CoverURL
		}
	}
	for i := range artists {
		if strings.TrimSpace(artists[i].ImageURL) == "" {
			artists[i].ImageURL = coversByArtistID[artists[i].ID]
		}
	}

	return nil
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
	if err := hydrateArtistDisplayImages(h.service.db, artists); err != nil {
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
	if err := hydrateArtistDisplayImages(h.service.db, artistRows); err != nil {
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
	canonicalID := album.ID
	if album.CanonicalAlbumID != nil {
		canonicalID = *album.CanonicalAlbumID
	}
	if err := h.service.db.Where("id <> ? AND (id = ? OR canonical_album_id = ?)", album.ID, canonicalID, canonicalID).
		Where("COALESCE(entry_status, '') <> ? AND COALESCE(status, '') <> ?", "closed", "closed").
		Preload("Artists").Order("edition_type ASC, release_date DESC, title ASC").Find(&album.OtherVersions).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	resolveAlbumMediaURLs(&album)
	for index := range album.OtherVersions {
		resolveAlbumMediaURLs(&album.OtherVersions[index])
	}
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

// home godoc
// @Summary 获取音乐首页内容
// @Description 登录用户返回最近播放和基于播放/收藏艺术家的未接触专辑；其他用户返回非个性化结果。
// @Tags music
// @Produce json
// @Success 200 {object} HomeResponse
// @Router /api/v1/music/home [get]
func (h *Handler) home(c *gin.Context) {
	var user *authctx.CurrentUser
	if current, ok := authctx.Current(c); ok {
		user = &current
	}
	response, err := h.service.Home(user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	for index := range response.RecentlyPlayed {
		if response.RecentlyPlayed[index].Song == nil {
			continue
		}
		response.RecentlyPlayed[index].Song.AudioURL = resolveMusicMediaURL(response.RecentlyPlayed[index].Song.AudioURL)
		response.RecentlyPlayed[index].Song.CoverURL = resolveMusicMediaURL(response.RecentlyPlayed[index].Song.CoverURL)
		if response.RecentlyPlayed[index].Song.Album != nil {
			response.RecentlyPlayed[index].Song.Album.CoverURL = resolveMusicMediaURL(response.RecentlyPlayed[index].Song.Album.CoverURL)
		}
	}
	httpx.OK(c, http.StatusOK, response)
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

// discover godoc
// @Summary 获取音乐发现流
// @Description 返回混合发现流，按专辑、艺人、公开歌单的简单规则混排。
// @Tags music-discovery
// @Produce json
// @Param mode query string false "排序模式" Enums(hot,featured,latest) default(hot)
// @Success 200 {object} DiscoverListResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Router /api/v1/music/discover [get]
func (h *Handler) discover(c *gin.Context) {
	mode := recommendation.ModeHot
	if rawMode := strings.TrimSpace(c.Query("mode")); rawMode != "" {
		parsedMode, err := parseMusicRecommendationMode(rawMode)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		mode = parsedMode
	}
	page, pageSize := httpx.PageParams(c)
	items, total, err := h.service.Discover(mode, page, pageSize)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	for i := range items {
		items[i].ImageURL = resolveMusicMediaURL(items[i].ImageURL)
	}
	httpx.List(c, items, page, pageSize, total)
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
	case recommendation.ModeLatest:
		return recommendation.ModeLatest, nil
	default:
		return "", apperr.BadRequest("validation.invalid_request", "mode must be one of hot, featured, discover, latest")
	}
}
