package music

import (
	"encoding/json"
	"errors"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	if sourceType == targetType {
		if err := mergeMusicEditReferences(tx, sourceType, sourceID, targetType, targetID); err != nil {
			return err
		}
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
		if err := mergeMusicTagVotes(tx, row.ID, existing.ID); err != nil {
			return err
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func mergeMusicTagVotes(tx *gorm.DB, sourceID, targetID uuid.UUID) error {
	if !tx.Migrator().HasTable(&model.MusicTagVote{}) {
		return nil
	}
	var votes []model.MusicTagVote
	if err := tx.Where("assignment_id = ?", sourceID).Find(&votes).Error; err != nil {
		return err
	}
	for _, vote := range votes {
		var target model.MusicTagVote
		err := tx.Where("assignment_id = ? AND user_id = ?", targetID, vote.UserID).First(&target).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Model(&vote).Updates(map[string]any{"assignment_id": targetID, "updated_at": vote.UpdatedAt}).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		// 同一用户只保留最新一次投票，旧记录留在软删除历史中。
		if vote.UpdatedAt.After(target.UpdatedAt) {
			if err := tx.Model(&target).Updates(map[string]any{"vote": vote.Vote, "updated_at": vote.UpdatedAt}).Error; err != nil {
				return err
			}
		}
		if err := tx.Delete(&vote).Error; err != nil {
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
	if musicMatchStatusRank(left.Status) != musicMatchStatusRank(right.Status) {
		return musicMatchStatusRank(left.Status) > musicMatchStatusRank(right.Status)
	}
	if !left.UpdatedAt.Equal(right.UpdatedAt) {
		return left.UpdatedAt.After(right.UpdatedAt)
	}
	return musicMatchProviderRank(left.Provider) > musicMatchProviderRank(right.Provider)
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
	var targets []model.DiscussionTarget
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("(kind = ? AND resource_key = ?) OR (kind = ? AND resource_key = ?)", sourceKind, sourceID.String(), targetKind, targetID.String()).
		Order("id").Find(&targets).Error; err != nil {
		return err
	}
	var source, target *model.DiscussionTarget
	for i := range targets {
		if targets[i].Kind == sourceKind && targets[i].ResourceKey == sourceID.String() {
			source = &targets[i]
		} else {
			target = &targets[i]
		}
	}
	if source == nil {
		return nil
	}
	if target == nil {
		return tx.Model(source).Updates(map[string]any{"kind": targetKind, "resource_id": targetID, "resource_key": targetID.String()}).Error
	}
	// 根评论楼层顺延；评论 ID、回复关系、点赞和附件不变。
	var floor int
	if err := tx.Unscoped().Model(&model.CommentEntry{}).Where("target_id = ?", target.ID).Select("COALESCE(MAX(floor_number), 0)").Scan(&floor).Error; err != nil {
		return err
	}
	if err := tx.Unscoped().Model(&model.CommentEntry{}).Where("target_id = ? AND floor_number IS NOT NULL", source.ID).
		Update("floor_number", gorm.Expr("floor_number + ?", floor)).Error; err != nil {
		return err
	}
	if err := tx.Unscoped().Model(&model.CommentEntry{}).Where("target_id = ?", source.ID).Update("target_id", target.ID).Error; err != nil {
		return err
	}
	if tx.Migrator().HasTable(&model.CommentPublishRecord{}) {
		if err := tx.Model(&model.CommentPublishRecord{}).Where("target_id = ?", source.ID).Update("target_id", target.ID).Error; err != nil {
			return err
		}
	}
	var commentCount, rootCount int64
	if err := tx.Model(&model.CommentEntry{}).Where("target_id = ? AND status IN ?", target.ID, []string{"active", "auto_folded"}).Count(&commentCount).Error; err != nil {
		return err
	}
	if err := tx.Model(&model.CommentEntry{}).Where("target_id = ? AND root_id IS NULL AND status IN ?", target.ID, []string{"active", "auto_folded"}).Count(&rootCount).Error; err != nil {
		return err
	}
	if err := tx.Unscoped().Model(&model.CommentEntry{}).Where("target_id = ?", target.ID).Select("COALESCE(MAX(floor_number), 0)").Scan(&floor).Error; err != nil {
		return err
	}
	updates := map[string]any{"comment_count": commentCount, "root_count": rootCount, "next_floor": floor + 1}
	if target.PinnedCommentID == nil && source.PinnedCommentID != nil {
		updates["pinned_comment_id"] = source.PinnedCommentID
	}
	if err := tx.Model(target).Updates(updates).Error; err != nil {
		return err
	}
	return tx.Delete(source).Error
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
	// 与普通歌词编辑共用歌曲行锁，防止迁移期间产生同版本号。
	ids := []uuid.UUID{sourceID, targetID}
	if ids[0].String() > ids[1].String() {
		ids[0], ids[1] = ids[1], ids[0]
	}
	for _, id := range ids {
		if err := lockLyricsSong(tx, id); err != nil {
			return err
		}
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
		if err := tx.Model(&model.Song{}).Where("id = ?", targetID).Update("lyrics", source.Content).Error; err != nil {
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
			"updated_by":           source.UpdatedBy,
			"edit_summary":         source.EditSummary,
			"source":               source.Source,
		}).Error; err != nil {
			return err
		}
		var lines []model.MusicSongLyricLine
		if err := tx.Where("lyric_id = ?", source.ID).Find(&lines).Error; err != nil {
			return err
		}
		if _, err := replaceCurrentLyricLines(tx, target.ID, modelLinesToParsed(lines)); err != nil {
			return err
		}
		if err := tx.Model(&model.Song{}).Where("id = ?", targetID).Update("lyrics", source.Content).Error; err != nil {
			return err
		}
	}
	if err := moveSongLyricChildren(tx, source.ID, sourceID, targetID); err != nil {
		return err
	}
	// 合并后仍以目标歌词为当前内容，版本号必须高于所有迁入历史。
	if err := tx.First(&target, "id = ?", target.ID).Error; err != nil {
		return err
	}
	var maxVersion int
	if err := tx.Model(&model.MusicSongLyricVersion{}).Where("song_id = ?", targetID).Select("COALESCE(MAX(version), 0)").Scan(&maxVersion).Error; err != nil {
		return err
	}
	target.Version = maxVersion + 1
	if err := tx.Model(&target).Update("version", target.Version).Error; err != nil {
		return err
	}
	return tx.Create(&model.MusicSongLyricVersion{SongID: targetID, Version: target.Version, Content: target.Content,
		Translation: target.Translation, Language: target.TranslationLanguage, Format: target.Format, Target: "all",
		CreatedBy: target.UpdatedBy, EditSummary: "合并歌曲，保留目标歌词"}).Error
}

func moveSongLyricChildren(tx *gorm.DB, lyricID, sourceSongID, targetSongID uuid.UUID) error {
	if tx.Migrator().HasTable(&model.MusicSongLyricVersion{}) {
		var versions []model.MusicSongLyricVersion
		if err := tx.Where("song_id = ?", sourceSongID).Order("version ASC").Find(&versions).Error; err != nil {
			return err
		}
		var next int
		if err := tx.Model(&model.MusicSongLyricVersion{}).Where("song_id = ?", targetSongID).Select("COALESCE(MAX(version), 0)").Scan(&next).Error; err != nil {
			return err
		}
		for _, version := range versions {
			next++
			if err := tx.Model(&version).Updates(map[string]any{"song_id": targetSongID, "version": next}).Error; err != nil {
				return err
			}
		}
	}
	if !tx.Migrator().HasTable(&model.MusicLyricAnnotation{}) {
		return nil
	}
	if err := tx.Model(&model.MusicLyricAnnotation{}).Where("song_id = ?", sourceSongID).Update("song_id", targetSongID).Error; err != nil {
		return err
	}
	var target model.MusicSongLyric
	if err := tx.First(&target, "song_id = ?", targetSongID).Error; err != nil {
		return err
	}
	var lines []model.MusicSongLyricLine
	if err := tx.Where("lyric_id = ?", target.ID).Order("line_index ASC").Find(&lines).Error; err != nil {
		return err
	}
	// 不猜测跨歌词行的对应关系；无法验证的锚点交给原有待重绑定流程。
	return resolveInvalidAnnotationAnchors(tx, uuid.Nil, targetSongID, lines, nil, true)
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
				if progress.ReportedAt.After(target.ReportedAt) {
					target.PositionSeconds = progress.PositionSeconds
					target.DurationSeconds = progress.DurationSeconds
					target.Completed = progress.Completed
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
