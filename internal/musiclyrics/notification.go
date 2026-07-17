package musiclyrics

import (
	"errors"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// UpsertRebindNotification creates or refreshes the stable notification for an invalid annotation anchor.
func UpsertRebindNotification(tx *gorm.DB, actorID, songID uuid.UUID, annotation model.MusicLyricAnnotation) error {
	if actorID == annotation.CreatedBy {
		return nil
	}
	meta := model.NotificationMeta{
		"song_id": songID.String(), "annotation_id": annotation.ID.String(),
		"title": "歌词修改影响了你的注释绑定",
		"body":  "请重新选择注释对应的歌词片段。", "source_label": "歌词注释",
	}
	var existing model.Notification
	err := tx.Where("recipient_id = ? AND source_type = ? AND source_id = ? AND aggregation_key = ''", annotation.CreatedBy, "music_lyrics", annotation.ID).
		First(&existing).Error
	if err == nil {
		return tx.Model(&existing).Updates(map[string]any{
			"actor_id": actorID, "type": "collaboration.required", "meta": meta, "read_at": nil,
		}).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	notification := model.Notification{
		RecipientID: annotation.CreatedBy,
		ActorID:     &actorID,
		Type:        "collaboration.required",
		SourceType:  "music_lyrics",
		SourceID:    annotation.ID,
		Meta:        meta,
	}
	return tx.Create(&notification).Error
}
