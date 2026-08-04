package music

import (
	"sort"
	"strings"

	"atoman/internal/model"
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

func (s *Service) Home(user *authctx.CurrentUser) (HomeResponse, error) {
	response := HomeResponse{
		RecentlyPlayed: []model.MusicListeningHistory{},
		ForYou:         []model.Album{},
	}
	if user == nil || user.ID == uuid.Nil {
		return response, nil
	}

	history, _, err := s.ListListeningHistory(*user, 1, musicHomeRecentLimit)
	if err != nil {
		return response, err
	}
	response.RecentlyPlayed = history
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

func (s *Service) recommendHomeAlbums(affinity map[uuid.UUID]float64, seenAlbums, seenSongs map[uuid.UUID]struct{}) ([]model.Album, error) {
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

	results := make([]model.Album, 0, musicHomeForYouLimit)
	usedArtists := make(map[uuid.UUID]struct{})
	for _, candidate := range candidates {
		if len(results) == musicHomeForYouLimit {
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
		results = append(results, candidate.album)
		for _, artist := range candidate.album.Artists {
			usedArtists[artist.ID] = struct{}{}
		}
	}

	if err := hydrateAlbumStats(s.db, results); err != nil {
		return nil, err
	}
	for index := range results {
		resolveAlbumMediaURLs(&results[index])
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
