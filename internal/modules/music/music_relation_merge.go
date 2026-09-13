package music

import (
	"encoding/json"
	"errors"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func mergeMusicEntityRelations(tx *gorm.DB, entityType string, sourceID, targetID uuid.UUID) error {
	if err := mergeMusicEntityRelationsBetween(tx, entityType, sourceID, entityType, targetID); err != nil {
		return err
	}
	switch entityType {
	case "album":
		if err := mergeAlbumRatings(tx, sourceID, targetID); err != nil {
			return err
		}
		return moveMusicEntityCorrections(tx, entityType, sourceID, targetID)
	case "song":
		if err := mergeSongRatings(tx, sourceID, targetID); err != nil {
			return err
		}
		if err := mergeSongLyrics(tx, sourceID, targetID); err != nil {
			return err
		}
		if err := mergeSongPlayback(tx, sourceID, targetID); err != nil {
			return err
		}
		return moveMusicEntityCorrections(tx, entityType, sourceID, targetID)
	case "artist":
		return moveMusicEntityCorrections(tx, entityType, sourceID, targetID)
	default:
		return nil
	}
}

func mergeMusicEntityRelationsBetween(tx *gorm.DB, sourceType string, sourceID uuid.UUID, targetType string, targetID uuid.UUID) error {
	if err := mergeMusicTagAssignments(tx, sourceType, sourceID, targetType, targetID); err != nil {
		return err
	}
	if err := mergeMusicMatchRecords(tx, sourceType, sourceID, targetType, targetID); err != nil {
		return err
	}
	if err := mergeMusicCatalogLinks(tx, sourceType, sourceID, targetType, targetID); err != nil {
		return err
	}
	if err := mergeMusicEditReferences(tx, sourceType, sourceID, targetType, targetID); err != nil {
		return err
	}
	if err := mergeMusicDiscussionTargets(tx, sourceType, sourceID, targetType, targetID); err != nil {
		return err
	}
	return mergeMusicProtection(tx, sourceType, sourceID, targetType, targetID)
}

func mergeMusicTagAssignments(tx *gorm.DB, sourceType string, sourceID uuid.UUID, targetType string, targetID uuid.UUID) error {
	if !tx.Migrator().HasTable(&model.MusicTagAssignment{}) {
		return nil
	}
	var rows []model.MusicTagAssignment
	if err := tx.Where("entity_type = ? AND entity_id = ?", sourceType, sourceID).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		var existing model.MusicTagAssignment
		err := tx.Where("entity_type = ? AND entity_id = ? AND tag_id = ?", targetType, targetID, row.TagID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Model(&row).Updates(map[string]any{"entity_type": targetType, "entity_id": targetID}).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func mergeMusicMatchRecords(tx *gorm.DB, sourceType string, sourceID uuid.UUID, targetType string, targetID uuid.UUID) error {
	if !tx.Migrator().HasTable(&model.MusicMatchRecord{}) {
		return nil
	}
	var rows []model.MusicMatchRecord
	if err := tx.Where("entity_type = ? AND entity_id = ?", sourceType, sourceID).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		var existing model.MusicMatchRecord
		err := tx.Where("entity_type = ? AND entity_id = ? AND provider = ?", targetType, targetID, row.Provider).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Model(&row).Updates(map[string]any{"entity_type": targetType, "entity_id": targetID}).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if musicMatchRecordPreferred(row, existing) {
			if err := tx.Model(&existing).Updates(map[string]any{
				"external_id":     row.ExternalID,
				"source_url":      row.SourceURL,
				"status":          row.Status,
				"confidence":      row.Confidence,
				"matched_at":      row.MatchedAt,
				"user_overridden": row.UserOverridden,
				"metadata_json":   row.MetadataJSON,
			}).Error; err != nil {
				return err
			}
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func musicMatchRecordPreferred(left, right model.MusicMatchRecord) bool {
	if left.UserOverridden != right.UserOverridden {
		return left.UserOverridden
	}
	return left.UpdatedAt.After(right.UpdatedAt)
}

func mergeMusicCatalogLinks(tx *gorm.DB, sourceType string, sourceID uuid.UUID, targetType string, targetID uuid.UUID) error {
	if !tx.Migrator().HasTable(&model.MusicCatalogLink{}) {
		return nil
	}
	var rows []model.MusicCatalogLink
	if err := tx.Where("entity_type = ? AND entity_id = ?", sourceType, sourceID).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		var existing model.MusicCatalogLink
		err := tx.Where("provider = ? AND entity_type = ? AND external_id = ?", row.Provider, targetType, row.ExternalID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Model(&row).Updates(map[string]any{"entity_type": targetType, "entity_id": targetID}).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func mergeMusicEditReferences(tx *gorm.DB, sourceType string, sourceID uuid.UUID, targetType string, targetID uuid.UUID) error {
	if tx.Migrator().HasTable(&model.MusicEdit{}) {
		if err := tx.Model(&model.MusicEdit{}).Where("entity_type = ? AND entity_id = ?", sourceType, sourceID).
			Updates(map[string]any{"entity_type": targetType, "entity_id": targetID}).Error; err != nil {
			return err
		}
	}
	if tx.Migrator().HasTable(&model.MusicEditChange{}) {
		if err := tx.Model(&model.MusicEditChange{}).Where("entity_type = ? AND entity_id = ?", sourceType, sourceID).
			Updates(map[string]any{"entity_type": targetType, "entity_id": targetID}).Error; err != nil {
			return err
		}
	}
	if tx.Migrator().HasTable(&model.EditConflict{}) {
		if err := tx.Model(&model.EditConflict{}).Where("content_type = ? AND content_id = ?", sourceType, sourceID).
			Updates(map[string]any{"content_type": targetType, "content_id": targetID}).Error; err != nil {
			return err
		}
	}
	return nil
}

func mergeMusicDiscussionTargets(tx *gorm.DB, sourceType string, sourceID uuid.UUID, targetType string, targetID uuid.UUID) error {
	if !tx.Migrator().HasTable(&model.DiscussionTarget{}) {
		return nil
	}
	sourceKind := "music_" + sourceType
	targetKind := "music_" + targetType
	return tx.Model(&model.DiscussionTarget{}).Where("kind = ? AND resource_id = ?", sourceKind, sourceID).
		Updates(map[string]any{"kind": targetKind, "resource_id": targetID, "resource_key": targetID.String()}).Error
}

func mergeMusicProtection(tx *gorm.DB, sourceType string, sourceID uuid.UUID, targetType string, targetID uuid.UUID) error {
	if !tx.Migrator().HasTable(&model.ContentProtection{}) {
		return nil
	}
	var source model.ContentProtection
	err := tx.Where("content_type = ? AND content_id = ?", sourceType, sourceID).First(&source).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var target model.ContentProtection
	err = tx.Where("content_type = ? AND content_id = ?", targetType, targetID).First(&target).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Model(&source).Updates(map[string]any{"content_type": targetType, "content_id": targetID}).Error
	}
	if err != nil {
		return err
	}
	return tx.Delete(&source).Error
}

func mergeAlbumRatings(tx *gorm.DB, sourceID, targetID uuid.UUID) error {
	if !tx.Migrator().HasTable(&model.AlbumRating{}) {
		return nil
	}
	var rows []model.AlbumRating
	if err := tx.Where("album_id = ?", sourceID).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		var existing model.AlbumRating
		err := tx.Where("user_id = ? AND album_id = ?", row.UserID, targetID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Model(&row).Update("album_id", targetID).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func mergeSongRatings(tx *gorm.DB, sourceID, targetID uuid.UUID) error {
	if !tx.Migrator().HasTable(&model.SongRating{}) {
		return nil
	}
	var rows []model.SongRating
	if err := tx.Where("song_id = ?", sourceID).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		var existing model.SongRating
		err := tx.Where("user_id = ? AND song_id = ?", row.UserID, targetID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Model(&row).Update("song_id", targetID).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func moveSongRatingsToAlbum(tx *gorm.DB, songID, albumID uuid.UUID) error {
	if !tx.Migrator().HasTable(&model.SongRating{}) || !tx.Migrator().HasTable(&model.AlbumRating{}) {
		return nil
	}
	var rows []model.SongRating
	if err := tx.Where("song_id = ?", songID).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		var existing model.AlbumRating
		err := tx.Where("user_id = ? AND album_id = ?", row.UserID, albumID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Create(&model.AlbumRating{Base: model.Base{ID: uuid.New()}, UserID: row.UserID, AlbumID: albumID, Score: row.Score}).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func moveAlbumRatingsToSong(tx *gorm.DB, albumID, songID uuid.UUID) error {
	if !tx.Migrator().HasTable(&model.AlbumRating{}) || !tx.Migrator().HasTable(&model.SongRating{}) {
		return nil
	}
	var rows []model.AlbumRating
	if err := tx.Where("album_id = ?", albumID).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		var existing model.SongRating
		err := tx.Where("user_id = ? AND song_id = ?", row.UserID, songID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Create(&model.SongRating{Base: model.Base{ID: uuid.New()}, UserID: row.UserID, SongID: songID, Score: row.Score}).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func mergeSongLyrics(tx *gorm.DB, sourceID, targetID uuid.UUID) error {
	if !tx.Migrator().HasTable(&model.MusicSongLyric{}) {
		return nil
	}
	var source model.MusicSongLyric
	err := tx.Where("song_id = ?", sourceID).First(&source).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var target model.MusicSongLyric
	err = tx.Where("song_id = ?", targetID).First(&target).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := tx.Model(&source).Update("song_id", targetID).Error; err != nil {
			return err
		}
		if err := moveSongLyricChildren(tx, source.ID, sourceID, targetID); err != nil {
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}
	if target.Content == "" && source.Content != "" {
		if err := tx.Model(&target).Updates(map[string]any{
			"content":              source.Content,
			"translation":          source.Translation,
			"translation_language": source.TranslationLanguage,
			"format":               source.Format,
			"version":              source.Version,
			"updated_by":           source.UpdatedBy,
			"edit_summary":         source.EditSummary,
			"source":               source.Source,
		}).Error; err != nil {
			return err
		}
	}
	return moveSongLyricChildren(tx, source.ID, sourceID, targetID)
}

func moveSongLyricChildren(tx *gorm.DB, lyricID, sourceSongID, targetSongID uuid.UUID) error {
	if tx.Migrator().HasTable(&model.MusicSongLyricVersion{}) {
		var versions []model.MusicSongLyricVersion
		if err := tx.Where("song_id = ?", sourceSongID).Find(&versions).Error; err != nil {
			return err
		}
		for _, version := range versions {
			var existing model.MusicSongLyricVersion
			err := tx.Where("song_id = ? AND version = ?", targetSongID, version.Version).First(&existing).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if err := tx.Model(&version).Update("song_id", targetSongID).Error; err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
		}
	}
	if !tx.Migrator().HasTable(&model.MusicLyricAnnotation{}) {
		return nil
	}
	return tx.Model(&model.MusicLyricAnnotation{}).Where("song_id = ?", sourceSongID).Update("song_id", targetSongID).Error
}

func mergeSongPlayback(tx *gorm.DB, sourceID, targetID uuid.UUID) error {
	if tx.Migrator().HasTable(&model.MusicPlaybackProgress{}) {
		var progresses []model.MusicPlaybackProgress
		if err := tx.Where("song_id = ?", sourceID).Find(&progresses).Error; err != nil {
			return err
		}
		for _, progress := range progresses {
			var target model.MusicPlaybackProgress
			err := tx.Where("user_id = ? AND song_id = ?", progress.UserID, targetID).First(&target).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if err := tx.Model(&progress).Update("song_id", targetID).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			} else {
				if progress.PositionSeconds > target.PositionSeconds {
					target.PositionSeconds = progress.PositionSeconds
				}
				if progress.DurationSeconds > target.DurationSeconds {
					target.DurationSeconds = progress.DurationSeconds
				}
				target.Completed = target.Completed || progress.Completed
				if progress.ReportedAt.After(target.ReportedAt) {
					target.ReportedAt = progress.ReportedAt
				}
				if err := tx.Save(&target).Error; err != nil {
					return err
				}
				if err := tx.Delete(&progress).Error; err != nil {
					return err
				}
			}
		}
	}
	if !tx.Migrator().HasTable(&model.MusicPlaybackSession{}) {
		return nil
	}
	var sessions []model.MusicPlaybackSession
	if err := tx.Find(&sessions).Error; err != nil {
		return err
	}
	for _, session := range sessions {
		var songIDs []uuid.UUID
		if err := json.Unmarshal(session.QueueJSON, &songIDs); err != nil {
			continue
		}
		changed := false
		seen := map[uuid.UUID]bool{}
		ordered := make([]uuid.UUID, 0, len(songIDs))
		for _, songID := range songIDs {
			if songID == sourceID {
				songID = targetID
				changed = true
			}
			if seen[songID] {
				changed = true
				continue
			}
			seen[songID] = true
			ordered = append(ordered, songID)
		}
		currentSongID := session.CurrentSongID
		if currentSongID != nil && *currentSongID == sourceID {
			value := targetID
			currentSongID = &value
			changed = true
		}
		if !changed {
			continue
		}
		queueJSON, err := json.Marshal(ordered)
		if err != nil {
			return err
		}
		if err := tx.Model(&session).Updates(map[string]any{"queue_json": queueJSON, "current_song_id": currentSongID}).Error; err != nil {
			return err
		}
	}
	return nil
}

func moveMusicEntityCorrections(tx *gorm.DB, entityType string, sourceID, targetID uuid.UUID) error {
	var table any
	switch entityType {
	case "album":
		table = &model.AlbumCorrection{}
	case "song":
		table = &model.SongCorrection{}
	case "artist":
		table = &model.ArtistCorrection{}
	default:
		return nil
	}
	if !tx.Migrator().HasTable(table) {
		return nil
	}
	column := entityType + "_id"
	return tx.Model(table).Where(column+" = ?", sourceID).Update(column, targetID).Error
}

func removeMusicEntityRelations(tx *gorm.DB, entityType string, entityID uuid.UUID) error {
	if tx.Migrator().HasTable(&model.MusicTagAssignment{}) {
		if err := tx.Where("entity_type = ? AND entity_id = ?", entityType, entityID).Delete(&model.MusicTagAssignment{}).Error; err != nil {
			return err
		}
	}
	if tx.Migrator().HasTable(&model.MusicMatchRecord{}) {
		if err := tx.Where("entity_type = ? AND entity_id = ?", entityType, entityID).Delete(&model.MusicMatchRecord{}).Error; err != nil {
			return err
		}
	}
	if tx.Migrator().HasTable(&model.MusicCatalogLink{}) {
		if err := tx.Where("entity_type = ? AND entity_id = ?", entityType, entityID).Delete(&model.MusicCatalogLink{}).Error; err != nil {
			return err
		}
	}
	return nil
}
