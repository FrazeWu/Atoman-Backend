package music

import (
	"errors"
	"strings"
	"unicode"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/audit"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) PreviewAlbumMerge(targetAlbumID, sourceAlbumID uuid.UUID) (AlbumMergePreviewResponse, error) {
	var preview AlbumMergePreviewResponse
	if targetAlbumID == uuid.Nil || sourceAlbumID == uuid.Nil || targetAlbumID == sourceAlbumID {
		return preview, apperr.BadRequest("validation.invalid_request", "source and target albums must be different")
	}
	if err := s.db.Preload("Songs").First(&preview.TargetAlbum, "id = ?", targetAlbumID).Error; err != nil {
		return preview, albumMergeNotFound(err, "Target album not found")
	}
	if err := s.db.Preload("Songs").First(&preview.SourceAlbum, "id = ?", sourceAlbumID).Error; err != nil {
		return preview, albumMergeNotFound(err, "Source album not found")
	}
	preview.Matches = matchAlbumSongs(preview.SourceAlbum.Songs, preview.TargetAlbum.Songs)
	return preview, nil
}

func albumMergeNotFound(err error, message string) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.NotFound("music.album_not_found", message)
	}
	return err
}

func matchAlbumSongs(sourceSongs, targetSongs []model.Song) []AlbumMergeSongMatchResponse {
	usedTargets := make(map[uuid.UUID]struct{})
	matches := make([]AlbumMergeSongMatchResponse, 0)
	for _, source := range sourceSongs {
		key := normalizedSongTitle(source.Title)
		if key == "" {
			continue
		}
		best := -1
		for index, target := range targetSongs {
			if _, used := usedTargets[target.ID]; used || normalizedSongTitle(target.Title) != key {
				continue
			}
			if best < 0 {
				best = index
			}
			if source.TrackNumber == target.TrackNumber && source.DiscNumber == target.DiscNumber {
				best = index
				break
			}
		}
		if best < 0 {
			continue
		}
		usedTargets[targetSongs[best].ID] = struct{}{}
		reason := "曲名一致"
		if source.TrackNumber == targetSongs[best].TrackNumber && source.DiscNumber == targetSongs[best].DiscNumber {
			reason = "曲名、碟号和曲序一致"
		}
		matches = append(matches, AlbumMergeSongMatchResponse{SourceSong: source, TargetSong: targetSongs[best], Reason: reason})
	}
	return matches
}

func normalizedSongTitle(value string) string {
	var builder strings.Builder
	for _, char := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func (s *Service) MergeAlbums(user authctx.CurrentUser, targetAlbumID, sourceAlbumID uuid.UUID, requested []AlbumMergeSongMatchInput) error {
	if !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return apperr.Forbidden("music.merge_forbidden", "Admin role required")
	}
	preview, err := s.PreviewAlbumMerge(targetAlbumID, sourceAlbumID)
	if err != nil {
		return err
	}
	validMatches := make(map[uuid.UUID]uuid.UUID, len(requested))
	sourceIDs := make(map[uuid.UUID]struct{}, len(preview.SourceAlbum.Songs))
	targetIDs := make(map[uuid.UUID]struct{}, len(preview.TargetAlbum.Songs))
	for _, song := range preview.SourceAlbum.Songs {
		sourceIDs[song.ID] = struct{}{}
	}
	for _, song := range preview.TargetAlbum.Songs {
		targetIDs[song.ID] = struct{}{}
	}
	usedTargets := make(map[uuid.UUID]struct{})
	for _, match := range requested {
		_, sourceOK := sourceIDs[match.SourceSongID]
		_, targetOK := targetIDs[match.TargetSongID]
		_, targetUsed := usedTargets[match.TargetSongID]
		if !sourceOK || !targetOK || targetUsed {
			return apperr.BadRequest("validation.invalid_request", "song_matches are invalid")
		}
		validMatches[match.SourceSongID] = match.TargetSongID
		usedTargets[match.TargetSongID] = struct{}{}
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := mergeAlbumCredits(tx, sourceAlbumID, targetAlbumID); err != nil {
			return err
		}
		for _, sourceSong := range preview.SourceAlbum.Songs {
			if targetSongID, matched := validMatches[sourceSong.ID]; matched {
				if err := mergeSongRelations(tx, sourceSong.ID, targetSongID); err != nil {
					return err
				}
				if err := tx.Model(&model.Song{}).Where("id = ?", sourceSong.ID).Update("status", "closed").Error; err != nil {
					return err
				}
				continue
			}
			if err := tx.Model(&model.Song{}).Where("id = ?", sourceSong.ID).Update("album_id", targetAlbumID).Error; err != nil {
				return err
			}
		}
		if err := mergeAlbumBookmarks(tx, sourceAlbumID, targetAlbumID); err != nil {
			return err
		}
		if err := tx.Model(&model.AlbumImportSession{}).Where("target_album_id = ?", sourceAlbumID).Update("target_album_id", targetAlbumID).Error; err != nil {
			return err
		}
		if tx.Migrator().HasTable(&model.DiscussionTarget{}) {
			if err := tx.Model(&model.DiscussionTarget{}).Where("kind = ? AND resource_id = ?", "music_album", sourceAlbumID).Updates(map[string]any{"resource_id": targetAlbumID, "resource_key": targetAlbumID.String()}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&model.Album{}).Where("id = ?", sourceAlbumID).Updates(map[string]any{"entry_status": "closed", "status": "closed", "redirect_to": targetAlbumID}).Error; err != nil {
			return err
		}
		return audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "music.album.merge", EntityType: "album", EntityID: &targetAlbumID, Reason: "合并重复专辑", Metadata: map[string]any{"source_album_id": sourceAlbumID, "song_matches": len(validMatches)}})
	})
}

