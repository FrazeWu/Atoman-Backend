package service

import (
	"errors"
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
		var activeJob model.VideoProcessingJob
		if err := tx.Where("video_id = ? AND job_type = ? AND status IN ?", video.ID, "thumbnail_preview", []string{"pending", "processing"}).First(&activeJob).Error; err == nil {
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
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
	// "local" is the legacy name for first-party object storage (R2/S3).
	// The URL can therefore be either an R2 public URL or an old local path.
	return video.StorageType == "local" && strings.TrimSpace(video.VideoURL) != ""
}

// EnqueueMissingVideoPreviewJobs gives existing first-party videos a safe path
// into the R2 preview pipeline after the worker is upgraded.
func EnqueueMissingVideoPreviewJobs(db *gorm.DB, limit int) error {
	if limit <= 0 {
		limit = 20
	}
	videos, err := contentmodule.LoadVideos(db, contentmodule.VideoQuery(db).
		Where("videos.storage_type = ? AND videos.video_url <> ? AND videos.processing_status = ?", "local", "", "none").
		Limit(limit))
	if err != nil {
		return err
	}
	for index := range videos {
		if err := EnsureVideoPreviewJob(db, &videos[index]); err != nil {
			return err
		}
	}
	return nil
}
