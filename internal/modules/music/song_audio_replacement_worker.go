package music

import (
	"context"
	"log"
	"path"
	"time"

	"atoman/internal/model"
	"atoman/internal/service"
	"atoman/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const songAudioReplacementLease = 15 * time.Minute

func RunSongAudioReplacementOnce(ctx context.Context, db *gorm.DB, workerID string) (bool, error) {
	return runSongAudioReplacementOnce(ctx, db, workerID, nil)
}

func runSongAudioReplacementOnce(ctx context.Context, db *gorm.DB, workerID string, mediaService *Service) (bool, error) {
	if db == nil || !db.Migrator().HasTable(&model.SongAudioReplacement{}) {
		return false, nil
	}
	var candidate model.SongAudioReplacement
	now := time.Now().UTC()
	staleBefore := now.Add(-songAudioReplacementLease)
	claimable := "(status = ? OR (status = ? AND (started_at IS NULL OR started_at < ?)))"
	if err := db.WithContext(ctx).Where(claimable, "pending", "processing", staleBefore).Order("created_at ASC").First(&candidate).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, err
	}
	// 每次领取都使用新令牌，即使同一个 Worker 再次领取也不能复用旧租约。
	leaseToken := workerID + ":" + uuid.NewString()
	result := db.WithContext(ctx).Model(&model.SongAudioReplacement{}).
		Where("id = ? AND "+claimable, candidate.ID, "pending", "processing", staleBefore).
		Updates(map[string]any{"status": "processing", "locked_by": leaseToken, "started_at": now})
	if result.Error != nil || result.RowsAffected == 0 {
		return false, result.Error
	}
	oldObjectKey := ""
	newObjectKey := ""
	if mediaService != nil {
		var song model.Song
		if err := db.WithContext(ctx).First(&song, "id = ?", candidate.SongID).Error; err != nil {
			return failSongAudioReplacement(db, candidate.ID, leaseToken, err)
		}
		destinationKey := storage.BuildMusicSongAudioVersionKey(song.ID.String(), uuid.NewString(), path.Ext(candidate.AudioURL))
		if song.AlbumID != nil {
			destinationKey = storage.BuildMusicAlbumTrackVersionKey(song.AlbumID.String(), song.ID.String(), uuid.NewString(), path.Ext(candidate.AudioURL))
		}
		asset, err := storage.PromoteMusicUploadAsset(db.WithContext(ctx), mediaService.s3, candidate.RequestedBy, candidate.AudioURL, destinationKey)
		if err != nil {
			return failSongAudioReplacement(db, candidate.ID, leaseToken, err)
		}
		if asset.DestinationKey != "" {
			candidate.AudioURL = asset.URL
			oldObjectKey, newObjectKey = asset.SourceKey, asset.DestinationKey
		}
	}
	// 发布 URL 与音频切换一同提交；重新领取的 Worker 不会误用旧 Worker 待清理的文件。
	if err := service.NewRevisionService(db.WithContext(ctx)).ApplySongAudioReplacement(candidate.ID, leaseToken, candidate.AudioURL); err != nil {
		if mediaService != nil {
			storage.DeleteMusicObjects(mediaService.s3, []string{newObjectKey})
		}
		return failSongAudioReplacement(db, candidate.ID, leaseToken, err)
	}
	if mediaService != nil {
		deleteSource := candidate.AssetID == nil
		if candidate.AssetID != nil {
			result := db.Where("id = ? AND user_id = ? AND purpose = ?", *candidate.AssetID, candidate.RequestedBy, "music.audio").Delete(&model.MediaAsset{})
			if result.Error != nil {
				log.Printf("consume song audio replacement asset %s: %v", candidate.AssetID.String(), result.Error)
			} else {
				deleteSource = true
			}
		}
		if deleteSource {
			storage.DeleteMusicObjects(mediaService.s3, []string{oldObjectKey})
		}
	}
	return true, nil
}

func failSongAudioReplacement(db *gorm.DB, jobID uuid.UUID, leaseToken string, err error) (bool, error) {
	db.Model(&model.SongAudioReplacement{}).Where("id = ? AND status = ? AND locked_by = ?", jobID, "processing", leaseToken).
		Updates(map[string]any{"status": "failed", "error_message": err.Error(), "finished_at": time.Now().UTC()})
	return true, err
}
