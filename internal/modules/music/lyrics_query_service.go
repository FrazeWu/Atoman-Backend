package music

import (
	"errors"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) GetSongLyrics(user authctx.CurrentUser, songID uuid.UUID) (MusicLyricsDTO, error) {
	viewer := &user
	if user.ID == uuid.Nil {
		viewer = nil
	}
	var song model.Song
	query := scopeVisibleMusicEntries(s.db, "\"Songs\"", "uploaded_by", viewer, false)
	if err := query.First(&song, "\"Songs\".id = ?", songID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return MusicLyricsDTO{}, apperr.NotFound("music.song_not_found", "Song not found")
		}
		return MusicLyricsDTO{}, err
	}
	dto := MusicLyricsDTO{SongID: songID, Format: "plain", Lines: []MusicLyricLineDTO{}, Annotations: []MusicLyricAnnotationDTO{}}
	var lyric model.MusicSongLyric
	if err := s.db.First(&lyric, "song_id = ?", songID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return dto, nil
		}
		return MusicLyricsDTO{}, err
	}
	dto.ID, dto.Content, dto.Translation, dto.Format = lyric.ID, lyric.Content, lyric.Translation, lyric.Format
	dto.TranslationLanguage = lyric.TranslationLanguage
	dto.Version, dto.UpdatedBy, dto.EditSummary, dto.UpdatedAt = lyric.Version, lyric.UpdatedBy, lyric.EditSummary, lyric.UpdatedAt
	dto.Source, dto.IsEdited = lyric.Source, lyric.Source != "" && lyric.Version > 1
	var lines []model.MusicSongLyricLine
	if err := s.db.Where("lyric_id = ?", lyric.ID).Order("line_index ASC").Find(&lines).Error; err != nil {
		return MusicLyricsDTO{}, err
	}
	for _, line := range lines {
		dto.Lines = append(dto.Lines, lyricLineDTO(line))
	}
	annotations, err := s.listLyricAnnotationDTOs(user, songID)
	if err != nil {
		return MusicLyricsDTO{}, err
	}
	dto.Annotations = annotations
	return dto, nil
}

func (s *Service) ListPendingLyricAnnotations(user authctx.CurrentUser) ([]PendingLyricAnnotationDTO, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	type row struct {
		AnnotationID, SongID uuid.UUID
		AlbumID              *uuid.UUID
	}
	var rows []row
	if err := s.db.Table("music_lyric_annotations AS a").
		Select("a.id AS annotation_id, a.song_id, s.album_id").
		Joins(`JOIN "Songs" AS s ON s.id = a.song_id AND s.deleted_at IS NULL AND s.lifecycle_status = 'active'`).
		Joins(`JOIN "Albums" AS al ON al.id = s.album_id AND al.deleted_at IS NULL AND al.lifecycle_status = 'active'`).
		Where("a.created_by = ? AND a.status = ? AND a.deleted_at IS NULL AND s.album_id IS NOT NULL", user.ID, "needs_rebind").
		Order("a.updated_at DESC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]PendingLyricAnnotationDTO, 0, len(rows))
	for _, row := range rows {
		if row.AlbumID != nil {
			items = append(items, PendingLyricAnnotationDTO{AnnotationID: row.AnnotationID.String(), SongID: row.SongID.String(), AlbumID: row.AlbumID.String()})
		}
	}
	return items, nil
}

func lyricLineDTO(line model.MusicSongLyricLine) MusicLyricLineDTO {
	return MusicLyricLineDTO{ID: line.ID, LineKey: line.LineKey, LineIndex: line.LineIndex, TimeMS: line.TimeMS, Text: line.Text, Translation: line.Translation}
}
