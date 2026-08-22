package music

import (
	"math"
	"sort"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	musicHomeRecentLimit          = 8
	musicHomeAffinityHistoryLimit = 32
	musicHomeForYouLimit          = 8
	musicHomeCandidateLimit       = musicHomeForYouLimit * 16
	musicHomeSearchLimit          = 100
	musicHomePlaylistSongLimit    = 512
	musicHomeHistoryHalfLife      = 45 * 24 * time.Hour
	musicHomeSearchHalfLife       = 60 * 24 * time.Hour
	musicHomeFreshnessHorizon     = 90 * 24 * time.Hour
	musicHomeMaxAlbumsPerArtist   = 2
)

type homeAlbumCandidate struct {
	album        model.Album
	score        float64
	personalized bool
}

func (s *Service) Home(user *authctx.CurrentUser) (HomeResponse, error) {
	response := HomeResponse{
		RecentlyPlayed: []model.MusicListeningHistory{},
		ForYou:         []HomeAlbumRecommendation{},
	}
	if user == nil || user.ID == uuid.Nil {
		return response, nil
	}

	history, err := s.repo.ListRecentListeningHistory(user.ID, musicHomeAffinityHistoryLimit)
	if err != nil {
		return response, err
	}
	progress, err := s.GetPlaybackProgress(*user)
	if err != nil {
		return response, err
	}
	response.RecentlyPlayed = history
	if progress != nil {
		response.ContinueListening = progress
		response.RecentlyPlayed = make([]model.MusicListeningHistory, 0, len(history))
		for _, item := range history {
			if item.SongID != progress.SongID {
				response.RecentlyPlayed = append(response.RecentlyPlayed, item)
			}
		}
	}
	response.Personalized = len(history) > 0

	affinity, seenAlbums, seenSongs, err := s.homeAffinity(user.ID, history)
	if err != nil {
		return response, err
	}
	if len(response.RecentlyPlayed) > musicHomeRecentLimit {
		response.RecentlyPlayed = response.RecentlyPlayed[:musicHomeRecentLimit]
	}
	if len(affinity) > 0 {
		response.Personalized = true
	}
	response.ForYou, err = s.recommendHomeAlbums(affinity, seenAlbums, seenSongs)
	if len(response.ForYou) > 0 {
		if len(affinity) > 0 {
			response.ForYouReason = "结合你的音乐记录和近期热度"
		} else {
			response.ForYouReason = "近期热门音乐"
		}
	}
	return response, err
}

