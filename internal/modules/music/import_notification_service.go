package music

import (
	"errors"
	"log"

	"atoman/internal/model"

	"gorm.io/gorm"
)

const musicImportNotificationType = "music_import"

func (s *Service) updateAlbumImportNotification(session model.AlbumImportSession) {
	if session.UserID == nil {
		return
	}
	title := albumImportNotificationTitle(session)
	notification := model.Notification{
		RecipientID: *session.UserID,
		Type:        musicImportNotificationType,
		SourceType:  "music_album_import",
		SourceID:    session.ID,
		Meta: model.NotificationMeta{
			"title":        title,
			"body":         albumImportNotificationBody(session.Status),
			"source_label": "音乐导入",
		},
	}

	var existing model.Notification
	err := s.db.Where("recipient_id = ? AND source_type = ? AND source_id = ?", notification.RecipientID, notification.SourceType, notification.SourceID).First(&existing).Error
	if err == nil {
		if err := s.db.Model(&existing).Updates(map[string]any{"type": notification.Type, "meta": notification.Meta}).Error; err != nil {
			log.Printf("update music import notification: %v", err)
		}
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		log.Printf("load music import notification: %v", err)
		return
	}
	if err := s.db.Create(&notification).Error; err != nil {
		log.Printf("create music import notification: %v", err)
	}
}

func albumImportNotificationTitle(session model.AlbumImportSession) string {
	title := ""
	payload, err := readAlbumImportPayloadMap(session.PayloadJSON)
	if err == nil {
		title = albumImportSessionAlbumTitle(session, payload)
	}
	if title == "" {
		title = "未命名专辑"
	}
	switch session.Status {
	case AlbumImportStatusCommitted:
		return "已发布：" + title
	case AlbumImportStatusCanceled:
		return "导入已取消：" + title
	case AlbumImportStatusNeedsAttention, AlbumImportStatusFailed:
		return "导入需要处理：" + title
	default:
		return "正在导入：" + title
	}
}

func albumImportNotificationBody(status string) string {
	switch status {
	case AlbumImportStatusQueued:
		return "等待处理"
	case AlbumImportStatusExtracting:
		return "正在解压"
	case AlbumImportStatusAnalyzing:
		return "正在识别"
	case AlbumImportStatusTranscoding:
		return "正在转码"
	case AlbumImportStatusCommitted:
		return "已发布到音乐库"
	case AlbumImportStatusCanceled:
		return "导入已取消"
	case AlbumImportStatusNeedsAttention, AlbumImportStatusFailed:
		return "请前往导入中心处理"
	default:
		return "正在上传"
	}
}
