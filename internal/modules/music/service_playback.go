package music

import (
	"encoding/json"
	"errors"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) RecordSongPlay(userID *uuid.UUID, songID uuid.UUID) error {
	if songID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "song_id is required")
	}

	result := s.db.Model(&model.Song{}).
		Where("id = ? AND lifecycle_status = ? AND audio_url <> ?", songID, model.MusicLifecycleActive, "")
	var count int64
	if err := result.Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return apperr.NotFound("music.song_not_found", "Song not found")
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		repo := NewRepo(tx)
		if err := repo.IncrementSongPlayCount(songID); err != nil {
			return err
		}
		if userID == nil || *userID == uuid.Nil {
			return nil
		}
		return repo.RecordListeningHistory(*userID, songID, time.Now())
	})
}

func normalizePlaybackReportedAt(reportedAt time.Time) (time.Time, error) {
	now := time.Now().UTC()
	if reportedAt.IsZero() {
		return now, nil
	}
	reportedAt = reportedAt.UTC()
	if reportedAt.After(now.Add(5 * time.Minute)) {
		return time.Time{}, apperr.BadRequest("validation.invalid_request", "reported_at cannot be in the future")
	}
	return reportedAt, nil
}

func (s *Service) SavePlaybackProgress(user authctx.CurrentUser, input SavePlaybackProgressRequest) (model.MusicPlaybackProgress, error) {
	if user.ID == uuid.Nil {
		return model.MusicPlaybackProgress{}, apperr.Unauthorized("Login required")
	}
	if input.SongID == uuid.Nil || input.PositionSeconds < 0 || input.DurationSeconds < 0 {
		return model.MusicPlaybackProgress{}, apperr.BadRequest("validation.invalid_request", "song_id and non-negative playback position are required")
	}
	reportedAt, err := normalizePlaybackReportedAt(input.ReportedAt)
	if err != nil {
		return model.MusicPlaybackProgress{}, err
	}
	if input.DurationSeconds > 0 && input.PositionSeconds > input.DurationSeconds {
		input.PositionSeconds = input.DurationSeconds
	}
	if input.DurationSeconds > 0 && input.DurationSeconds-input.PositionSeconds <= 5 {
		input.Completed = true
		input.PositionSeconds = input.DurationSeconds
	}

	var count int64
	if err := s.db.Model(&model.Song{}).
		Where("id = ? AND lifecycle_status = ? AND audio_url <> ?", input.SongID, model.MusicLifecycleActive, "").
		Count(&count).Error; err != nil {
		return model.MusicPlaybackProgress{}, err
	}
	if count == 0 {
		return model.MusicPlaybackProgress{}, apperr.NotFound("music.song_not_found", "Song not found")
	}

	var result model.MusicPlaybackProgress
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var stored model.MusicPlaybackProgress
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND song_id = ?", user.ID, input.SongID).First(&stored).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			candidate := model.MusicPlaybackProgress{UserID: user.ID, SongID: input.SongID, PositionSeconds: input.PositionSeconds, DurationSeconds: input.DurationSeconds, Completed: input.Completed, ReportedAt: reportedAt}
			create := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
			if create.Error != nil {
				return create.Error
			}
			if create.RowsAffected > 0 {
				result = candidate
				return nil
			}
			err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND song_id = ?", user.ID, input.SongID).First(&stored).Error
		}
		if err != nil {
			return err
		}
		if stored.ReportedAt.After(reportedAt) {
			result = stored
			return nil
		}
		if err := tx.Model(&stored).Updates(map[string]any{
			"position_seconds": input.PositionSeconds,
			"duration_seconds": input.DurationSeconds,
			"completed":        input.Completed,
			"reported_at":      reportedAt,
		}).Error; err != nil {
			return err
		}
		result = stored
		result.PositionSeconds = input.PositionSeconds
		result.DurationSeconds = input.DurationSeconds
		result.Completed = input.Completed
		result.ReportedAt = reportedAt
		return nil
	})
	return result, err
}

