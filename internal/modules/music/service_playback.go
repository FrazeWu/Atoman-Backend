package music

import (
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) RecordSongPlay(userID *uuid.UUID, songID uuid.UUID) error {
	if songID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "song_id is required")
	}

	result := s.db.Model(&model.Song{}).Where("id = ?", songID)
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

func (s *Service) ListListeningHistory(user authctx.CurrentUser, page, pageSize int) ([]model.MusicListeningHistory, int64, error) {
	if user.ID == uuid.Nil {
		return nil, 0, apperr.Unauthorized("Login required")
	}
	return s.repo.ListListeningHistory(user.ID, page, pageSize)
}
