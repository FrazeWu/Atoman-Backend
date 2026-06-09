package service

import (
	"atoman/internal/model"

	"gorm.io/gorm"
)

func EnsureVideoPreviewJob(db *gorm.DB, video *model.Video) error {
	if video.StorageType != "local" {
		return db.Model(video).Updates(map[string]interface{}{
			"processing_status": "none",
			"processing_error":  "",
		}).Error
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(video).Updates(map[string]interface{}{
			"processing_status":  "pending",
			"processing_error":   "",
			"preview_thumbnails": nil,
		}).Error; err != nil {
			return err
		}

		job := model.VideoProcessingJob{
			VideoID: video.ID,
			Status:  "pending",
			JobType: "thumbnail_preview",
		}
		return tx.Create(&job).Error
	})
}