// Build a bounded profile from recent playback and explicit library interactions.
func (s *Service) homeAffinity(userID uuid.UUID, history []model.MusicListeningHistory) (map[uuid.UUID]float64, map[uuid.UUID]struct{}, map[uuid.UUID]struct{}, error) {
	affinity := make(map[uuid.UUID]float64)
	seenAlbums := make(map[uuid.UUID]struct{})
	seenSongs := make(map[uuid.UUID]struct{})

	now := time.Now().UTC()
	for _, item := range history {
		if item.Song == nil {
			continue
		}
		seenSongs[item.Song.ID] = struct{}{}
		if item.Song.AlbumID != nil {
			seenAlbums[*item.Song.AlbumID] = struct{}{}
		}
		weight := homeHistoryAffinityWeight(item, now)
		for _, artist := range item.Song.Artists {
			affinity[artist.ID] += weight
		}
	}

	var artistBookmarks []model.ArtistBookmark
	var albumBookmarks []model.AlbumBookmark
	if err := s.db.Where("user_id = ?", userID).Find(&artistBookmarks).Error; err != nil {
		return nil, nil, nil, err
	}
	if err := s.db.Where("user_id = ?", userID).Find(&albumBookmarks).Error; err != nil {
		return nil, nil, nil, err
	}

	albumWeights := make(map[uuid.UUID]float64, len(albumBookmarks))
	for _, bookmark := range albumBookmarks {
		seenAlbums[bookmark.AlbumID] = struct{}{}
		albumWeights[bookmark.AlbumID] += 5
	}
	if err := s.addHomeAlbumArtistAffinityWeighted(albumWeights, affinity); err != nil {
		return nil, nil, nil, err
	}

	var ownedPlaylistIDs []uuid.UUID
	if err := s.db.Model(&model.Playlist{}).
		Where("user_id = ? AND deleted_at IS NULL", userID).
		Pluck("id", &ownedPlaylistIDs).Error; err != nil {
		return nil, nil, nil, err
	}
	var bookmarkedPlaylistIDs []uuid.UUID
	if err := s.db.Model(&model.PlaylistBookmark{}).
		Where("user_id = ?", userID).
		Pluck("playlist_id", &bookmarkedPlaylistIDs).Error; err != nil {
		return nil, nil, nil, err
	}
	playlistIDs := uniqueHomeUUIDs(ownedPlaylistIDs, bookmarkedPlaylistIDs)
	playlistSongWeights := make(map[uuid.UUID]float64)
	if len(playlistIDs) > 0 {
		playlistSongs, err := s.loadHomePlaylistSongs(playlistIDs)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, playlistSong := range playlistSongs {
			playlistSongWeights[playlistSong.SongID] += 3.5
			seenSongs[playlistSong.SongID] = struct{}{}
		}
	}
	if err := s.addHomeSongArtistAffinityWeighted(playlistSongWeights, affinity); err != nil {
		return nil, nil, nil, err
	}

	var importedAlbumIDs []uuid.UUID
	if err := s.db.Model(&model.AlbumImportSession{}).
		Where("user_id = ? AND status = ? AND target_album_id IS NOT NULL", userID, AlbumImportStatusCommitted).
		Pluck("target_album_id", &importedAlbumIDs).Error; err != nil {
		return nil, nil, nil, err
	}
	if err := s.addHomeAlbumArtistAffinity(importedAlbumIDs, affinity); err != nil {
		return nil, nil, nil, err
	}
	for _, albumID := range importedAlbumIDs {
		seenAlbums[albumID] = struct{}{}
	}

	var interactions []model.MusicSearchInteraction
	if err := s.db.Where("user_id = ?", userID).
		Order("created_at DESC").
		Limit(musicHomeSearchLimit).
		Find(&interactions).Error; err != nil {
		return nil, nil, nil, err
	}
	searchAlbumWeights := make(map[uuid.UUID]float64)
	searchSongWeights := make(map[uuid.UUID]float64)
	searchPlaylistWeights := make(map[uuid.UUID]float64)
	for _, interaction := range interactions {
		switch interaction.EntityType {
		case "artist":
			affinity[interaction.EntityID] += homeSignalWeight(3, interaction.CreatedAt, now, musicHomeSearchHalfLife)
		case "album":
			searchAlbumWeights[interaction.EntityID] += homeSignalWeight(4, interaction.CreatedAt, now, musicHomeSearchHalfLife)
			seenAlbums[interaction.EntityID] = struct{}{}
		case "song":
			searchSongWeights[interaction.EntityID] += homeSignalWeight(4, interaction.CreatedAt, now, musicHomeSearchHalfLife)
			seenSongs[interaction.EntityID] = struct{}{}
		case "playlist":
			searchPlaylistWeights[interaction.EntityID] += homeSignalWeight(3, interaction.CreatedAt, now, musicHomeSearchHalfLife)
		}
	}
	if len(searchPlaylistWeights) > 0 {
		playlistIDs := homeWeightedUUIDs(searchPlaylistWeights)
		playlistSongs, err := s.loadHomePlaylistSongs(playlistIDs)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, playlistSong := range playlistSongs {
			searchSongWeights[playlistSong.SongID] += searchPlaylistWeights[playlistSong.PlaylistID]
			seenSongs[playlistSong.SongID] = struct{}{}
		}
	}
	if err := s.addHomeAlbumArtistAffinityWeighted(searchAlbumWeights, affinity); err != nil {
		return nil, nil, nil, err
	}
	if err := s.addHomeSongArtistAffinityWeighted(searchSongWeights, affinity); err != nil {
		return nil, nil, nil, err
	}

	return affinity, seenAlbums, seenSongs, nil
}