func (s *Service) SavePlaybackSession(user authctx.CurrentUser, input SavePlaybackSessionRequest) (PlaybackSessionResponse, error) {
	if user.ID == uuid.Nil {
		return PlaybackSessionResponse{}, apperr.Unauthorized("Login required")
	}
	if len(input.SongIDs) == 0 || len(input.SongIDs) > 200 || input.CurrentSongID == uuid.Nil || input.PositionSeconds < 0 {
		return PlaybackSessionResponse{}, apperr.BadRequest("validation.invalid_request", "a current song, non-negative position, and up to 200 queued songs are required")
	}
	if input.PlaybackMode != "loop" && input.PlaybackMode != "single" && input.PlaybackMode != "random" {
		return PlaybackSessionResponse{}, apperr.BadRequest("validation.invalid_request", "invalid playback_mode")
	}
	reportedAt, err := normalizePlaybackReportedAt(input.ReportedAt)
	if err != nil {
		return PlaybackSessionResponse{}, err
	}
	seen := make(map[uuid.UUID]struct{}, len(input.SongIDs))
	currentInQueue := false
	for _, songID := range input.SongIDs {
		if songID == uuid.Nil {
			return PlaybackSessionResponse{}, apperr.BadRequest("validation.invalid_request", "song_ids must be valid UUIDs")
		}
		if _, exists := seen[songID]; exists {
			return PlaybackSessionResponse{}, apperr.BadRequest("validation.invalid_request", "song_ids must not contain duplicates")
		}
		seen[songID] = struct{}{}
		currentInQueue = currentInQueue || songID == input.CurrentSongID
	}
	if !currentInQueue {
		return PlaybackSessionResponse{}, apperr.BadRequest("validation.invalid_request", "current_song_id must be in song_ids")
	}

	queue, err := s.loadPlaybackSessionQueue(input.SongIDs, &user)
	if err != nil {
		return PlaybackSessionResponse{}, err
	}
	serialized, err := json.Marshal(input.SongIDs)
	if err != nil {
		return PlaybackSessionResponse{}, err
	}
	currentSongID := input.CurrentSongID
	var stored model.MusicPlaybackSession
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", user.ID).First(&stored).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			candidate := model.MusicPlaybackSession{UserID: user.ID, CurrentSongID: &currentSongID, QueueJSON: serialized, PositionSeconds: input.PositionSeconds, PlaybackMode: input.PlaybackMode, ReportedAt: reportedAt}
			create := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
			if create.Error != nil {
				return create.Error
			}
			if create.RowsAffected > 0 {
				stored = candidate
				return nil
			}
			err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", user.ID).First(&stored).Error
		}
		if err != nil {
			return err
		}
		if stored.ReportedAt.After(reportedAt) {
			return nil
		}
		if err := tx.Model(&stored).Updates(map[string]any{
			"current_song_id":  currentSongID,
			"queue_json":       serialized,
			"position_seconds": input.PositionSeconds,
			"playback_mode":    input.PlaybackMode,
			"reported_at":      reportedAt,
		}).Error; err != nil {
			return err
		}
		stored.CurrentSongID = &currentSongID
		stored.QueueJSON = serialized
		stored.PositionSeconds = input.PositionSeconds
		stored.PlaybackMode = input.PlaybackMode
		stored.ReportedAt = reportedAt
		return nil
	})
	if txErr != nil {
		return PlaybackSessionResponse{}, txErr
	}
	if stored.ReportedAt.After(reportedAt) {
		queue, err = s.loadPlaybackSessionQueueFromStored(stored, &user)
		if err != nil {
			return PlaybackSessionResponse{}, err
		}
	}
	if stored.CurrentSongID == nil || !playbackQueueContains(queue, *stored.CurrentSongID) {
		return PlaybackSessionResponse{}, apperr.NotFound("music.song_not_found", "Song not found")
	}
	return PlaybackSessionResponse{Queue: queue, CurrentSongID: *stored.CurrentSongID, PositionSeconds: stored.PositionSeconds, PlaybackMode: stored.PlaybackMode, UpdatedAt: stored.UpdatedAt}, nil
}

