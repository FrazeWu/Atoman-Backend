package music

import (
	"errors"
	"net/http"
	"os"
	"strconv"
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
	for index := range album.ArtistCredits {
		if album.ArtistCredits[index].Artist != nil {
			album.ArtistCredits[index].Artist.ImageURL = resolveMusicMediaURL(album.ArtistCredits[index].Artist.ImageURL)
		}
	}
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

	var songRows []struct {
		AlbumID   uuid.UUID
		PlayCount int64
		SongCount int64
	}
	if err := db.Model(&model.Song{}).
		Select("album_id, COALESCE(SUM(play_count), 0) AS play_count, COUNT(*) AS song_count").
		Where("album_id IN ? AND lifecycle_status = ?", albumIDs, model.MusicLifecycleActive).
		Group("album_id").
		Scan(&songRows).Error; err != nil {
		return err
	}
	for _, row := range songRows {
		if idx, ok := albumIndex[row.AlbumID]; ok {
			albums[idx].PlayCount = row.PlayCount
			albums[idx].SongCount = row.SongCount
		}
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
		Joins("JOIN \"Songs\" ON \"Songs\".id = song_artists.song_id AND \"Songs\".deleted_at IS NULL AND \"Songs\".lifecycle_status = 'active'").
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
		Where("\"Albums\".deleted_at IS NULL").
		Where("TRIM(COALESCE(\"Albums\".cover_url, '')) <> ''").
		Where("\"Albums\".lifecycle_status = ?", model.MusicLifecycleActive).
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

	viewer, hasViewer := currentMusicUser(c)
	db := scopeVisibleMusicEntries(h.service.db.Model(&model.Artist{}).Distinct("\"Artists\".*"), "\"Artists\"", "created_by", musicViewer(viewer, hasViewer), false)
	if query != "" {
		like := "%" + strings.ToLower(query) + "%"
		db = db.
			Joins("LEFT JOIN artist_aliases ON artist_aliases.artist_id = \"Artists\".id").
			Where("LOWER(\"Artists\".name) LIKE ? OR LOWER("+artistDisambiguationSearchExpression+") LIKE ? OR LOWER(COALESCE(\"Artists\".legal_name, '')) LIKE ? OR LOWER(COALESCE(artist_aliases.alias, '')) LIKE ?", like, like, like, like)
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

// createArtist godoc
// @Summary 创建艺术家
// @Description 创建艺术家并直接保存完整资料，包括艺名、活动日期和组合成员。
// @Tags music
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param input body CreateArtistRequest true "艺术家资料"
// @Success 201 {object} model.Artist
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Router /api/v1/music/artists [post]
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

	viewer, hasViewer := currentMusicUser(c)
	viewerPtr := musicViewer(viewer, hasViewer)
	var artist model.Artist
	query := scopeVisibleMusicEntries(h.service.db, "\"Artists\"", "created_by", viewerPtr, true).
		Preload("Aliases").
		Preload("Albums", func(db *gorm.DB) *gorm.DB {
			return scopeVisibleMusicEntries(db, "\"Albums\"", "uploaded_by", viewerPtr, false)
		}).
		Preload("Albums.Artists", visibleArtistPreload(viewerPtr)).Preload("Albums.ArtistCredits", visibleAlbumArtistCreditsPreload(viewerPtr)).Preload("Albums.ArtistCredits.Artist", visibleArtistPreload(viewerPtr)).Preload("Albums.Songs", visibleSongPreload(viewerPtr))
	if h.service.db.Migrator().HasTable(&model.ArtistMember{}) {
		query = query.Preload("MemberRelations.MemberArtist", visibleArtistPreload(viewerPtr))
	}
	if err := query.First(&artist, "\"Artists\".id = ?", artistID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("music.artist_not_found", "Artist not found"))
			return
		}
		httpx.Error(c, err)
		return
	}
	visibleRelations := artist.MemberRelations[:0]
	for _, relation := range artist.MemberRelations {
		if relation.MemberArtist == nil || !canViewMusicLifecycle(relation.MemberArtist.LifecycleStatus, relation.MemberArtist.CreatedBy, viewerPtr, false) {
			continue
		}
		visibleRelations = append(visibleRelations, relation)
	}
	artist.MemberRelations = visibleRelations
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
		resolveAlbumMediaURLs(&artist.Albums[i])
		for j := range artist.Albums[i].Artists {
			artist.Albums[i].Artists[j].ImageURL = resolveMusicMediaURL(artist.Albums[i].Artists[j].ImageURL)
		}
	}
	artist.Albums = uniqueAlbums(artist.Albums)
	for i := range artist.MemberRelations {
		if artist.MemberRelations[i].MemberArtist != nil {
			artist.MemberRelations[i].MemberArtist.ImageURL = resolveMusicMediaURL(artist.MemberRelations[i].MemberArtist.ImageURL)
		}
	}

	httpx.OK(c, http.StatusOK, buildArtistDetailResponse(artist))
}