func homeHistoryAffinityWeight(item model.MusicListeningHistory, now time.Time) float64 {
	playCount := item.PlayCount
	if playCount < 1 {
		playCount = 1
	}
	base := 1.5 + 0.75*math.Log1p(float64(playCount))
	return homeSignalWeight(base, item.LastPlayedAt, now, musicHomeHistoryHalfLife)
}

func homeSignalWeight(base float64, occurredAt, now time.Time, halfLife time.Duration) float64 {
	if base <= 0 {
		return 0
	}
	if occurredAt.IsZero() || halfLife <= 0 {
		return base * 0.5
	}
	age := now.Sub(occurredAt)
	if age <= 0 {
		return base
	}
	return base * math.Exp(-age.Hours()/halfLife.Hours())
}

func uniqueHomeUUIDs(groups ...[]uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{})
	values := make([]uuid.UUID, 0)
	for _, group := range groups {
		for _, id := range group {
			if id == uuid.Nil {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			values = append(values, id)
		}
	}
	return values
}
func homeWeightedUUIDs(weights map[uuid.UUID]float64) []uuid.UUID {
	values := make([]uuid.UUID, 0, len(weights))
	for id := range weights {
		values = append(values, id)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].String() < values[j].String() })
	return values
}

func (s *Service) loadHomePlaylistSongs(playlistIDs []uuid.UUID) ([]model.PlaylistSong, error) {
	if len(playlistIDs) == 0 {
		return nil, nil
	}
	var playlistSongs []model.PlaylistSong
	err := s.db.Where("playlist_id IN ?", playlistIDs).
		Order("position ASC, id ASC").
		Limit(musicHomePlaylistSongLimit).
		Find(&playlistSongs).Error
	return playlistSongs, err
}

func (s *Service) addHomeAlbumArtistAffinity(albumIDs []uuid.UUID, affinity map[uuid.UUID]float64) error {
	weights := make(map[uuid.UUID]float64, len(albumIDs))
	for _, albumID := range albumIDs {
		weights[albumID] += 4
	}
	return s.addHomeAlbumArtistAffinityWeighted(weights, affinity)
}

func (s *Service) addHomeAlbumArtistAffinityWeighted(weights map[uuid.UUID]float64, affinity map[uuid.UUID]float64) error {
	if len(weights) == 0 {
		return nil
	}
	albumIDs := make([]uuid.UUID, 0, len(weights))
	for albumID := range weights {
		albumIDs = append(albumIDs, albumID)
	}
	var links []model.AlbumArtist
	if err := s.db.Where("album_id IN ?", albumIDs).Find(&links).Error; err != nil {
		return err
	}
	for _, link := range links {
		affinity[link.ArtistID] += weights[link.AlbumID]
	}
	return nil
}

func (s *Service) addHomeSongArtistAffinity(songIDs []uuid.UUID, affinity map[uuid.UUID]float64) error {
	weights := make(map[uuid.UUID]float64, len(songIDs))
	for _, songID := range songIDs {
		weights[songID] += 4
	}
	return s.addHomeSongArtistAffinityWeighted(weights, affinity)
}

func (s *Service) addHomeSongArtistAffinityWeighted(weights map[uuid.UUID]float64, affinity map[uuid.UUID]float64) error {
	if len(weights) == 0 {
		return nil
	}
	songIDs := make([]uuid.UUID, 0, len(weights))
	for songID := range weights {
		songIDs = append(songIDs, songID)
	}
	var links []model.SongArtist
	if err := s.db.Where("song_id IN ?", songIDs).Find(&links).Error; err != nil {
		return err
	}
	for _, link := range links {
		affinity[link.ArtistID] += weights[link.SongID]
	}
	return nil
}