func (s *Service) GetPlaybackSession(user authctx.CurrentUser) (*PlaybackSessionResponse, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	var session model.MusicPlaybackSession
	if err := s.db.Where("user_id = ?", user.ID).First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var songIDs []uuid.UUID
	if err := json.Unmarshal(session.QueueJSON, &songIDs); err != nil || len(songIDs) == 0 || session.CurrentSongID == nil {
		return nil, nil
	}
	queue, err := s.loadAvailablePlaybackSessionQueue(songIDs, &user)
	if err != nil {
		return nil, err
	}
	if len(queue) == 0 {
		return nil, apperr.NotFound("music.song_not_found", "Song not found")
	}
	if !playbackQueueContains(queue, *session.CurrentSongID) {
		return nil, nil
	}
	return &PlaybackSessionResponse{Queue: queue, CurrentSongID: *session.CurrentSongID, PositionSeconds: session.PositionSeconds, PlaybackMode: session.PlaybackMode, UpdatedAt: session.UpdatedAt}, nil
}

func (s *Service) loadPlaybackSessionQueueFromStored(session model.MusicPlaybackSession, viewer *authctx.CurrentUser) ([]model.Song, error) {
	var songIDs []uuid.UUID
	if err := json.Unmarshal(session.QueueJSON, &songIDs); err != nil || len(songIDs) == 0 {
		return nil, apperr.NotFound("music.song_not_found", "Song not found")
	}
	return s.loadAvailablePlaybackSessionQueue(songIDs, viewer)
}

func (s *Service) loadAvailablePlaybackSessionQueue(songIDs []uuid.UUID, viewer *authctx.CurrentUser) ([]model.Song, error) {
	var songs []model.Song
	if err := s.db.Preload("Album", visibleAlbumPreload(viewer)).Preload("Artists", visibleArtistPreload(viewer)).
		Where("id IN ? AND lifecycle_status = ? AND audio_url <> ?", songIDs, model.MusicLifecycleActive, "").
		Find(&songs).Error; err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]model.Song, len(songs))
	for _, song := range songs {
		byID[song.ID] = song
	}
	ordered := make([]model.Song, 0, len(songs))
	for _, songID := range songIDs {
		if song, ok := byID[songID]; ok {
			ordered = append(ordered, song)
		}
	}
	return ordered, nil
}

func (s *Service) loadPlaybackSessionQueue(songIDs []uuid.UUID, viewer *authctx.CurrentUser) ([]model.Song, error) {
	ordered, err := s.loadAvailablePlaybackSessionQueue(songIDs, viewer)
	if err != nil {
		return nil, err
	}
	if len(ordered) != len(songIDs) {
		return nil, apperr.NotFound("music.song_not_found", "Song not found")
	}
	return ordered, nil
}

func playbackQueueContains(queue []model.Song, songID uuid.UUID) bool {
	for _, song := range queue {
		if song.ID == songID {
			return true
		}
	}
	return false
}

func (s *Service) GetPlaybackProgress(user authctx.CurrentUser) (*model.MusicPlaybackProgress, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	var progress model.MusicPlaybackProgress
	err := s.db.Joins("JOIN \"Songs\" AS visible_song ON visible_song.id = music_playback_progresses.song_id AND visible_song.deleted_at IS NULL AND visible_song.lifecycle_status = ? AND visible_song.audio_url <> ?", model.MusicLifecycleActive, "").
		Preload("Song.Album", visibleAlbumPreload(&user)).Preload("Song.Artists", visibleArtistPreload(&user)).
		Where("music_playback_progresses.user_id = ? AND music_playback_progresses.completed = ?", user.ID, false).
		Order("music_playback_progresses.updated_at DESC").First(&progress).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &progress, nil
}

func (s *Service) ListListeningHistory(user authctx.CurrentUser, page, pageSize int) ([]model.MusicListeningHistory, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	return s.repo.ListListeningHistory(user.ID, page, pageSize, &user)
}

func (s *Service) ClearListeningHistory(user authctx.CurrentUser) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.repo.ClearListeningHistory(user.ID)
}
