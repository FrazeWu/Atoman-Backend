package music

import (
	"context"
	"time"

	"atoman/internal/model"
	"atoman/internal/service"

	"gorm.io/gorm"
)

func RunSongAudioReplacementOnce(ctx context.Context, db *gorm.DB, workerID string) (bool, error) {
	if db == nil || !db.Migrator().HasTable(&model.SongAudioReplacement{}) {
		return false, nil
	}
	var candidate model.SongAudioReplacement
	if err := db.WithContext(ctx).Where("status = ?", "pending").Order("created_at ASC").First(&candidate).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, err
	}
	now := time.Now().UTC()
	result := db.WithContext(ctx).Model(&model.SongAudioReplacement{}).
		Where("id = ? AND status = ?", candidate.ID, "pending").
		Updates(map[string]any{"status": "processing", "locked_by": workerID, "started_at": now})
	if result.Error != nil || result.RowsAffected == 0 {
		return false, result.Error
	}
	if err := service.NewRevisionService(db).ApplySongAudioReplacement(candidate.ID); err != nil {
		db.WithContext(ctx).Model(&model.SongAudioReplacement{}).Where("id = ?", candidate.ID).
			Updates(map[string]any{"status": "failed", "error_message": err.Error(), "finished_at": time.Now().UTC()})
		return true, err
	}
	return true, nil
}