func (s *Service) queryHomeAlbums(artistIDs, excludedAlbumIDs, excludedSongIDs []uuid.UUID, limit int) ([]model.Album, error) {
	query := s.db.Model(&model.Album{}).
		Where("\"Albums\".lifecycle_status = ?", model.MusicLifecycleActive).
		Where("COALESCE(\"Albums\".cover_url, '') <> ''").
		Where(`EXISTS (SELECT 1 FROM "Songs" WHERE "Songs".album_id = "Albums".id AND "Songs".deleted_at IS NULL AND "Songs".lifecycle_status = 'active' AND COALESCE("Songs".audio_url, '') <> '')`)
	if len(artistIDs) > 0 {
		query = query.Joins("JOIN album_artists ON album_artists.album_id = \"Albums\".id").
			Where("album_artists.artist_id IN ?", artistIDs)
	}
	if len(excludedAlbumIDs) > 0 {
		query = query.Where("\"Albums\".id NOT IN ?", excludedAlbumIDs)
	}
	if len(excludedSongIDs) > 0 {
		query = query.Where(`NOT EXISTS (SELECT 1 FROM "Songs" WHERE "Songs".album_id = "Albums".id AND "Songs".deleted_at IS NULL AND "Songs".id IN ?)`, excludedSongIDs)
	}

	var albums []model.Album
	err := query.Distinct("\"Albums\".*").
		Preload("Artists", func(db *gorm.DB) *gorm.DB {
			return db.Select("id", "name")
		}).
		Order("\"Albums\".hot_score DESC").
		Order("\"Albums\".release_date DESC").
		Order("\"Albums\".id ASC").
		Limit(limit).
		Find(&albums).Error
	return albums, err
}

