package music

import (
	"fmt"
	"math"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/feed"
	"atoman/internal/modules/recommendation"

	"github.com/google/uuid"
)

const musicRecommendationCandidateLimit = 1000

func (s *Service) RecommendAlbumsByMode(mode recommendation.Mode, page int, pageSize int) ([]feed.RecommendationItemDTO, int64, error) {
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)

	var albums []model.Album
	if err := s.db.Model(&model.Album{}).
		Preload("Songs", "lifecycle_status = ?", model.MusicLifecycleActive).
		Where("\"Albums\".lifecycle_status = ?", model.MusicLifecycleActive).
		Order("\"Albums\".hot_score DESC, \"Albums\".created_at DESC").
		Limit(musicRecommendationCandidateLimit).
		Find(&albums).Error; err != nil {
		return nil, 0, err
	}

	albumIDs := make([]uuid.UUID, 0, len(albums))
	for _, album := range albums {
		albumIDs = append(albumIDs, album.ID)
	}
	albumBookmarkCounts := map[uuid.UUID]int64{}
	if len(albumIDs) > 0 {
		var bookmarkRows []struct {
			AlbumID uuid.UUID
			Count   int64
		}
		if err := s.db.Model(&model.AlbumBookmark{}).
			Select("album_id, COUNT(*) AS count").
			Where("album_id IN ?", albumIDs).
			Group("album_id").
			Scan(&bookmarkRows).Error; err != nil {
			return nil, 0, err
		}
		for _, row := range bookmarkRows {
			albumBookmarkCounts[row.AlbumID] = row.Count
		}
	}

	candidates := make([]recommendation.Candidate, 0, len(albums))
	albumByID := make(map[string]model.Album, len(albums))
	for _, album := range albums {
		qualityScore := normalizeMusicDiscoverQuality(album.HotScore)
		candidates = append(candidates, recommendation.Candidate{
			Module:              "music",
			EntityType:          recommendation.EntityAlbum,
			EntityID:            album.ID.String(),
			SourceKey:           album.ID.String(),
			QualityScore:        qualityScore,
			TrendScore:          clampMusicRecommendation(album.HotScore / 10),
			FreshnessScore:      normalizeMusicAlbumFreshness(album.CreatedAt, 30*24*time.Hour),
			AuthorityScore:      0.5,
			ExposureScore:       0,
			EditorialScore:      0,
			PublishedAtUnixNano: album.CreatedAt.UnixNano(),
		})
		albumByID[album.ID.String()] = album
	}

	ranked := recommendation.RankCandidates(mode, candidates, 0)
	items := make([]feed.RecommendationItemDTO, 0, len(ranked))
	for _, item := range ranked {
		album, ok := albumByID[item.EntityID]
		if !ok {
			continue
		}
		items = append(items, feed.RecommendationItemDTO{
			ID:         album.ID.String(),
			Title:      album.Title,
			Summary:    "",
			ImageURL:   album.CoverURL,
			TargetPath: "/music/album/" + album.ID.String(),
			ScoreLabel: musicRecommendationScoreLabel(mode, item.FinalScore),
			PlayCount: func() int64 {
				var total int64
				for _, song := range album.Songs {
					total += song.PlayCount
				}
				return total
			}(),
			BookmarkCount: albumBookmarkCounts[album.ID],
		})
	}

	total := int64(len(items))
	start := (page - 1) * pageSize
	if start > len(items) {
		start = len(items)
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], total, nil
}

type artistWithHotScore struct {
	model.Artist
	MaxHotScore float64
	AlbumCount  int64
}

