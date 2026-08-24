package service

import (
	"strings"

	"atoman/internal/model"
	contentmodule "atoman/internal/modules/content"

	"gorm.io/gorm"
)

func EnsureVideoPreviewJob(db *gorm.DB, video *model.Video) error {
	contentID, err := contentmodule.VideoContentID(db, video.ID)
	if err != nil {
		return err
	}
	if !needsVideoPreviewJob(*video) {
		return db.Model(&model.ContentVideoExtension{}).Where("content_id = ?", contentID).Updates(map[string]interface{}{
			"processing_status": "none",
			"processing_error":  "",
		}).Error
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.ContentVideoExtension{}).Where("content_id = ?", contentID).Updates(map[string]interface{}{
			"processing_status":  "pending",
			"processing_error":   "",
			"preview_thumbnails": nil,
		}).Error; err != nil {
			return err
		}

		job := model.VideoProcessingJob{
			ContentID: contentID,
			VideoID:   video.ID,
			Status:    "pending",
			JobType:   "thumbnail_preview",
		}
		return tx.Create(&job).Error
	})
}

func needsVideoPreviewJob(video model.Video) bool {
	return video.StorageType == "local" && strings.HasPrefix(video.VideoURL, "/uploads/")
}
