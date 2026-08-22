package music

import (
	"sort"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	musicHomeRecentLimit = 8
	musicHomeForYouLimit = 8
	// Keep recommendation work bounded before applying the diversity rule.
	musicHomeCandidateLimit = musicHomeForYouLimit * 8
)

type homeAlbumCandidate struct {
	album model.Album
	score float64
}

func (s *Service) Home(user *authctx.CurrentUser) (HomeResponse, error) {
	response := HomeResponse{
		RecentlyPlayed: []model.MusicListeningHistory{},
		ForYou:         []HomeAlbumRecommendation{},
	}
	if user == nil || user.ID == uuid.Nil {
		return response, nil
	}

	history, _, err := s.ListListeningHistory(*user, 1, musicHomeRecentLimit)
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
	if len(affinity) > 0 {
		response.Personalized = true
	}
	response.ForYou, err = s.recommendHomeAlbums(affinity, seenAlbums, seenSongs)
	if len(response.ForYou) > 0 {
		response.ForYouReason = "基于播放、收藏、歌单和搜索记录"
	}
	return response, err
}

func (s *Service) homeAffinity(userID uuid.UUID, history []model.MusicListeningHistory) (map[uuid.UUID]float64, map[uuid.UUID]struct{}, map[uuid.UUID]struct{}, error) {
	affinity := make(map[uuid.UUID]float64)
	seenAlbums := make(map[uuid.UUID]struct{})
	seenSongs := make(map[uuid.UUID]struct{})

	for index, item := range history {
		if item.Song == nil {
			continue
		}
		seenSongs[item.Song.ID] = struct{}{}
		if item.Song.AlbumID != nil {
			seenAlbums[*item.Song.AlbumID] = struct{}{}
		}
		weight := 2.0 + float64(len(history)-index)*0.25
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

	for _, bookmark := range artistBookmarks {
		affinity[bookmark.ArtistID] += 6
	}

	albumIDs := make([]uuid.UUID, 0, len(albumBookmarks))
	for _, bookmark := range albumBookmarks {
		seenAlbums[bookmark.AlbumID] = struct{}{}
		albumIDs = append(albumIDs, bookmark.AlbumID)
	}
	if err := s.addHomeAlbumArtistAffinity(albumIDs, affinity); err != nil {
		return nil, nil, nil, err
	}

	var playlistSongIDs []uuid.UUID
	if err := s.db.Table("music_playlist_songs").
		Select("DISTINCT music_playlist_songs.song_id").
		Joins("JOIN music_playlists ON music_playlists.id = music_playlist_songs.playlist_id").
		Where("music_playlists.user_id = ? AND music_playlists.deleted_at IS NULL AND music_playlist_songs.deleted_at IS NULL", userID).
		Pluck("music_playlist_songs.song_id", &playlistSongIDs).Error; err != nil {
		return nil, nil, nil, err
	}
	if err := s.addHomeSongArtistAffinity(playlistSongIDs, affinity); err != nil {
		return nil, nil, nil, err
	}
	for _, songID := range playlistSongIDs {
		seenSongs[songID] = struct{}{}
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
	if s.db.Migrator().HasTable(&model.MusicSearchInteraction{}) {
		if err := s.db.Where("user_id = ?", userID).Order("created_at DESC").Limit(100).Find(&interactions).Error; err != nil {
			return nil, nil, nil, err
		}
	}
	searchAlbumIDs := make([]uuid.UUID, 0)
	searchSongIDs := make([]uuid.UUID, 0)
	searchPlaylistIDs := make([]uuid.UUID, 0)
	for _, interaction := range interactions {
		switch interaction.EntityType {
		case "artist":
			affinity[interaction.EntityID] += 3
		case "album":
			searchAlbumIDs = append(searchAlbumIDs, interaction.EntityID)
			seenAlbums[interaction.EntityID] = struct{}{}
		case "song":
			searchSongIDs = append(searchSongIDs, interaction.EntityID)
			seenSongs[interaction.EntityID] = struct{}{}
		case "playlist":
			searchPlaylistIDs = append(searchPlaylistIDs, interaction.EntityID)
		}
	}
	if len(searchPlaylistIDs) > 0 {
		var ids []uuid.UUID
		if err := s.db.Model(&model.PlaylistSong{}).Where("playlist_id IN ?", searchPlaylistIDs).Pluck("song_id", &ids).Error; err != nil {
			return nil, nil, nil, err
		}
		searchSongIDs = append(searchSongIDs, ids...)
	}
	if err := s.addHomeAlbumArtistAffinity(searchAlbumIDs, affinity); err != nil {
		return nil, nil, nil, err
	}
	if err := s.addHomeSongArtistAffinity(searchSongIDs, affinity); err != nil {
		return nil, nil, nil, err
	}

	return affinity, seenAlbums, seenSongs, nil
}

func (s *Service) addHomeAlbumArtistAffinity(albumIDs []uuid.UUID, affinity map[uuid.UUID]float64) error {
	if len(albumIDs) == 0 {
		return nil
	}
	var links []model.AlbumArtist
	if err := s.db.Where("album_id IN ?", albumIDs).Find(&links).Error; err != nil {
		return err
	}
	for _, link := range links {
		affinity[link.ArtistID] += 4
	}
	return nil
}

func (s *Service) addHomeSongArtistAffinity(songIDs []uuid.UUID, affinity map[uuid.UUID]float64) error {
	if len(songIDs) == 0 {
		return nil
	}
	var links []model.SongArtist
	if err := s.db.Where("song_id IN ?", songIDs).Find(&links).Error; err != nil {
		return err
	}
	for _, link := range links {
		affinity[link.ArtistID] += 4
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
		Order("\"Albums\".hot_score DESC, \"Albums\".release_date DESC").
		Limit(limit).
		Find(&albums).Error
	return albums, err
}

func (s *Service) recommendHomeAlbums(affinity map[uuid.UUID]float64, seenAlbums, seenSongs map[uuid.UUID]struct{}) ([]HomeAlbumRecommendation, error) {
	artistIDs := make([]uuid.UUID, 0, len(affinity))
	for artistID := range affinity {
		artistIDs = append(artistIDs, artistID)
	}

	personalizedAlbums, err := s.queryHomeAlbums(
		artistIDs,
		homeUUIDs(seenAlbums),
		homeUUIDs(seenSongs),
		musicHomeCandidateLimit,
	)
	if err != nil {
		return nil, err
	}

	candidates := make([]homeAlbumCandidate, 0, len(personalizedAlbums))
	for _, album := range personalizedAlbums {
		score := album.HotScore * 0.2
		for _, artist := range album.Artists {
			score += affinity[artist.ID]
		}
		candidates = append(candidates, homeAlbumCandidate{album: album, score: score})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return candidates[i].album.ReleaseDate.After(candidates[j].album.ReleaseDate)
		}
		return candidates[i].score > candidates[j].score
	})

	selectedAlbums := make([]model.Album, 0, musicHomeForYouLimit)
	reasons := make(map[uuid.UUID]string)
	usedArtists := make(map[uuid.UUID]struct{})
	selectedIDs := make(map[uuid.UUID]struct{})
	for _, candidate := range candidates {
		if len(selectedAlbums) == musicHomeForYouLimit {
			break
		}
		overlaps := false
		for _, artist := range candidate.album.Artists {
			if _, used := usedArtists[artist.ID]; used {
				overlaps = true
				break
			}
		}
		if overlaps {
			continue
		}
		selectedAlbums = append(selectedAlbums, candidate.album)
		selectedIDs[candidate.album.ID] = struct{}{}
		for _, artist := range candidate.album.Artists {
			usedArtists[artist.ID] = struct{}{}
		}
	}

	if len(selectedAlbums) < musicHomeForYouLimit {
		excludedAlbumIDs := make([]uuid.UUID, 0, len(seenAlbums)+len(selectedIDs))
		excludedAlbumIDs = append(excludedAlbumIDs, homeUUIDs(seenAlbums)...)
		for albumID := range selectedIDs {
			excludedAlbumIDs = append(excludedAlbumIDs, albumID)
		}
		fallbackAlbums, err := s.queryHomeAlbums(
			nil,
			excludedAlbumIDs,
			homeUUIDs(seenSongs),
			musicHomeCandidateLimit,
		)
		if err != nil {
			return nil, err
		}

		appendFallback := func(requireNewArtist bool) {
			for _, album := range fallbackAlbums {
				if len(selectedAlbums) == musicHomeForYouLimit {
					return
				}
				if _, selected := selectedIDs[album.ID]; selected {
					continue
				}
				if requireNewArtist {
					overlaps := false
					for _, artist := range album.Artists {
						if _, used := usedArtists[artist.ID]; used {
							overlaps = true
							break
						}
					}
					if overlaps {
						continue
					}
				}
				selectedAlbums = append(selectedAlbums, album)
				selectedIDs[album.ID] = struct{}{}
				reasons[album.ID] = "热门音乐推荐"
				for _, artist := range album.Artists {
					usedArtists[artist.ID] = struct{}{}
				}
			}
		}

		appendFallback(true)
		if len(selectedAlbums) < musicHomeForYouLimit {
			appendFallback(false)
		}
	}

	if len(selectedAlbums) > 0 {
		selectedIDsList := make([]uuid.UUID, 0, len(selectedAlbums))
		for _, album := range selectedAlbums {
			selectedIDsList = append(selectedIDsList, album.ID)
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
		for index, album := range selectedAlbums {
			selectedAlbums[index] = byID[album.ID]
		}
	}
	if err := hydrateAlbumStats(s.db, selectedAlbums); err != nil {
		return nil, err
	}
	results := make([]HomeAlbumRecommendation, 0, len(selectedAlbums))
	for index := range selectedAlbums {
		resolveAlbumMediaURLs(&selectedAlbums[index])
		reason := reasons[selectedAlbums[index].ID]
		if reason == "" {
			reason = "基于你的音乐记录"
			var strongestArtist *model.Artist
			var strongestScore float64
			for artistIndex := range selectedAlbums[index].Artists {
				artist := &selectedAlbums[index].Artists[artistIndex]
				if score := affinity[artist.ID]; score > strongestScore {
					strongestArtist = artist
					strongestScore = score
				}
			}
			if strongestArtist != nil {
				reason = "基于你与 " + strongestArtist.Name + " 相关的记录"
			}
		}
		results = append(results, homeAlbumRecommendation(selectedAlbums[index], reason))
	}
	return results, nil
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