func (s *Service) RecommendArtistsByMode(mode recommendation.Mode, page int, pageSize int) ([]feed.RecommendationItemDTO, int64, error) {
	page, pageSize = normalizeMusicRecommendationPage(page, pageSize)

	var dbArtists []artistWithHotScore
	if err := s.db.Table("Artists").
		Select("\"Artists\".*, COALESCE(MAX(a.hot_score), 0) as max_hot_score, COUNT(a.id) as album_count").
		Joins("LEFT JOIN album_artists aa ON aa.artist_id = \"Artists\".id").
		Joins("LEFT JOIN \"Albums\" a ON a.id = aa.album_id AND a.lifecycle_status = 'active'").
		Where("\"Artists\".lifecycle_status = ?", model.MusicLifecycleActive).
		Group("\"Artists\".id").
		Order("max_hot_score DESC, \"Artists\".created_at DESC").
		Limit(musicRecommendationCandidateLimit).
		Find(&dbArtists).Error; err != nil {
		return nil, 0, err
	}

	artistIDs := make([]uuid.UUID, 0, len(dbArtists))
	artists := make([]model.Artist, 0, len(dbArtists))
	for _, art := range dbArtists {
		artistIDs = append(artistIDs, art.ID)
		artists = append(artists, art.Artist)
	}
	if err := hydrateArtistDisplayImages(s.db, artists); err != nil {
		return nil, 0, err
	}
	for i := range dbArtists {
		dbArtists[i].ImageURL = artists[i].ImageURL
	}

	artistBookmarkCounts := map[uuid.UUID]int64{}
	if len(artistIDs) > 0 {
		var bookmarkRows []struct {
			ArtistID uuid.UUID
			Count    int64
		}
		if err := s.db.Model(&model.ArtistBookmark{}).
			Select("artist_id, COUNT(*) AS count").
			Where("artist_id IN ?", artistIDs).
			Group("artist_id").
			Scan(&bookmarkRows).Error; err != nil {
			return nil, 0, err
		}
		for _, row := range bookmarkRows {
			artistBookmarkCounts[row.ArtistID] = row.Count
		}
	}

	artistPlayCounts := map[uuid.UUID]int64{}
	if len(artistIDs) > 0 {
		var playRows []struct {
			ArtistID  uuid.UUID
			PlayCount int64
		}
		if err := s.db.Table("song_artists").
			Select("song_artists.artist_id AS artist_id, COALESCE(SUM(\"Songs\".play_count), 0) AS play_count").
			Joins("JOIN \"Songs\" ON \"Songs\".id = song_artists.song_id AND \"Songs\".lifecycle_status = 'active'").
			Where("song_artists.artist_id IN ?", artistIDs).
			Group("song_artists.artist_id").
			Scan(&playRows).Error; err != nil {
			return nil, 0, err
		}
		for _, row := range playRows {
			artistPlayCounts[row.ArtistID] = row.PlayCount
		}
	}

	candidates := make([]recommendation.Candidate, 0, len(dbArtists))
	artistByID := make(map[string]artistWithHotScore, len(dbArtists))
	for _, art := range dbArtists {
		qualityScore := normalizeMusicDiscoverQuality(art.MaxHotScore)
		candidates = append(candidates, recommendation.Candidate{
			Module:              "music",
			EntityType:          recommendation.EntityArtist,
			EntityID:            art.ID.String(),
			SourceKey:           art.ID.String(),
			QualityScore:        qualityScore,
			TrendScore:          clampMusicRecommendation(art.MaxHotScore / 10),
			FreshnessScore:      normalizeMusicAlbumFreshness(art.CreatedAt, 30*24*time.Hour),
			AuthorityScore:      math.Min(1.0, 0.5+0.1*float64(art.AlbumCount)),
			ExposureScore:       0,
			EditorialScore:      0,
			PublishedAtUnixNano: art.CreatedAt.UnixNano(),
		})
		artistByID[art.ID.String()] = art
	}

	ranked := recommendation.RankCandidates(mode, candidates, 0)
	items := make([]feed.RecommendationItemDTO, 0, len(ranked))
	for _, item := range ranked {
		art, ok := artistByID[item.EntityID]
		if !ok {
			continue
		}
		items = append(items, feed.RecommendationItemDTO{
			ID:            art.ID.String(),
			Title:         art.Name,
			Summary:       art.Bio,
			ImageURL:      art.ImageURL,
			TargetPath:    "/music/artist/" + art.ID.String(),
			ScoreLabel:    musicRecommendationScoreLabel(mode, item.FinalScore),
			PlayCount:     artistPlayCounts[art.ID],
			BookmarkCount: artistBookmarkCounts[art.ID],
			BirthYear:     art.BirthYear,
			BirthDate:     art.BirthDate,
		})
	}

	total := int64(len(items))
	start := (page - 1) * pageSize
	if start > len(items) {
		start = len(items)
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], total, nil
}

func normalizeMusicAlbumFreshness(createdAt time.Time, horizon time.Duration) float64 {
	if createdAt.IsZero() || horizon <= 0 {
		return 0
	}
	age := time.Since(createdAt)
	if age <= 0 {
		return 1
	}
	return clampMusicRecommendation(1 - float64(age)/float64(horizon))
}

func normalizeMusicDiscoverQuality(hotScore float64) float64 {
	return clampMusicRecommendation(0.3 + hotScore/10)
}

func clampMusicRecommendation(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func musicRecommendationLabel(mode recommendation.Mode) string {
	switch mode {
	case recommendation.ModeHot:
		return "热度"
	case recommendation.ModeFeatured:
		return "精选"
	case recommendation.ModeDiscover:
		return "探索"
	case recommendation.ModeLatest:
		return "最新"
	default:
		return "推荐"
	}
}

func musicRecommendationScoreLabel(mode recommendation.Mode, score float64) string {
	if mode == recommendation.ModeLatest {
		return musicRecommendationLabel(mode)
	}
	return fmt.Sprintf("%s %.0f", musicRecommendationLabel(mode), math.Round(score*100))
}
