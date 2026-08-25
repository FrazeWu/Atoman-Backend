package music

import (
	"errors"
	"math"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SongRatingSummary struct {
	RatingScore  float64 `json:"rating_score"`
	RatingCount  int64   `json:"rating_count"`
	ViewerRating *int    `json:"viewer_rating,omitempty"`
}

type AlbumRatingSummary struct {
	RatingScore  float64 `json:"rating_score"`
	RatingCount  int64   `json:"rating_count"`
	ViewerRating *int    `json:"viewer_rating,omitempty"`
}

func (s *Service) SetSongRating(user authctx.CurrentUser, songID uuid.UUID, score int) (SongRatingSummary, error) {
	if user.ID == uuid.Nil {
		return SongRatingSummary{}, apperr.Unauthorized("Login required")
	}
	if score < 1 || score > 5 {
		return SongRatingSummary{}, apperr.BadRequest("validation.invalid_request", "score must be between 1 and 5")
	}
	if err := s.ensureSongRatingAccess(user, songID); err != nil {
		return SongRatingSummary{}, err
	}

	rating := model.SongRating{UserID: user.ID, SongID: songID, Score: score}
	if err := s.db.Clauses(ratingUpsertConflict("user_id", "song_id", score)).Create(&rating).Error; err != nil {
		return SongRatingSummary{}, err
	}
	return s.SongRatingSummary(songID, &user.ID)
}

func ratingUpsertConflict(ownerColumn, targetColumn string, score int) clause.OnConflict {
	return clause.OnConflict{
		Columns: []clause.Column{{Name: ownerColumn}, {Name: targetColumn}},
		TargetWhere: clause.Where{Exprs: []clause.Expression{
			clause.Expr{SQL: "deleted_at IS NULL"},
		}},
		DoUpdates: clause.Assignments(map[string]any{
			"score":      score,
			"updated_at": time.Now(),
		}),
	}
}

func (s *Service) DeleteSongRating(user authctx.CurrentUser, songID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if err := s.ensureSongRatingAccess(user, songID); err != nil {
		return err
	}
	return s.db.Where("user_id = ? AND song_id = ?", user.ID, songID).Delete(&model.SongRating{}).Error
}

func (s *Service) SongRatingSummary(songID uuid.UUID, viewerID *uuid.UUID) (SongRatingSummary, error) {
	var aggregate struct {
		RatingScore float64 `gorm:"column:rating_score"`
		RatingCount int64   `gorm:"column:rating_count"`
	}
	if err := s.db.Model(&model.SongRating{}).
		Select("COALESCE(AVG(score), 0) AS rating_score, COUNT(*) AS rating_count").
		Where("song_id = ?", songID).
		Scan(&aggregate).Error; err != nil {
		return SongRatingSummary{}, err
	}
	summary := SongRatingSummary{
		RatingScore: math.Round(aggregate.RatingScore*10) / 10,
		RatingCount: aggregate.RatingCount,
	}
	if viewerID != nil {
		var rating model.SongRating
		if err := s.db.Where("user_id = ? AND song_id = ?", *viewerID, songID).First(&rating).Error; err == nil {
			score := rating.Score
			summary.ViewerRating = &score
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return SongRatingSummary{}, err
		}
	}
	return summary, nil
}

func (s *Service) PopulateSongRatings(songs []model.Song, viewerID *uuid.UUID) error {
	return s.populateSongRatings(songs, viewerID)
}

func (s *Service) populateSongRatings(songs []model.Song, viewerID *uuid.UUID) error {
	if len(songs) == 0 || !s.db.Migrator().HasTable(&model.SongRating{}) {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(songs))
	for _, song := range songs {
		if song.ID != uuid.Nil {
			ids = append(ids, song.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}

	type aggregateRow struct {
		SongID      uuid.UUID `gorm:"column:song_id"`
		RatingScore float64   `gorm:"column:rating_score"`
		RatingCount int64     `gorm:"column:rating_count"`
	}
	var aggregates []aggregateRow
	if err := s.db.Model(&model.SongRating{}).
		Select("song_id, COALESCE(AVG(score), 0) AS rating_score, COUNT(*) AS rating_count").
		Where("song_id IN ?", ids).
		Group("song_id").
		Scan(&aggregates).Error; err != nil {
		return err
	}
	aggregateBySongID := make(map[uuid.UUID]aggregateRow, len(aggregates))
	for _, aggregate := range aggregates {
		aggregateBySongID[aggregate.SongID] = aggregate
	}

	viewerRatings := make(map[uuid.UUID]int)
	if viewerID != nil {
		var ratings []model.SongRating
		if err := s.db.Where("user_id = ? AND song_id IN ?", *viewerID, ids).Find(&ratings).Error; err != nil {
			return err
		}
		for _, rating := range ratings {
			viewerRatings[rating.SongID] = rating.Score
		}
	}

	for index := range songs {
		if aggregate, ok := aggregateBySongID[songs[index].ID]; ok {
			songs[index].RatingScore = math.Round(aggregate.RatingScore*10) / 10
			songs[index].RatingCount = aggregate.RatingCount
		}
		if score, ok := viewerRatings[songs[index].ID]; ok {
			songs[index].ViewerRating = &score
		}
	}
	return nil
}

func (s *Service) ensureSongRatingAccess(user authctx.CurrentUser, songID uuid.UUID) error {
	if songID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "song_id is required")
	}
	var song model.Song
	query := scopeVisibleMusicEntries(s.db, `"Songs"`, "uploaded_by", &user, false)
	if err := query.First(&song, `"Songs".id = ?`, songID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("music.song_not_found", "Song not found")
		}
		return err
	}
	return nil
}