func mergeAlbumCredits(tx *gorm.DB, sourceID, targetID uuid.UUID) error {
	var credits []model.AlbumArtist
	if err := tx.Where("album_id = ?", sourceID).Find(&credits).Error; err != nil {
		return err
	}
	for _, credit := range credits {
		credit.AlbumID = targetID
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&credit).Error; err != nil {
			return err
		}
	}
	return tx.Where("album_id = ?", sourceID).Delete(&model.AlbumArtist{}).Error
}

func mergeAlbumBookmarks(tx *gorm.DB, sourceID, targetID uuid.UUID) error {
	var rows []model.AlbumBookmark
	if err := tx.Where("album_id = ?", sourceID).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		bookmark := model.AlbumBookmark{UserID: row.UserID, AlbumID: targetID}
		if err := tx.Where("user_id = ? AND album_id = ?", row.UserID, targetID).FirstOrCreate(&bookmark).Error; err != nil {
			return err
		}
	}
	return tx.Where("album_id = ?", sourceID).Delete(&model.AlbumBookmark{}).Error
}

func mergeSongRelations(tx *gorm.DB, sourceID, targetID uuid.UUID) error {
	var credits []model.SongArtist
	if err := tx.Where("song_id = ?", sourceID).Find(&credits).Error; err != nil {
		return err
	}
	for _, credit := range credits {
		credit.SongID = targetID
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&credit).Error; err != nil {
			return err
		}
	}
	var bookmarks []model.SongBookmark
	if err := tx.Where("song_id = ?", sourceID).Find(&bookmarks).Error; err != nil {
		return err
	}
	for _, row := range bookmarks {
		bookmark := model.SongBookmark{UserID: row.UserID, SongID: targetID}
		if err := tx.Where("user_id = ? AND song_id = ?", row.UserID, targetID).FirstOrCreate(&bookmark).Error; err != nil {
			return err
		}
	}
	if err := tx.Where("song_id = ?", sourceID).Delete(&model.SongBookmark{}).Error; err != nil {
		return err
	}

	var playlistRows []model.PlaylistSong
	if err := tx.Where("song_id = ?", sourceID).Find(&playlistRows).Error; err != nil {
		return err
	}
	for _, row := range playlistRows {
		entry := model.PlaylistSong{PlaylistID: row.PlaylistID, SongID: targetID, Position: row.Position}
		if err := tx.Where("playlist_id = ? AND song_id = ?", row.PlaylistID, targetID).FirstOrCreate(&entry).Error; err != nil {
			return err
		}
	}
	if err := tx.Where("song_id = ?", sourceID).Delete(&model.PlaylistSong{}).Error; err != nil {
		return err
	}

	var history []model.MusicListeningHistory
	if err := tx.Where("song_id = ?", sourceID).Find(&history).Error; err != nil {
		return err
	}
	for _, row := range history {
		var target model.MusicListeningHistory
		err := tx.Where("user_id = ? AND song_id = ?", row.UserID, targetID).First(&target).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Model(&row).Update("song_id", targetID).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		lastPlayed := target.LastPlayedAt
		if row.LastPlayedAt.After(lastPlayed) {
			lastPlayed = row.LastPlayedAt
		}
		if err := tx.Model(&target).Updates(map[string]any{"play_count": target.PlayCount + row.PlayCount, "last_played_at": lastPlayed}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
	}
	if err := tx.Where("song_id = ?", sourceID).Delete(&model.SongArtist{}).Error; err != nil {
		return err
	}
	return nil
}
