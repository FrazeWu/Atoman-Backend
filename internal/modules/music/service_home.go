package music

import (
	"sort"
	"strings"

	"atoman/internal/model"
	"atoman/internal/modules/recommendation"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
)

const (
	musicHomeRecentLimit = 8
	musicHomeForYouLimit = 8
)

type homeAlbumCandidate struct {
	album model.Album
	score float64
}

func (s *Service) Home(user *authctx.CurrentUser, discoverPage, discoverPageSize int) (HomeResponse, error) {
	discoverPage, discoverPageSize = normalizeMusicRecommendationPage(discoverPage, discoverPageSize)
	response := HomeResponse{
		RecentlyPlayed: []model.MusicListeningHistory{},
		ForYou:         []HomeAlbumRecommendation{},
		Sections:       []MusicHomeSection{},
		Discover:       []DiscoverItemResponse{},
	}
	sections, err := s.homePublicSections()
	if err != nil {
		return response, err
	}
	response.Sections = sections
	discover, total, err := s.Discover(recommendation.ModeHot, discoverPage, discoverPageSize)
	if err != nil {
		return response, err
	}
	for index := range discover {
		discover[index].Section = discover[index].Type
		switch discover[index].Type {
		case "album":
			discover[index].Reason = "近期热门专辑"
		case "artist":
			discover[index].Reason = "近期热门艺人"
		case "playlist":
			discover[index].Reason = "最新公开歌单"
		}
	}
	response.Discover = discover
	response.DiscoverMore = int64(discoverPage*discoverPageSize) < total
	response.DiscoverMeta = PaginationMetaResponse{Page: discoverPage, PageSize: discoverPageSize, Total: total, HasMore: response.DiscoverMore}
	if user == nil || user.ID == uuid.Nil {
		return response, nil
	}

	history, _, err := s.ListListeningHistory(*user, 1, musicHomeRecentLimit)
	if err != nil {
		return response, err
	}
	if len(history) > 0 {
		response.ContinueListening = &history[0]
		response.RecentlyPlayed = history[1:]
	}
	response.Personalized = len(history) > 0

	affinity, seenAlbums, seenSongs, err := s.homeAffinity(user.ID, history)
	if err != nil {
		return response, err
	}
	if len(affinity) == 0 {
		return response, nil
	}

	response.Personalized = true
	response.ForYou, err = s.recommendHomeAlbums(affinity, seenAlbums, seenSongs)
	if len(response.ForYou) > 0 {
		response.ForYouReason = "基于播放、收藏、歌单和搜索记录"
	}
	return response, err
}

func (s *Service) homePublicSections() ([]MusicHomeSection, error) {
	specs := []struct {
		key, title, order string
	}{
		{key: "hot", title: "热门", order: "hot_score DESC, play_count DESC, title ASC"},
		{key: "latest", title: "最新入库", order: "created_at DESC, title ASC"},
		{key: "random", title: "随机发现", order: "RANDOM()"},
	}
	sections := make([]MusicHomeSection, 0, len(specs))
	for _, spec := range specs {
		var albums []model.Album
		if err := s.db.Where("COALESCE(entry_status, '') <> ? AND COALESCE(status, '') <> ?", "closed", "closed").
			Preload("Artists").Preload("Songs").Order(spec.order).Limit(32).Find(&albums).Error; err != nil {
			return nil, err
		}
		visible := make([]model.Album, 0, musicHomeForYouLimit)
		for _, album := range albums {
			if !isDiscoverableHomeAlbum(album) {
				continue
			}
			visible = append(visible, album)
			if len(visible) == musicHomeForYouLimit {
				break
			}
		}
		if len(visible) == 0 {
			continue
		}
		if err := hydrateAlbumStats(s.db, visible); err != nil {
			return nil, err
		}
		for index := range visible {
			resolveAlbumMediaURLs(&visible[index])
		}
		sections = append(sections, MusicHomeSection{Key: spec.key, Title: spec.title, Albums: visible})
	}
	return sections, nil
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
	var songBookmarks []model.SongBookmark
	if err := s.db.Where("user_id = ?", userID).Find(&artistBookmarks).Error; err != nil {
		return nil, nil, nil, err
	}
	if err := s.db.Where("user_id = ?", userID).Find(&albumBookmarks).Error; err != nil {
		return nil, nil, nil, err
	}
	if err := s.db.Where("user_id = ?", userID).Find(&songBookmarks).Error; err != nil {
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

	songIDs := make([]uuid.UUID, 0, len(songBookmarks))
	for _, bookmark := range songBookmarks {
		seenSongs[bookmark.SongID] = struct{}{}
		songIDs = append(songIDs, bookmark.SongID)
	}
	if err := s.addHomeSongArtistAffinity(songIDs, affinity); err != nil {
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

func (s *Service) recommendHomeAlbums(affinity map[uuid.UUID]float64, seenAlbums, seenSongs map[uuid.UUID]struct{}) ([]HomeAlbumRecommendation, error) {
	artistIDs := make([]uuid.UUID, 0, len(affinity))
	for artistID := range affinity {
		artistIDs = append(artistIDs, artistID)
	}

	var albums []model.Album
	if err := s.db.Model(&model.Album{}).
		Joins("JOIN album_artists ON album_artists.album_id = \"Albums\".id").
		Where("album_artists.artist_id IN ?", artistIDs).
		Where("COALESCE(\"Albums\".entry_status, '') <> ? AND COALESCE(\"Albums\".status, '') <> ?", "closed", "closed").
		Distinct("\"Albums\".*").
		Preload("Artists").
		Preload("Songs").
		Find(&albums).Error; err != nil {
		return nil, err
	}

	candidates := make([]homeAlbumCandidate, 0, len(albums))
	for _, album := range albums {
		if _, seen := seenAlbums[album.ID]; seen {
			continue
		}
		if !isDiscoverableHomeAlbum(album) {
			continue
		}
		containsSeenSong := false
		for _, song := range album.Songs {
			if _, seen := seenSongs[song.ID]; seen {
				containsSeenSong = true
				break
			}
		}
		if containsSeenSong {
			continue
		}

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
	usedArtists := make(map[uuid.UUID]struct{})
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
		for _, artist := range candidate.album.Artists {
			usedArtists[artist.ID] = struct{}{}
		}
	}

	if err := hydrateAlbumStats(s.db, selectedAlbums); err != nil {
		return nil, err
	}
	results := make([]HomeAlbumRecommendation, 0, len(selectedAlbums))
	for index := range selectedAlbums {
		resolveAlbumMediaURLs(&selectedAlbums[index])
		reason := "基于你的音乐记录"
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
		results = append(results, HomeAlbumRecommendation{Album: selectedAlbums[index], Reason: reason})
	}
	return results, nil
}

func isDiscoverableHomeAlbum(album model.Album) bool {
	if strings.TrimSpace(album.CoverURL) == "" {
		return false
	}
	for _, song := range album.Songs {
		if strings.TrimSpace(song.AudioURL) != "" {
			return true
		}
	}
	return false
}
