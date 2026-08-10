package music

import (
	"context"
	"path"
	"time"

	"atoman/internal/model"
	"atoman/internal/service"
	"atoman/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func RunSongAudioReplacementOnce(ctx context.Context, db *gorm.DB, workerID string) (bool, error) {
	return runSongAudioReplacementOnce(ctx, db, workerID, nil)
}

func runSongAudioReplacementOnce(ctx context.Context, db *gorm.DB, workerID string, mediaService *Service) (bool, error) {
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
	oldObjectKey := ""
	newObjectKey := ""
	if mediaService != nil {
		var song model.Song
		if err := db.WithContext(ctx).First(&song, "id = ?", candidate.SongID).Error; err != nil {
			return failSongAudioReplacement(db, candidate.ID, err)
		}
		destinationKey := storage.BuildMusicSongAudioVersionKey(song.ID.String(), uuid.NewString(), path.Ext(candidate.AudioURL))
		if song.AlbumID != nil {
			destinationKey = storage.BuildMusicAlbumTrackVersionKey(song.AlbumID.String(), song.ID.String(), uuid.NewString(), path.Ext(candidate.AudioURL))
		}
		asset, err := storage.PromoteMusicUploadAsset(mediaService.s3, candidate.AudioURL, destinationKey)
		if err != nil {
			return failSongAudioReplacement(db, candidate.ID, err)
		}
		if asset.DestinationKey != "" {
			candidate.AudioURL = asset.URL
			oldObjectKey, newObjectKey = asset.SourceKey, asset.DestinationKey
			if err := db.WithContext(ctx).Model(&model.SongAudioReplacement{}).Where("id = ?", candidate.ID).Update("audio_url", asset.URL).Error; err != nil {
				storage.DeleteMusicObjects(mediaService.s3, []string{newObjectKey})
				return failSongAudioReplacement(db, candidate.ID, err)
			}
		}
	}
	if err := service.NewRevisionService(db).ApplySongAudioReplacement(candidate.ID); err != nil {
		if mediaService != nil {
			storage.DeleteMusicObjects(mediaService.s3, []string{newObjectKey})
		}
		db.WithContext(ctx).Model(&model.SongAudioReplacement{}).Where("id = ?", candidate.ID).
			Updates(map[string]any{"status": "failed", "error_message": err.Error(), "finished_at": time.Now().UTC()})
		return true, err
	}
	if mediaService != nil {
		storage.DeleteMusicObjects(mediaService.s3, []string{oldObjectKey})
	}
	return true, nil
}

func failSongAudioReplacement(db *gorm.DB, jobID uuid.UUID, err error) (bool, error) {
	db.Model(&model.SongAudioReplacement{}).Where("id = ?", jobID).
		Updates(map[string]any{"status": "failed", "error_message": err.Error(), "finished_at": time.Now().UTC()})
	return true, err
}