func (s *Service) recommendHomeAlbums(affinity map[uuid.UUID]float64, seenAlbums, seenSongs map[uuid.UUID]struct{}) ([]HomeAlbumRecommendation, error) {
	artistIDs := make([]uuid.UUID, 0, len(affinity))
	maxAffinity := 0.0
	for artistID, score := range affinity {
		artistIDs = append(artistIDs, artistID)
		if score > maxAffinity {
			maxAffinity = score
		}
	}
	now := time.Now().UTC()

	personalizedCandidates := make([]homeAlbumCandidate, 0)
	if len(artistIDs) > 0 {
		personalizedAlbums, err := s.queryHomeAlbums(
			artistIDs,
			homeUUIDs(seenAlbums),
			homeUUIDs(seenSongs),
			musicHomeCandidateLimit,
		)
		if err != nil {
			return nil, err
		}
		personalizedCandidates = make([]homeAlbumCandidate, 0, len(personalizedAlbums))
		for _, album := range personalizedAlbums {
			personalizedCandidates = append(personalizedCandidates, homeAlbumCandidate{
				album:        album,
				score:        homeAlbumCandidateScore(album, affinity, maxAffinity, now, true),
				personalized: true,
			})
		}
	}
	sortHomeAlbumCandidates(personalizedCandidates)

	selected := make([]homeAlbumCandidate, 0, musicHomeForYouLimit)
	selectedIDs := make(map[uuid.UUID]struct{}, musicHomeForYouLimit)
	artistCounts := make(map[uuid.UUID]int)
	appendHomeAlbumCandidates(&selected, selectedIDs, artistCounts, personalizedCandidates, true, 0)

	if len(selected) < musicHomeForYouLimit {
		fallbackAlbums, err := s.queryHomeAlbums(
			nil,
			homeUUIDs(seenAlbums),
			homeUUIDs(seenSongs),
			musicHomeCandidateLimit,
		)
		if err != nil {
			return nil, err
		}
		fallbackCandidates := make([]homeAlbumCandidate, 0, len(fallbackAlbums))
		for _, album := range fallbackAlbums {
			fallbackCandidates = append(fallbackCandidates, homeAlbumCandidate{
				album: album,
				score: homeAlbumCandidateScore(album, nil, 0, now, false),
			})
		}
		sortHomeAlbumCandidates(fallbackCandidates)

		// First use unrelated artists to preserve discovery diversity.
		appendHomeAlbumCandidates(&selected, selectedIDs, artistCounts, fallbackCandidates, true, 0)
		// Then allow a second album from an already represented artist.
		appendHomeAlbumCandidates(&selected, selectedIDs, artistCounts, personalizedCandidates, false, musicHomeMaxAlbumsPerArtist)
		appendHomeAlbumCandidates(&selected, selectedIDs, artistCounts, fallbackCandidates, false, musicHomeMaxAlbumsPerArtist)
		// A small catalog should still fill the requested batch instead of stopping at the diversity cap.
		appendHomeAlbumCandidates(&selected, selectedIDs, artistCounts, personalizedCandidates, false, 0)
		appendHomeAlbumCandidates(&selected, selectedIDs, artistCounts, fallbackCandidates, false, 0)
	}

	if len(selected) > 0 {
		selectedIDsList := make([]uuid.UUID, 0, len(selected))
		for _, candidate := range selected {
			selectedIDsList = append(selectedIDsList, candidate.album.ID)
		}
		var hydrated []model.Album
		if err := s.db.Where("id IN ? AND lifecycle_status = ?", selectedIDsList, model.MusicLifecycleActive).
			Preload("Artists", func(db *gorm.DB) *gorm.DB {
				return db.Select("id", "name")
			}).
			Find(&hydrated).Error; err != nil {
			return nil, err
		}
		byID := make(map[uuid.UUID]model.Album, len(hydrated))
		for _, album := range hydrated {
			byID[album.ID] = album
		}
		hydratedSelected := make([]homeAlbumCandidate, 0, len(selected))
		for _, candidate := range selected {
			album, ok := byID[candidate.album.ID]
			if !ok {
				continue
			}
			candidate.album = album
			hydratedSelected = append(hydratedSelected, candidate)
		}
		selected = hydratedSelected
	}

	selectedAlbums := make([]model.Album, 0, len(selected))
	for _, candidate := range selected {
		selectedAlbums = append(selectedAlbums, candidate.album)
	}
	if err := hydrateAlbumStats(s.db, selectedAlbums); err != nil {
		return nil, err
	}
	for index := range selected {
		selected[index].album = selectedAlbums[index]
		resolveAlbumMediaURLs(&selected[index].album)
	}

	results := make([]HomeAlbumRecommendation, 0, len(selected))
	for _, candidate := range selected {
		reason := "热门音乐推荐"
		if candidate.personalized {
			reason = homePersonalizedAlbumReason(candidate.album, affinity)
		}
		results = append(results, homeAlbumRecommendation(candidate.album, reason))
	}
	return results, nil
}

func homeAlbumCandidateScore(album model.Album, affinity map[uuid.UUID]float64, maxAffinity float64, now time.Time, personalized bool) float64 {
	trend := homeAlbumTrendScore(album.HotScore)
	freshness := homeAlbumFreshnessScore(album.CreatedAt, now)
	if !personalized {
		return 0.65*trend + 0.35*freshness
	}
	preference := homeAlbumAffinityScore(album, affinity, maxAffinity)
	return 0.70*preference + 0.20*trend + 0.10*freshness
}

func homeAlbumAffinityScore(album model.Album, affinity map[uuid.UUID]float64, maxAffinity float64) float64 {
	if maxAffinity <= 0 {
		return 0
	}
	scores := make([]float64, 0, len(album.Artists))
	for _, artist := range album.Artists {
		if score := affinity[artist.ID]; score > 0 {
			scores = append(scores, score)
		}
	}
	if len(scores) == 0 {
		return 0
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(scores)))
	combined := scores[0]
	denominator := maxAffinity
	if len(scores) > 1 {
		combined += scores[1] * 0.35
		denominator *= 1.35
	}
	return clampHomeRecommendationScore(combined / denominator)
}

func homeAlbumTrendScore(hotScore float64) float64 {
	if hotScore <= 0 {
		return 0
	}
	return clampHomeRecommendationScore(hotScore / (hotScore + 8))
}

