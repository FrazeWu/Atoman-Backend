package musiclyrics

import (
	"errors"
	"strings"
	"unicode/utf16"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SyncLegacySongLyrics routes legacy writes through current lyrics, parsed lines, and history.
func SyncLegacySongLyrics(tx *gorm.DB, actorID, songID uuid.UUID, content, editSummary string) error {
	var lyric model.MusicSongLyric
	findErr := tx.First(&lyric, "song_id = ?", songID).Error
	if findErr == nil && lyric.Content == content && lyric.Translation == "" && lyric.Format == "plain" {
		return tx.Model(&model.Song{}).Where("id = ?", songID).Update("lyrics", content).Error
	}
	if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
		return findErr
	}
	if errors.Is(findErr, gorm.ErrRecordNotFound) && strings.TrimSpace(content) == "" {
		return nil
	}

	if errors.Is(findErr, gorm.ErrRecordNotFound) {
		lyric = model.MusicSongLyric{SongID: songID, Version: 1}
	} else {
		lyric.Version++
	}
	lyric.Content = content
	lyric.Translation = ""
	lyric.Format = "plain"
	lyric.UpdatedBy = actorID
	lyric.EditSummary = editSummary
	if errors.Is(findErr, gorm.ErrRecordNotFound) {
		if err := tx.Create(&lyric).Error; err != nil {
			return err
		}
	} else if err := tx.Save(&lyric).Error; err != nil {
		return err
	}

	lines, err := replacePlainLines(tx, lyric.ID, ParsePlain(content, ""))
	if err != nil {
		return err
	}
	if err := markInvalidAnchorsForRebind(tx, songID, lines); err != nil {
		return err
	}
	version := model.MusicSongLyricVersion{
		SongID: songID, Version: lyric.Version, Content: content, Format: "plain",
		EditSummary: editSummary, CreatedBy: actorID,
	}
	if err := tx.Create(&version).Error; err != nil {
		return err
	}
	return tx.Model(&model.Song{}).Where("id = ?", songID).Update("lyrics", content).Error
}

func replacePlainLines(tx *gorm.DB, lyricID uuid.UUID, parsed []PlainLine) ([]model.MusicSongLyricLine, error) {
	var oldLines []model.MusicSongLyricLine
	if err := tx.Where("lyric_id = ?", lyricID).Find(&oldLines).Error; err != nil {
		return nil, err
	}
	oldByKey := make(map[string]model.MusicSongLyricLine, len(oldLines))
	for _, line := range oldLines {
		oldByKey[line.LineKey] = line
	}
	kept := make(map[uuid.UUID]bool, len(parsed))
	current := make([]model.MusicSongLyricLine, 0, len(parsed))
	for _, parsedLine := range parsed {
		line, exists := oldByKey[parsedLine.LineKey]
		if !exists {
			line = model.MusicSongLyricLine{LyricID: lyricID, LineKey: parsedLine.LineKey}
		}
		line.LineIndex = parsedLine.LineIndex
		line.Text = parsedLine.Text
		line.Translation = parsedLine.Translation
		if exists {
			if err := tx.Save(&line).Error; err != nil {
				return nil, err
			}
		} else if err := tx.Create(&line).Error; err != nil {
			return nil, err
		}
		kept[line.ID] = true
		current = append(current, line)
	}
	for _, line := range oldLines {
		if !kept[line.ID] {
			if err := tx.Delete(&line).Error; err != nil {
				return nil, err
			}
		}
	}
	return current, nil
}

func markInvalidAnchorsForRebind(tx *gorm.DB, songID uuid.UUID, lines []model.MusicSongLyricLine) error {
	lineByID := make(map[uuid.UUID]model.MusicSongLyricLine, len(lines))
	for _, line := range lines {
		lineByID[line.ID] = line
	}
	var annotations []model.MusicLyricAnnotation
	if err := tx.Where("song_id = ? AND status = ?", songID, "active").Find(&annotations).Error; err != nil {
		return err
	}
	for _, annotation := range annotations {
		line, exists := lineByID[annotation.LineID]
		if exists && validAnchor(line.Text, annotation.StartOffset, annotation.EndOffset, annotation.SelectedText) {
			continue
		}
		if err := tx.Model(&annotation).Update("status", "needs_rebind").Error; err != nil {
			return err
		}
	}
	return nil
}

func validAnchor(text string, startOffset, endOffset int, selectedText string) bool {
	units := utf16.Encode([]rune(text))
	if startOffset < 0 || startOffset >= endOffset || endOffset > len(units) {
		return false
	}
	selected := utf16.Encode([]rune(selectedText))
	anchor := units[startOffset:endOffset]
	if len(anchor) != len(selected) {
		return false
	}
	for index := range anchor {
		if anchor[index] != selected[index] {
			return false
		}
	}
	return true
}