func buildArtistDetailResponse(artist model.Artist) ArtistDetailResponse {
	now := time.Now()
	resp := ArtistDetailResponse{
		ID:                       artist.ID,
		Name:                     artist.Name,
		Disambiguation:           artist.Disambiguation,
		DisplayName:              artist.DisplayName,
		LegalName:                artist.LegalName,
		StageNamesJSON:           artist.StageNamesJSON,
		Bio:                      artist.Bio,
		ImageURL:                 artist.ImageURL,
		Nationality:              artist.Nationality,
		BirthPlace:               artist.BirthPlace,
		BirthDate:                artist.BirthDate,
		BirthDatePrecision:       artist.BirthDatePrecision,
		BirthYear:                artist.BirthYear,
		DeathYear:                artist.DeathYear,
		ArtistForm:               artist.ArtistForm,
		Members:                  artist.Members,
		EntryStatus:              artist.EntryStatus,
		LifecycleStatus:          artist.LifecycleStatus,
		EditStatus:               artist.EditStatus,
		RedirectTo:               artist.RedirectTo,
		Albums:                   artist.Albums,
		Aliases:                  artist.Aliases,
		PlayCount:                artist.PlayCount,
		BookmarkCount:            artist.BookmarkCount,
		Sources:                  artist.Sources,
		ActiveStartDatePrecision: artist.ActiveStartDatePrecision,
		ActiveEndDatePrecision:   artist.ActiveEndDatePrecision,
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
			ArtistID:           relation.MemberArtist.ID,
			Name:               relation.MemberArtist.Name,
			ImageURL:           relation.MemberArtist.ImageURL,
			JoinDatePrecision:  relation.JoinDatePrecision,
			LeaveDatePrecision: relation.LeaveDatePrecision,
			IsPublished:        relation.MemberArtist.LifecycleStatus == "" || relation.MemberArtist.LifecycleStatus == model.MusicLifecycleActive,
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

// listAlbums godoc
// @Summary 获取音乐专辑列表
// @Tags music
// @Produce json
// @Param q query string false "搜索关键词"
// @Param artist_id query string false "艺术家 ID"
// @Param sort query string false "排序方式"
// @Param page query int false "页码"
// @Param page_size query int false "每页数量"
// @Param cursor query string false "仅 sort=-created_at 可用；传 cursor= 启动游标分页"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/music/albums [get]
func (h *Handler) listAlbums(c *gin.Context) {
	page, pageSize := httpx.PageParams(c)
	query := strings.TrimSpace(c.Query("q"))
	artistIDRaw := strings.TrimSpace(c.Query("artist_id"))
	sort := strings.TrimSpace(c.Query("sort"))

	cursorRaw, useCursor := c.GetQuery("cursor")
	cursor, err := parseMusicCreatedAtCursor(strings.TrimSpace(cursorRaw))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if useCursor && sort != "-created_at" {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "cursor requires sort=-created_at"))
		return
	}

	viewer, hasViewer := currentMusicUser(c)
	viewerPtr := musicViewer(viewer, hasViewer)
	db := scopeVisibleMusicEntries(h.service.db.Model(&model.Album{}), "\"Albums\"", "uploaded_by", viewerPtr, false)
	joinedArtists := false
	if query != "" {
		like := "%" + strings.ToLower(query) + "%"
		artistVisibility, artistArgs := musicEntryVisibilityCondition("search_artists", "created_by", viewerPtr, true)
		db = db.
			Joins("LEFT JOIN album_artists AS search_album_artists ON search_album_artists.album_id = \"Albums\".id").
			Joins("LEFT JOIN \"Artists\" AS search_artists ON search_artists.id = search_album_artists.artist_id AND "+artistVisibility, artistArgs...)
		joinedArtists = true
		db = db.Where("LOWER(\"Albums\".title) LIKE ? OR LOWER(search_artists.name) LIKE ?", like, like)
	}
	if artistIDRaw != "" {
		artistID, err := parseMusicID(artistIDRaw, "artist_id")
		if err != nil {
			httpx.Error(c, err)
			return
		}
		artistVisibility, artistArgs := musicEntryVisibilityCondition("filter_artists", "created_by", viewerPtr, true)
		db = db.Joins("JOIN album_artists AS filter_album_artists ON filter_album_artists.album_id = \"Albums\".id").
			Joins("JOIN \"Artists\" AS filter_artists ON filter_artists.id = filter_album_artists.artist_id AND "+artistVisibility, artistArgs...).
			Where("filter_album_artists.artist_id = ?", artistID)
		joinedArtists = true
	}
	if cursor != nil {
		db = db.Where("(\"Albums\".created_at < ? OR (\"Albums\".created_at = ? AND \"Albums\".id < ?))", cursor.CreatedAt, cursor.CreatedAt, cursor.ID)
	}

	var total int64
	if !useCursor {
		countDB := db
		if joinedArtists {
			countDB = countDB.Distinct("\"Albums\".id")
		}
		if err := countDB.Count(&total).Error; err != nil {
			httpx.Error(c, err)
			return
		}
	}

	var albums []model.Album
	findDB := db.Preload("Artists", visibleArtistPreload(viewerPtr)).Preload("ArtistCredits", visibleAlbumArtistCreditsPreload(viewerPtr)).Preload("ArtistCredits.Artist", visibleArtistPreload(viewerPtr))
	if joinedArtists {
		findDB = findDB.Distinct("\"Albums\".*")
	}
	orders := albumSortOrders(sort)
	if useCursor {
		orders = []string{"\"Albums\".created_at DESC", "\"Albums\".id DESC"}
	}
	for _, order := range orders {
		findDB = findDB.Order(order)
	}
	if useCursor {
		findDB = findDB.Limit(pageSize + 1)
	} else {
		findDB = findDB.Limit(pageSize).Offset(httpx.Offset(page, pageSize))
	}
	if err := findDB.Find(&albums).Error; err != nil {
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

	if useCursor {
		hasMore := len(albums) > pageSize
		if hasMore {
			albums = albums[:pageSize]
		}
		nextCursor := ""
		if hasMore && len(albums) > 0 {
			last := albums[len(albums)-1]
			nextCursor = encodeMusicCreatedAtCursor(last.CreatedAt, last.ID)
		}
		writeMusicCursorList(c, albums, pageSize, hasMore, nextCursor)
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

	viewer, hasViewer := currentMusicUser(c)
	viewerPtr := musicViewer(viewer, hasViewer)
	var albums []model.Album
	query := scopeVisibleMusicEntries(h.service.db, "\"Albums\"", "uploaded_by", viewerPtr, true)
	if err := query.Preload("Artists", visibleArtistPreload(viewerPtr)).Preload("ArtistCredits", visibleAlbumArtistCreditsPreload(viewerPtr)).Preload("ArtistCredits.Artist", visibleArtistPreload(viewerPtr)).Preload("Songs", visibleSongPreload(viewerPtr)).Preload("Songs.ArtistCredits", visibleSongArtistCreditsPreload(viewerPtr)).Preload("Songs.ArtistCredits.Artist", visibleArtistPreload(viewerPtr)).Where("\"Albums\".id = ?", albumID).Limit(1).Find(&albums).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	if len(albums) == 0 {
		httpx.Error(c, apperr.NotFound("music.album_not_found", "Album not found"))
		return
	}
	album := albums[0]
	albumRows := []model.Album{album}
	if err := hydrateAlbumStats(h.service.db, albumRows); err != nil {
		httpx.Error(c, err)
		return
	}
	album = albumRows[0]
	resolveAlbumMediaURLs(&album)
	httpx.OK(c, http.StatusOK, album)
}

func uniqueAlbums(albums []model.Album) []model.Album {
	unique := make([]model.Album, 0, len(albums))
	seen := make(map[uuid.UUID]struct{}, len(albums))
	for _, album := range albums {
		if _, exists := seen[album.ID]; exists {
			continue
		}
		seen[album.ID] = struct{}{}
		unique = append(unique, album)
	}
	return unique
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
// @Failure 429 {object} handlers.ErrorResponse
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
	identity := "ip:" + c.ClientIP()
	if userID != nil {
		identity = "user:" + userID.String()
	}
	if allowed, retryAfter := h.playLimiter.Allow(identity, 12, time.Minute); !allowed {
		seconds := int((retryAfter + time.Second - 1) / time.Second)
		c.Header("Retry-After", strconv.Itoa(seconds))
		httpx.Error(c, apperr.New(http.StatusTooManyRequests, "music.play_rate_limited", "Too many play reports", map[string]any{"retry_after": seconds}))
		return
	}
	if err := h.service.RecordSongPlay(userID, req.SongID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"recorded": true})
}

// getPlaybackProgress godoc
// @Summary 获取跨端续听进度
// @Description 返回当前用户最近一首未完成歌曲的播放进度；没有可续听内容时返回 null。
// @Tags music
// @Produce json
// @Success 200 {object} model.MusicPlaybackProgress
// @Failure 401 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/music/playback-progress [get]
func (h *Handler) getPlaybackProgress(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	progress, err := h.service.GetPlaybackProgress(user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if progress != nil && progress.Song != nil {
		progress.Song.AudioURL = resolveMusicMediaURL(progress.Song.AudioURL)
		progress.Song.CoverURL = resolveMusicMediaURL(progress.Song.CoverURL)
		if progress.Song.Album != nil {
			progress.Song.Album.CoverURL = resolveMusicMediaURL(progress.Song.Album.CoverURL)
		}
	}
	httpx.OK(c, http.StatusOK, progress)
}

// savePlaybackProgress godoc
// @Summary 保存跨端续听进度
// @Tags music
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param input body SavePlaybackProgressRequest true "播放进度"
// @Success 200 {object} model.MusicPlaybackProgress
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/music/playback-progress [put]
func (h *Handler) savePlaybackProgress(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var input SavePlaybackProgressRequest
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	progress, err := h.service.SavePlaybackProgress(user, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, progress)
}

// getPlaybackSession godoc
// @Summary 获取跨端播放队列
// @Tags music
// @Produce json
// @Success 200 {object} PlaybackSessionResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/music/playback-session [get]
func (h *Handler) getPlaybackSession(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	session, err := h.service.GetPlaybackSession(user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if session != nil {
		for index := range session.Queue {
			session.Queue[index].AudioURL = resolveMusicMediaURL(session.Queue[index].AudioURL)
			session.Queue[index].CoverURL = resolveMusicMediaURL(session.Queue[index].CoverURL)
			if session.Queue[index].Album != nil {
				session.Queue[index].Album.CoverURL = resolveMusicMediaURL(session.Queue[index].Album.CoverURL)
			}
		}
	}
	httpx.OK(c, http.StatusOK, session)
}

// savePlaybackSession godoc
// @Summary 保存跨端播放队列
// @Tags music
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param input body SavePlaybackSessionRequest true "播放队列"
// @Success 200 {object} PlaybackSessionResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/music/playback-session [put]
func (h *Handler) savePlaybackSession(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var input SavePlaybackSessionRequest
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	session, err := h.service.SavePlaybackSession(user, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, session)
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

// clearListeningHistory godoc
// @Summary 清空播放历史
// @Tags music
// @Success 204
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/music/history [delete]
func (h *Handler) clearListeningHistory(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	if err := h.service.ClearListeningHistory(user); err != nil {
		httpx.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// home godoc
// @Summary 获取个性化音乐首页内容
// @Description 登录用户返回最近播放、续播和基于播放与收藏记录的推荐；访客返回空的个性化内容。
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
	if user != nil {
		response.RequestID = uuid.NewString()
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
	if response.ContinueListening != nil && response.ContinueListening.Song != nil {
		response.ContinueListening.Song.AudioURL = resolveMusicMediaURL(response.ContinueListening.Song.AudioURL)
		response.ContinueListening.Song.CoverURL = resolveMusicMediaURL(response.ContinueListening.Song.CoverURL)
		if response.ContinueListening.Song.Album != nil {
			response.ContinueListening.Song.Album.CoverURL = resolveMusicMediaURL(response.ContinueListening.Song.Album.CoverURL)
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
	case "-release_date":
		return []string{"\"Albums\".release_date DESC", "\"Albums\".title ASC"}
	default:
		return []string{"\"Albums\".release_date DESC", "\"Albums\".title ASC"}
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