func (s *Service) SetAlbumRating(user authctx.CurrentUser, albumID uuid.UUID, score int) (AlbumRatingSummary, error) {
	if user.ID == uuid.Nil {
		return AlbumRatingSummary{}, apperr.Unauthorized("Login required")
	}
	if score < 1 || score > 5 {
		return AlbumRatingSummary{}, apperr.BadRequest("validation.invalid_request", "score must be between 1 and 5")
	}
	if err := s.ensureAlbumRatingAccess(user, albumID); err != nil {
		return AlbumRatingSummary{}, err
	}

	rating := model.AlbumRating{UserID: user.ID, AlbumID: albumID, Score: score}
	if err := s.db.Clauses(ratingUpsertConflict("user_id", "album_id", score)).Create(&rating).Error; err != nil {
		return AlbumRatingSummary{}, err
	}
	return s.AlbumRatingSummary(albumID, &user.ID)
}

func (s *Service) DeleteAlbumRating(user authctx.CurrentUser, albumID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if err := s.ensureAlbumRatingAccess(user, albumID); err != nil {
		return err
	}
	return s.db.Where("user_id = ? AND album_id = ?", user.ID, albumID).Delete(&model.AlbumRating{}).Error
}

func (s *Service) AlbumRatingSummary(albumID uuid.UUID, viewerID *uuid.UUID) (AlbumRatingSummary, error) {
	var aggregate struct {
		RatingScore float64 `gorm:"column:rating_score"`
		RatingCount int64   `gorm:"column:rating_count"`
	}
	if err := s.db.Model(&model.AlbumRating{}).
		Select("COALESCE(AVG(score), 0) AS rating_score, COUNT(*) AS rating_count").
		Where("album_id = ?", albumID).
		Scan(&aggregate).Error; err != nil {
		return AlbumRatingSummary{}, err
	}
	summary := AlbumRatingSummary{
		RatingScore: math.Round(aggregate.RatingScore*10) / 10,
		RatingCount: aggregate.RatingCount,
	}
	if viewerID != nil {
		var rating model.AlbumRating
		if err := s.db.Where("user_id = ? AND album_id = ?", *viewerID, albumID).First(&rating).Error; err == nil {
			score := rating.Score
			summary.ViewerRating = &score
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return AlbumRatingSummary{}, err
		}
	}
	return summary, nil
}

func (s *Service) PopulateAlbumRatings(albums []model.Album, viewerID *uuid.UUID) error {
	if len(albums) == 0 || !s.db.Migrator().HasTable(&model.AlbumRating{}) {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(albums))
	for _, album := range albums {
		if album.ID != uuid.Nil {
			ids = append(ids, album.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}

	type aggregateRow struct {
		AlbumID     uuid.UUID `gorm:"column:album_id"`
		RatingScore float64   `gorm:"column:rating_score"`
		RatingCount int64     `gorm:"column:rating_count"`
	}
	var aggregates []aggregateRow
	if err := s.db.Model(&model.AlbumRating{}).
		Select("album_id, COALESCE(AVG(score), 0) AS rating_score, COUNT(*) AS rating_count").
		Where("album_id IN ?", ids).
		Group("album_id").
		Scan(&aggregates).Error; err != nil {
		return err
	}
	aggregateByAlbumID := make(map[uuid.UUID]aggregateRow, len(aggregates))
	for _, aggregate := range aggregates {
		aggregateByAlbumID[aggregate.AlbumID] = aggregate
	}

	viewerRatings := make(map[uuid.UUID]int)
	if viewerID != nil {
		var ratings []model.AlbumRating
		if err := s.db.Where("user_id = ? AND album_id IN ?", *viewerID, ids).Find(&ratings).Error; err != nil {
			return err
		}
		for _, rating := range ratings {
			viewerRatings[rating.AlbumID] = rating.Score
		}
	}
	for index := range albums {
		if aggregate, ok := aggregateByAlbumID[albums[index].ID]; ok {
			albums[index].RatingScore = math.Round(aggregate.RatingScore*10) / 10
			albums[index].RatingCount = aggregate.RatingCount
		}
		if score, ok := viewerRatings[albums[index].ID]; ok {
			albums[index].ViewerRating = &score
		}
	}
	return nil
}

func (s *Service) ensureAlbumRatingAccess(user authctx.CurrentUser, albumID uuid.UUID) error {
	if albumID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "album_id is required")
	}
	var album model.Album
	query := scopeVisibleMusicEntries(s.db, `"Albums"`, "uploaded_by", &user, false)
	if err := query.First(&album, `"Albums".id = ?`, albumID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("music.album_not_found", "Album not found")
		}
		return err
	}
	return nil
}