func homeAlbumFreshnessScore(createdAt, now time.Time) float64 {
	if createdAt.IsZero() {
		return 0
	}
	age := now.Sub(createdAt)
	if age <= 0 {
		return 1
	}
	return clampHomeRecommendationScore(1 - age.Hours()/musicHomeFreshnessHorizon.Hours())
}

func clampHomeRecommendationScore(score float64) float64 {
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

func sortHomeAlbumCandidates(candidates []homeAlbumCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		if !candidates[i].album.ReleaseDate.Equal(candidates[j].album.ReleaseDate) {
			return candidates[i].album.ReleaseDate.After(candidates[j].album.ReleaseDate)
		}
		if !candidates[i].album.CreatedAt.Equal(candidates[j].album.CreatedAt) {
			return candidates[i].album.CreatedAt.After(candidates[j].album.CreatedAt)
		}
		return candidates[i].album.ID.String() < candidates[j].album.ID.String()
	})
}

func appendHomeAlbumCandidates(selected *[]homeAlbumCandidate, selectedIDs map[uuid.UUID]struct{}, artistCounts map[uuid.UUID]int, candidates []homeAlbumCandidate, requireNewArtist bool, maxPerArtist int) {
	for _, candidate := range candidates {
		if len(*selected) >= musicHomeForYouLimit {
			return
		}
		if _, exists := selectedIDs[candidate.album.ID]; exists {
			continue
		}
		overlaps := false
		for _, artist := range candidate.album.Artists {
			count := artistCounts[artist.ID]
			if (requireNewArtist && count > 0) || (maxPerArtist > 0 && count >= maxPerArtist) {
				overlaps = true
				break
			}
		}
		if overlaps {
			continue
		}
		*selected = append(*selected, candidate)
		selectedIDs[candidate.album.ID] = struct{}{}
		for _, artist := range candidate.album.Artists {
			artistCounts[artist.ID]++
		}
	}
}

func homePersonalizedAlbumReason(album model.Album, affinity map[uuid.UUID]float64) string {
	strongestArtist := ""
	strongestScore := 0.0
	for _, artist := range album.Artists {
		if score := affinity[artist.ID]; score > strongestScore {
			strongestArtist = artist.Name
			strongestScore = score
		}
	}
	if strongestArtist == "" {
		return "基于你的音乐记录"
	}
	return "基于你与 " + strongestArtist + " 相关的记录"
}

func homeAlbumRecommendation(album model.Album, reason string) HomeAlbumRecommendation {
	year := album.Year
	if year == 0 {
		year = album.ReleaseYear
	}
	if year == 0 && !album.ReleaseDate.IsZero() {
		year = album.ReleaseDate.Year()
	}

	artists := make([]HomeAlbumArtist, 0, len(album.Artists))
	for _, artist := range album.Artists {
		artists = append(artists, HomeAlbumArtist{ID: artist.ID, Name: artist.Name})
	}

	recommendation := HomeAlbumRecommendation{
		ID:                   album.ID,
		Title:                album.Title,
		CoverURL:             resolveMusicMediaURL(album.CoverURL),
		Status:               album.Status,
		EntryStatus:          album.EntryStatus,
		Year:                 year,
		ReleaseDatePrecision: album.ReleaseDatePrecision,
		AlbumType:            album.AlbumType,
		Artists:              artists,
		PlayCount:            album.PlayCount,
		BookmarkCount:        album.BookmarkCount,
		SongCount:            album.SongCount,
		Reason:               reason,
	}
	if !album.ReleaseDate.IsZero() {
		recommendation.ReleaseDate = album.ReleaseDate.Format("2006-01-02")
	}
	return recommendation
}

func homeUUIDs(ids map[uuid.UUID]struct{}) []uuid.UUID {
	values := make([]uuid.UUID, 0, len(ids))
	for id := range ids {
		values = append(values, id)
	}
	return values
}
