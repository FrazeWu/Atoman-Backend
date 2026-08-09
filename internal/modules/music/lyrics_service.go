package music

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/musiclyrics"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SaveLyricsInput struct {
	Target                string                      `json:"target"`
	Language              string                      `json:"language"`
	TranslationIncluded   bool                        `json:"translation_included"`
	BaseVersion           *int                        `json:"base_version"`
	Lines                 []SaveLyricsLineInput       `json:"lines"`
	Content               string                      `json:"content"`
	Translation           string                      `json:"translation"`
	Format                string                      `json:"format"`
	EditSummary           string                      `json:"edit_summary"`
	AnnotationResolutions []AnnotationResolutionInput `json:"annotation_resolutions"`
}

type SaveLyricsLineInput struct {
	LineKey     string `json:"line_key"`
	Text        string `json:"text"`
	Translation string `json:"translation"`
	TimeMS      *int   `json:"time_ms"`
}

type AnnotationResolutionInput struct {
	AnnotationID uuid.UUID `json:"annotation_id"`
	Action       string    `json:"action"`
	LineID       uuid.UUID `json:"line_id"`
	LineKey      string    `json:"line_key"`
	SelectedText string    `json:"selected_text"`
	StartOffset  int       `json:"start_offset"`
	EndOffset    int       `json:"end_offset"`
}

type CreateAnnotationInput struct {
	LineID       uuid.UUID `json:"line_id"`
	LineKey      string    `json:"line_key"`
	SelectedText string    `json:"selected_text"`
	StartOffset  int       `json:"start_offset"`
	EndOffset    int       `json:"end_offset"`
	Body         string    `json:"body"`
}

type UpdateLyricAnnotationInput struct {
	Body         *string
	LineKey      *string
	SelectedText *string
	StartOffset  *int
	EndOffset    *int
}

type MusicLyricLineDTO struct {
	ID          uuid.UUID `json:"id"`
	LineKey     string    `json:"line_key"`
	LineIndex   int       `json:"line_index"`
	TimeMS      *int      `json:"time_ms" extensions:"x-nullable"`
	Text        string    `json:"text"`
	Translation string    `json:"translation"`
}

type MusicSongLyricsVersionDTO struct {
	ID          uuid.UUID `json:"id"`
	SongID      uuid.UUID `json:"song_id"`
	Version     int       `json:"version"`
	Content     string    `json:"content"`
	Translation string    `json:"translation"`
	Format      string    `json:"format"`
	EditSummary string    `json:"edit_summary"`
	Target      string    `json:"target"`
	Language    string    `json:"language"`
	CreatedBy   uuid.UUID `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

type MusicLyricCreatorDTO struct {
	ID       uuid.UUID `json:"id"`
	Username string    `json:"username"`
}

type MusicLyricAnnotationDTO struct {
	ID           uuid.UUID            `json:"id"`
	SongID       uuid.UUID            `json:"song_id"`
	LineID       uuid.UUID            `json:"line_id"`
	LineKey      string               `json:"line_key"`
	SelectedText string               `json:"selected_text"`
	StartOffset  int                  `json:"start_offset"`
	EndOffset    int                  `json:"end_offset"`
	Body         string               `json:"body"`
	Creator      MusicLyricCreatorDTO `json:"creator"`
	Upvotes      int64                `json:"upvotes"`
	Downvotes    int64                `json:"downvotes"`
	NetScore     int64                `json:"net_score"`
	ViewerVote   string               `json:"viewer_vote"`
	Status       string               `json:"status"`
	CanEdit      bool                 `json:"can_edit"`
	CreatedAt    time.Time            `json:"created_at"`
	UpdatedAt    time.Time            `json:"updated_at"`
}

type MusicLyricsDTO struct {
	ID                  uuid.UUID                 `json:"id"`
	SongID              uuid.UUID                 `json:"song_id"`
	Content             string                    `json:"content"`
	Translation         string                    `json:"translation"`
	TranslationLanguage string                    `json:"translation_language"`
	Format              string                    `json:"format"`
	Version             int                       `json:"version"`
	UpdatedBy           uuid.UUID                 `json:"updated_by"`
	EditSummary         string                    `json:"edit_summary"`
	UpdatedAt           time.Time                 `json:"updated_at"`
	Lines               []MusicLyricLineDTO       `json:"lines"`
	Annotations         []MusicLyricAnnotationDTO `json:"annotations"`
}

func (s *Service) SaveSongLyrics(user authctx.CurrentUser, songID uuid.UUID, input SaveLyricsInput) (MusicLyricsDTO, error) {
	if user.ID == uuid.Nil {
		return MusicLyricsDTO{}, apperr.Unauthorized("Login required")
	}
	if strings.TrimSpace(input.EditSummary) == "" {
		input.EditSummary = "更新歌词"
	}

	unlock := s.serializeLyricsSaveForSQLite()
	defer unlock()

	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := lockLyricsSong(tx, songID); err != nil {
			return err
		}
		lines, err := prepareLyricsSave(tx, songID, &input)
		if err != nil {
			return err
		}
		return persistSongLyrics(tx, user.ID, songID, input, lines, false)
	})
	if err != nil {
		return MusicLyricsDTO{}, err
	}
	return s.GetSongLyrics(user, songID)
}

func (s *Service) ListSongLyricVersions(songID uuid.UUID) ([]MusicSongLyricsVersionDTO, error) {
	var song model.Song
	if err := s.db.First(&song, "id = ?", songID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.NotFound("music.song_not_found", "Song not found")
		}
		return nil, err
	}
	var versions []model.MusicSongLyricVersion
	if err := s.db.Where("song_id = ?", songID).Order("version DESC").Find(&versions).Error; err != nil {
		return nil, err
	}
	dtos := make([]MusicSongLyricsVersionDTO, 0, len(versions))
	for _, version := range versions {
		dtos = append(dtos, MusicSongLyricsVersionDTO{
			ID: version.ID, SongID: version.SongID, Version: version.Version,
			Content: version.Content, Translation: version.Translation, Format: version.Format,
			EditSummary: version.EditSummary, Target: version.Target, Language: version.Language,
			CreatedBy: version.CreatedBy, CreatedAt: version.CreatedAt,
		})
	}
	return dtos, nil
}

func (s *Service) RevertSongLyrics(user authctx.CurrentUser, songID uuid.UUID, version int, editSummary string) (MusicLyricsDTO, error) {
	if user.ID == uuid.Nil {
		return MusicLyricsDTO{}, apperr.Unauthorized("Login required")
	}
	if version <= 0 {
		return MusicLyricsDTO{}, lyricValidationError("version must be a positive integer")
	}
	if strings.TrimSpace(editSummary) == "" {
		editSummary = "恢复到第 " + strconv.Itoa(version) + " 版"
	}

	unlock := s.serializeLyricsSaveForSQLite()
	defer unlock()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := lockLyricsSong(tx, songID); err != nil {
			return err
		}
		var target model.MusicSongLyricVersion
		if err := tx.Where("song_id = ? AND version = ?", songID, version).First(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("music.lyrics_version_not_found", "Lyrics version not found")
			}
			return err
		}
		input := SaveLyricsInput{
			Content: target.Content, Translation: target.Translation, Format: target.Format,
			EditSummary: strings.TrimSpace(editSummary), Target: "restore", Language: target.Language,
		}
		lines, err := ParseLyricLines(input.Content, input.Translation, input.Format)
		if err != nil {
			return err
		}
		return persistSongLyrics(tx, user.ID, songID, input, lines, true)
	})
	if err != nil {
		return MusicLyricsDTO{}, err
	}
	return s.GetSongLyrics(user, songID)
}

func persistSongLyrics(tx *gorm.DB, actorID, songID uuid.UUID, input SaveLyricsInput, lines []ParsedLyricLine, autoNeedsRebind bool) error {
	var lyric model.MusicSongLyric
	findErr := tx.First(&lyric, "song_id = ?", songID).Error
	if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
		return findErr
	}
	isNew := errors.Is(findErr, gorm.ErrRecordNotFound)
	if isNew {
		lyric = model.MusicSongLyric{SongID: songID, Version: 1}
	} else {
		lyric.Version++
	}
	lyric.Content = input.Content
	lyric.Translation = input.Translation
	lyric.TranslationLanguage = input.Language
	lyric.Format = input.Format
	lyric.UpdatedBy = actorID
	lyric.EditSummary = input.EditSummary
	if isNew {
		if err := tx.Create(&lyric).Error; err != nil {
			return err
		}
	} else if err := tx.Save(&lyric).Error; err != nil {
		return err
	}

	currentLines, err := replaceCurrentLyricLines(tx, lyric.ID, lines)
	if err != nil {
		return err
	}
	if err := resolveInvalidAnnotationAnchors(tx, actorID, songID, currentLines, input.AnnotationResolutions, autoNeedsRebind); err != nil {
		return err
	}
	created := model.MusicSongLyricVersion{
		SongID: songID, Version: lyric.Version, Content: input.Content,
		Translation: input.Translation, Format: input.Format,
		EditSummary: input.EditSummary, Target: normalizedLyricsTarget(input.Target),
		Language: lyric.TranslationLanguage, CreatedBy: actorID,
	}
	if err := tx.Create(&created).Error; err != nil {
		return err
	}
	return tx.Model(&model.Song{}).Where("id = ?", songID).Update("lyrics", input.Content).Error
}

func prepareLyricsSave(tx *gorm.DB, songID uuid.UUID, input *SaveLyricsInput) ([]ParsedLyricLine, error) {
	target := normalizedLyricsTarget(input.Target)
	if target == "all" || target == "restore" {
		return ParseLyricLines(input.Content, input.Translation, input.Format)
	}
	if target != "original" && target != "translation" && target != "timing" && target != "import" {
		return nil, lyricValidationError("target must be original, translation, timing, or import")
	}

	var lyric model.MusicSongLyric
	err := tx.First(&lyric, "song_id = ?", songID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if input.BaseVersion == nil || *input.BaseVersion != 0 || (target != "original" && target != "import") {
			return nil, lyricsVersionConflict(input.BaseVersion, 0)
		}
		lyric = model.MusicSongLyric{SongID: songID, Version: 0, Format: "plain"}
	} else if err != nil {
		return nil, err
	}
	if input.BaseVersion == nil || *input.BaseVersion != lyric.Version {
		return nil, lyricsVersionConflict(input.BaseVersion, lyric.Version)
	}

	var current []model.MusicSongLyricLine
	if lyric.ID != uuid.Nil {
		if err := tx.Where("lyric_id = ?", lyric.ID).Order("line_index ASC").Find(&current).Error; err != nil {
			return nil, err
		}
	}
	currentByKey := make(map[string]model.MusicSongLyricLine, len(current))
	for _, line := range current {
		currentByKey[line.LineKey] = line
	}

	var lines []ParsedLyricLine
	switch target {
	case "original":
		if len(input.Lines) == 0 {
			return nil, lyricValidationError("original lyrics require at least one line")
		}
		input.Format = lyric.Format
		if input.Format == "" {
			input.Format = "plain"
		}
		input.Language = lyric.TranslationLanguage
		occurrences := map[string]int{}
		seenKeys := map[string]bool{}
		lines = make([]ParsedLyricLine, 0, len(input.Lines))
		for index, submitted := range input.Lines {
			text := strings.TrimSpace(submitted.Text)
			if text == "" {
				return nil, lyricValidationError("original lyric lines cannot be empty")
			}
			fingerprint := musiclyrics.TextFingerprint(text)
			occurrence := occurrences[fingerprint]
			occurrences[fingerprint]++
			lineKey := submitted.LineKey
			old, exists := currentByKey[lineKey]
			if exists && seenKeys[lineKey] {
				return nil, lyricValidationError("lyric line keys must be unique")
			}
			if !exists {
				lineKey = fmt.Sprintf("plain:%s:%d", fingerprint, occurrence)
			}
			seenKeys[lineKey] = true
			line := ParsedLyricLine{LineKey: lineKey, LineIndex: index, Text: text}
			if exists {
				line.TimeMS, line.Translation = old.TimeMS, old.Translation
			} else if input.Format == "lrc" {
				defaultTime := 0
				line.TimeMS = &defaultTime
			}
			lines = append(lines, line)
		}
	case "translation":
		if len(current) == 0 {
			return nil, lyricValidationError("original lyrics are required before adding a translation")
		}
		translations := make(map[string]string, len(input.Lines))
		for _, submitted := range input.Lines {
			if _, exists := currentByKey[submitted.LineKey]; !exists {
				return nil, lyricsVersionConflict(input.BaseVersion, lyric.Version)
			}
			translations[submitted.LineKey] = strings.TrimSpace(submitted.Translation)
		}
		lines = modelLinesToParsed(current)
		for index := range lines {
			if translation, exists := translations[lines[index].LineKey]; exists {
				lines[index].Translation = translation
			}
		}
		input.Format = lyric.Format
		input.Language = strings.TrimSpace(input.Language)
		if input.Language == "" {
			input.Language = lyric.TranslationLanguage
		}
	case "timing":
		if len(current) == 0 || len(input.Lines) != len(current) {
			return nil, lyricValidationError("timing must include every current lyric line")
		}
		timings := make(map[string]*int, len(input.Lines))
		for _, submitted := range input.Lines {
			if submitted.TimeMS == nil || *submitted.TimeMS < 0 {
				return nil, lyricValidationError("each lyric line requires a valid time")
			}
			if _, exists := currentByKey[submitted.LineKey]; !exists {
				return nil, lyricsVersionConflict(input.BaseVersion, lyric.Version)
			}
			if _, exists := timings[submitted.LineKey]; exists {
				return nil, lyricValidationError("timing line keys must be unique")
			}
			timings[submitted.LineKey] = submitted.TimeMS
		}
		lines = modelLinesToParsed(current)
		for index := range lines {
			timeMS, exists := timings[lines[index].LineKey]
			if !exists {
				return nil, lyricValidationError("timing must include every current lyric line")
			}
			lines[index].TimeMS = timeMS
		}
		input.Format = "lrc"
		input.Language = lyric.TranslationLanguage
	case "import":
		if len(input.Lines) == 0 {
			return nil, lyricValidationError("import requires at least one lyric line")
		}
		currentByText := make(map[string][]model.MusicSongLyricLine, len(current))
		for _, line := range current {
			fingerprint := musiclyrics.TextFingerprint(line.Text)
			currentByText[fingerprint] = append(currentByText[fingerprint], line)
		}
		claimedKeys := make(map[string]bool, len(input.Lines))
		currentTextOccurrences := make(map[string]int, len(currentByText))
		generatedOccurrences := make(map[string]int, len(input.Lines))
		lines = make([]ParsedLyricLine, 0, len(input.Lines))
		for index, submitted := range input.Lines {
			text := strings.TrimSpace(submitted.Text)
			if text == "" {
				return nil, lyricValidationError("imported lyric lines cannot be empty")
			}
			if submitted.TimeMS == nil || *submitted.TimeMS < 0 {
				return nil, lyricValidationError("each imported lyric line requires a valid time")
			}

			fingerprint := musiclyrics.TextFingerprint(text)
			matched, matchedExisting := currentByKey[submitted.LineKey]
			if matchedExisting && claimedKeys[matched.LineKey] {
				return nil, lyricValidationError("lyric line keys must be unique")
			}
			if !matchedExisting {
				candidates := currentByText[fingerprint]
				occurrence := currentTextOccurrences[fingerprint]
				for occurrence < len(candidates) && claimedKeys[candidates[occurrence].LineKey] {
					occurrence++
				}
				if occurrence < len(candidates) {
					matched, matchedExisting = candidates[occurrence], true
					occurrence++
				}
				currentTextOccurrences[fingerprint] = occurrence
			}

			lineKey := matched.LineKey
			if !matchedExisting {
				baseKey := fmt.Sprintf("lrc:%d:%s", *submitted.TimeMS, fingerprint)
				occurrence := generatedOccurrences[baseKey]
				for {
					lineKey = fmt.Sprintf("%s:%d", baseKey, occurrence)
					occurrence++
					if !claimedKeys[lineKey] {
						if _, exists := currentByKey[lineKey]; !exists {
							break
						}
					}
				}
				generatedOccurrences[baseKey] = occurrence
			}
			claimedKeys[lineKey] = true
			translation := ""
			if input.TranslationIncluded {
				translation = strings.TrimSpace(submitted.Translation)
			} else if matchedExisting {
				translation = matched.Translation
			}
			lines = append(lines, ParsedLyricLine{
				LineKey: lineKey, LineIndex: index, TimeMS: submitted.TimeMS,
				Text: text, Translation: translation,
			})
		}
		input.Format = "lrc"
		if input.TranslationIncluded {
			input.Language = strings.TrimSpace(input.Language)
			if input.Language == "" {
				input.Language = lyric.TranslationLanguage
			}
		} else {
			input.Language = lyric.TranslationLanguage
		}
	}
	if (target == "import" || target == "timing") && hasDescendingLyricTime(lines) {
		return nil, lyricValidationError("lyric times must be in ascending order")
	}
	input.Content, input.Translation = serializeParsedLyrics(lines, input.Format)
	return lines, nil
}

func hasDescendingLyricTime(lines []ParsedLyricLine) bool {
	for index := 1; index < len(lines); index++ {
		if lines[index-1].TimeMS != nil && lines[index].TimeMS != nil && *lines[index].TimeMS < *lines[index-1].TimeMS {
			return true
		}
	}
	return false
}

func modelLinesToParsed(lines []model.MusicSongLyricLine) []ParsedLyricLine {
	parsed := make([]ParsedLyricLine, 0, len(lines))
	for _, line := range lines {
		parsed = append(parsed, ParsedLyricLine{LineKey: line.LineKey, LineIndex: line.LineIndex, TimeMS: line.TimeMS, Text: line.Text, Translation: line.Translation})
	}
	return parsed
}

func serializeParsedLyrics(lines []ParsedLyricLine, format string) (string, string) {
	content := make([]string, 0, len(lines))
	translation := make([]string, 0, len(lines))
	lastTranslatedOccurrence := make(map[int]int)
	translationOccurrences := make(map[int]int)
	if format == "lrc" {
		for _, line := range lines {
			if line.TimeMS == nil {
				continue
			}
			timeMS := *line.TimeMS
			occurrence := translationOccurrences[timeMS]
			translationOccurrences[timeMS] = occurrence + 1
			if line.Translation != "" {
				lastTranslatedOccurrence[timeMS] = occurrence
			}
		}
		clear(translationOccurrences)
	}
	for _, line := range lines {
		if format == "lrc" && line.TimeMS != nil {
			stamp := formatLRCTime(*line.TimeMS)
			content = append(content, stamp+line.Text)
			timeMS := *line.TimeMS
			occurrence := translationOccurrences[timeMS]
			translationOccurrences[timeMS] = occurrence + 1
			if lastOccurrence, exists := lastTranslatedOccurrence[timeMS]; exists && occurrence <= lastOccurrence {
				translation = append(translation, stamp+line.Translation)
			}
		} else {
			content = append(content, line.Text)
			translation = append(translation, line.Translation)
		}
	}
	return strings.Join(content, "\n"), strings.Join(translation, "\n")
}

func formatLRCTime(timeMS int) string {
	centiseconds := timeMS / 10
	return fmt.Sprintf("[%02d:%02d.%02d]", centiseconds/6000, (centiseconds/100)%60, centiseconds%100)
}

func normalizedLyricsTarget(target string) string {
	if strings.TrimSpace(target) == "" {
		return "all"
	}
	return strings.TrimSpace(target)
}

func lyricsVersionConflict(baseVersion *int, currentVersion int) error {
	err := apperr.Conflict("music.lyrics_version_conflict", "Lyrics changed while you were editing")
	if baseVersion != nil {
		err.Details["base_version"] = *baseVersion
	}
	err.Details["current_version"] = currentVersion
	return err
}

// SyncLegacySongLyrics routes legacy song writes through the Wiki version and line model.
func SyncLegacySongLyrics(tx *gorm.DB, actorID, songID uuid.UUID, content, editSummary string) error {
	return musiclyrics.SyncLegacySongLyrics(tx, actorID, songID, content, editSummary)
}

func replaceCurrentLyricLines(tx *gorm.DB, lyricID uuid.UUID, parsed []ParsedLyricLine) ([]model.MusicSongLyricLine, error) {
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
		line.TimeMS = parsedLine.TimeMS
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

func resolveInvalidAnnotationAnchors(tx *gorm.DB, actorID, songID uuid.UUID, lines []model.MusicSongLyricLine, resolutions []AnnotationResolutionInput, autoNeedsRebind bool) error {
	lineByID := make(map[uuid.UUID]model.MusicSongLyricLine, len(lines))
	lineByKey := make(map[string]model.MusicSongLyricLine, len(lines))
	for _, line := range lines {
		lineByID[line.ID] = line
		lineByKey[line.LineKey] = line
	}
	resolutionByID := make(map[uuid.UUID]AnnotationResolutionInput, len(resolutions))
	for _, resolution := range resolutions {
		resolutionByID[resolution.AnnotationID] = resolution
	}
	var annotations []model.MusicLyricAnnotation
	if err := tx.Where("song_id = ? AND status <> ?", songID, "deleted").Find(&annotations).Error; err != nil {
		return err
	}
	invalid := make([]model.MusicLyricAnnotation, 0)
	unresolvedIDs := make([]string, 0)
	for _, annotation := range annotations {
		line, exists := lineByID[annotation.LineID]
		if exists && ValidateAnnotationAnchor(line.Text, annotation.StartOffset, annotation.EndOffset, annotation.SelectedText) == nil {
			continue
		}
		invalid = append(invalid, annotation)
		resolution, ok := resolutionByID[annotation.ID]
		if annotation.Status == "needs_rebind" && !ok {
			continue
		}
		if autoNeedsRebind && annotation.Status == "active" && !ok {
			resolution = AnnotationResolutionInput{AnnotationID: annotation.ID, Action: "needs_rebind"}
			ok = true
			resolutionByID[annotation.ID] = resolution
		}
		if !ok {
			unresolvedIDs = append(unresolvedIDs, annotation.ID.String())
		}
	}
	if len(unresolvedIDs) > 0 {
		sort.Strings(unresolvedIDs)
		conflict := apperr.Conflict("music.annotation_anchor_conflict", "Annotation anchor must be resolved")
		conflict.Details["annotation_ids"] = unresolvedIDs
		return conflict
	}

	for _, annotation := range invalid {
		resolution, ok := resolutionByID[annotation.ID]
		if annotation.Status == "needs_rebind" && !ok {
			continue
		}
		switch resolution.Action {
		case "needs_rebind":
			result := tx.Model(&model.MusicLyricAnnotation{}).
				Where("id = ? AND status = ?", annotation.ID, "active").
				Update("status", "needs_rebind")
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 && annotation.Status == "active" {
				return apperr.Conflict("music.annotation_anchor_conflict", "Annotation was deleted while resolving its anchor")
			}
			if result.RowsAffected == 1 {
				if err := createLyricsRebindNotification(tx, actorID, songID, annotation); err != nil {
					return err
				}
			}
		case "rebind":
			target, ok := lineByID[resolution.LineID]
			if resolution.LineID == uuid.Nil {
				target, ok = lineByKey[resolution.LineKey]
			}
			if !ok || ValidateAnnotationAnchor(target.Text, resolution.StartOffset, resolution.EndOffset, resolution.SelectedText) != nil {
				return lyricValidationError("annotation rebind anchor is invalid")
			}
			updates := map[string]any{
				"line_id": target.ID, "selected_text": resolution.SelectedText,
				"start_offset": resolution.StartOffset, "end_offset": resolution.EndOffset, "status": "active",
			}
			result := tx.Model(&model.MusicLyricAnnotation{}).
				Where("id = ? AND status <> ?", annotation.ID, "deleted").
				Updates(updates)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return apperr.Conflict("music.annotation_anchor_conflict", "Annotation was deleted while resolving its anchor")
			}
		default:
			return lyricValidationError("annotation resolution action is invalid")
		}
	}
	return nil
}

func createLyricsRebindNotification(tx *gorm.DB, actorID, songID uuid.UUID, annotation model.MusicLyricAnnotation) error {
	return musiclyrics.UpsertRebindNotification(tx, actorID, songID, annotation)
}

func (s *Service) GetSongLyrics(user authctx.CurrentUser, songID uuid.UUID) (MusicLyricsDTO, error) {
	var song model.Song
	if err := s.db.First(&song, "id = ?", songID).Error; err != nil {
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
		Joins(`JOIN "Songs" AS s ON s.id = a.song_id`).
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

func (s *Service) CreateLyricAnnotation(user authctx.CurrentUser, songID uuid.UUID, input CreateAnnotationInput) (MusicLyricAnnotationDTO, error) {
	if user.ID == uuid.Nil {
		return MusicLyricAnnotationDTO{}, apperr.Unauthorized("Login required")
	}
	if strings.TrimSpace(input.Body) == "" {
		return MusicLyricAnnotationDTO{}, lyricValidationError("annotation body is required")
	}
	unlock := s.serializeLyricsSaveForSQLite()
	defer unlock()

	var annotation model.MusicLyricAnnotation
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := lockLyricsSong(tx, songID); err != nil {
			return err
		}
		line, err := findCurrentLyricLine(tx, songID, input.LineID, input.LineKey)
		if err != nil {
			return err
		}
		if err := ValidateAnnotationAnchor(line.Text, input.StartOffset, input.EndOffset, input.SelectedText); err != nil {
			return err
		}
		annotation = model.MusicLyricAnnotation{
			SongID: songID, LineID: line.ID, SelectedText: input.SelectedText,
			StartOffset: input.StartOffset, EndOffset: input.EndOffset,
			Body: strings.TrimSpace(input.Body), CreatedBy: user.ID, Status: "active",
		}
		return tx.Create(&annotation).Error
	})
	if err != nil {
		return MusicLyricAnnotationDTO{}, err
	}
	return s.getLyricAnnotationDTO(user, songID, annotation.ID)
}

func findCurrentLyricLine(db *gorm.DB, songID, lineID uuid.UUID, lineKey string) (model.MusicSongLyricLine, error) {
	query := db.Table("music_song_lyric_lines AS l").
		Select("l.*").Joins("JOIN music_song_lyrics lyrics ON lyrics.id = l.lyric_id").
		Where("lyrics.song_id = ? AND l.deleted_at IS NULL AND lyrics.deleted_at IS NULL", songID)
	if lineID != uuid.Nil {
		query = query.Where("l.id = ?", lineID)
	} else if lineKey != "" {
		query = query.Where("l.line_key = ?", lineKey)
	} else {
		return model.MusicSongLyricLine{}, lyricValidationError("line_id or line_key is required")
	}
	var line model.MusicSongLyricLine
	if err := query.Take(&line).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.MusicSongLyricLine{}, apperr.NotFound("music.lyric_line_not_found", "Lyric line not found")
		}
		return model.MusicSongLyricLine{}, err
	}
	return line, nil
}

func (s *Service) UpdateLyricAnnotation(user authctx.CurrentUser, songID, annotationID uuid.UUID, input UpdateLyricAnnotationInput) (MusicLyricAnnotationDTO, error) {
	if user.ID == uuid.Nil {
		return MusicLyricAnnotationDTO{}, apperr.Unauthorized("Login required")
	}
	hasAnchor := input.LineKey != nil || input.SelectedText != nil || input.StartOffset != nil || input.EndOffset != nil
	if hasAnchor && (input.LineKey == nil || input.SelectedText == nil || input.StartOffset == nil || input.EndOffset == nil) {
		return MusicLyricAnnotationDTO{}, lyricValidationError("line_key, selected_text, start_offset, and end_offset are required together")
	}
	if input.Body == nil && !hasAnchor {
		return MusicLyricAnnotationDTO{}, lyricValidationError("annotation update is required")
	}
	var body string
	if input.Body != nil {
		body = strings.TrimSpace(*input.Body)
		if body == "" {
			return MusicLyricAnnotationDTO{}, lyricValidationError("annotation body is required")
		}
	}
	unlock := s.serializeLyricsSaveForSQLite()
	defer unlock()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := lockLyricsSong(tx, songID); err != nil {
			return err
		}
		annotation, err := findEditableLyricAnnotation(tx, user, songID, annotationID)
		if err != nil {
			return err
		}
		updates := map[string]any{}
		if input.Body != nil {
			updates["body"] = body
		}
		if hasAnchor {
			line, err := findCurrentLyricLine(tx, songID, uuid.Nil, *input.LineKey)
			if err != nil {
				return err
			}
			if err := ValidateAnnotationAnchor(line.Text, *input.StartOffset, *input.EndOffset, *input.SelectedText); err != nil {
				return err
			}
			updates["line_id"] = line.ID
			updates["selected_text"] = *input.SelectedText
			updates["start_offset"] = *input.StartOffset
			updates["end_offset"] = *input.EndOffset
			updates["status"] = "active"
		}
		result := tx.Model(&model.MusicLyricAnnotation{}).
			Where("id = ? AND status <> ?", annotation.ID, "deleted").Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return apperr.NotFound("music.annotation_not_found", "Annotation not found")
		}
		return nil
	})
	if err != nil {
		return MusicLyricAnnotationDTO{}, err
	}
	return s.getLyricAnnotationDTO(user, songID, annotationID)
}

func (s *Service) DeleteLyricAnnotation(user authctx.CurrentUser, songID, annotationID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	unlock := s.serializeLyricsSaveForSQLite()
	defer unlock()
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := lockLyricsSong(tx, songID); err != nil {
			return err
		}
		annotation, err := findEditableLyricAnnotation(tx, user, songID, annotationID)
		if err != nil {
			return err
		}
		result := tx.Model(&model.MusicLyricAnnotation{}).
			Where("id = ? AND status <> ?", annotation.ID, "deleted").Update("status", "deleted")
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return apperr.NotFound("music.annotation_not_found", "Annotation not found")
		}
		return nil
	})
}

func findEditableLyricAnnotation(tx *gorm.DB, user authctx.CurrentUser, songID, annotationID uuid.UUID) (model.MusicLyricAnnotation, error) {
	var annotation model.MusicLyricAnnotation
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND song_id = ? AND status <> ?", annotationID, songID, "deleted").First(&annotation).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return annotation, apperr.NotFound("music.annotation_not_found", "Annotation not found")
		}
		return annotation, err
	}
	if annotation.CreatedBy != user.ID {
		return annotation, apperr.Forbidden("music.annotation_forbidden", "Only the annotation creator can edit it")
	}
	return annotation, nil
}

func (s *Service) SetLyricAnnotationVote(user authctx.CurrentUser, songID, annotationID uuid.UUID, vote string) (MusicLyricAnnotationDTO, error) {
	if user.ID == uuid.Nil {
		return MusicLyricAnnotationDTO{}, apperr.Unauthorized("Login required")
	}
	if vote != "up" && vote != "down" && vote != "none" {
		return MusicLyricAnnotationDTO{}, lyricValidationError("vote must be up, down, or none")
	}
	unlock := s.serializeLyricsVoteForSQLite()
	defer unlock()

	err := s.db.Transaction(func(tx *gorm.DB) error {
		var annotation model.MusicLyricAnnotation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND song_id = ? AND status <> ?", annotationID, songID, "deleted").First(&annotation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("music.annotation_not_found", "Annotation not found")
			}
			return err
		}
		if vote == "none" {
			return tx.Where("annotation_id = ? AND user_id = ?", annotationID, user.ID).
				Delete(&model.MusicLyricAnnotationVote{}).Error
		}
		entry := model.MusicLyricAnnotationVote{AnnotationID: annotationID, UserID: user.ID, Vote: vote}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "annotation_id"}, {Name: "user_id"}},
			TargetWhere: clause.Where{Exprs: []clause.Expression{
				clause.Expr{SQL: "deleted_at IS NULL"},
			}},
			DoUpdates: clause.AssignmentColumns([]string{"vote", "updated_at"}),
		}).Create(&entry).Error
	})
	if err != nil {
		return MusicLyricAnnotationDTO{}, err
	}
	return s.getLyricAnnotationDTO(user, songID, annotationID)
}

func (s *Service) serializeLyricsSaveForSQLite() func() {
	if s.db.Dialector.Name() != "sqlite" {
		return func() {}
	}
	s.lyricsSaveMu.Lock()
	return s.lyricsSaveMu.Unlock
}

func (s *Service) serializeLyricsVoteForSQLite() func() {
	if s.db.Dialector.Name() != "sqlite" {
		return func() {}
	}
	s.lyricsVoteMu.Lock()
	return s.lyricsVoteMu.Unlock
}

func lockLyricsSong(tx *gorm.DB, songID uuid.UUID) error {
	var song model.Song
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&song, "id = ?", songID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("music.song_not_found", "Song not found")
		}
		return err
	}
	return nil
}

type lyricAnnotationRow struct {
	ID           uuid.UUID
	SongID       uuid.UUID
	LineID       uuid.UUID
	LineKey      string
	SelectedText string
	StartOffset  int
	EndOffset    int
	Body         string
	CreatedBy    uuid.UUID
	Username     string
	Status       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Upvotes      int64
	Downvotes    int64
}

func (s *Service) listLyricAnnotationDTOs(user authctx.CurrentUser, songID uuid.UUID) ([]MusicLyricAnnotationDTO, error) {
	return s.queryLyricAnnotationDTOs(user, songID, nil)
}

func (s *Service) queryLyricAnnotationDTOs(user authctx.CurrentUser, songID uuid.UUID, annotationID *uuid.UUID) ([]MusicLyricAnnotationDTO, error) {
	var rows []lyricAnnotationRow
	query := s.db.Table("music_lyric_annotations AS a").
		Select(`a.id, a.song_id, a.line_id, l.line_key, a.selected_text, a.start_offset, a.end_offset,
			a.body, a.created_by, u.username, a.status, a.created_at, a.updated_at,
			COALESCE(SUM(CASE WHEN v.vote = 'up' AND v.deleted_at IS NULL THEN 1 ELSE 0 END), 0) AS upvotes,
			COALESCE(SUM(CASE WHEN v.vote = 'down' AND v.deleted_at IS NULL THEN 1 ELSE 0 END), 0) AS downvotes`).
		Joins("JOIN music_song_lyric_lines l ON l.id = a.line_id").
		Joins(`JOIN "Users" u ON u.uuid = a.created_by`).
		Joins("LEFT JOIN music_lyric_annotation_votes v ON v.annotation_id = a.id").
		Where("a.song_id = ? AND a.status <> ? AND a.deleted_at IS NULL", songID, "deleted")
	if annotationID != nil {
		query = query.Where("a.id = ?", *annotationID)
	}
	err := query.
		Group("a.id, l.line_key, u.uuid, u.username").
		Order("(COALESCE(SUM(CASE WHEN v.vote = 'up' AND v.deleted_at IS NULL THEN 1 ELSE 0 END), 0) - COALESCE(SUM(CASE WHEN v.vote = 'down' AND v.deleted_at IS NULL THEN 1 ELSE 0 END), 0)) DESC").
		Order("upvotes DESC").Order("a.updated_at DESC").Order("a.created_at DESC").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	viewerVotes := map[uuid.UUID]string{}
	if user.ID != uuid.Nil && len(rows) > 0 {
		ids := make([]uuid.UUID, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		var votes []model.MusicLyricAnnotationVote
		if err := s.db.Where("annotation_id IN ? AND user_id = ?", ids, user.ID).Find(&votes).Error; err != nil {
			return nil, err
		}
		for _, vote := range votes {
			viewerVotes[vote.AnnotationID] = vote.Vote
		}
	}
	dtos := make([]MusicLyricAnnotationDTO, 0, len(rows))
	for _, row := range rows {
		viewerVote := viewerVotes[row.ID]
		if viewerVote == "" {
			viewerVote = "none"
		}
		dtos = append(dtos, MusicLyricAnnotationDTO{
			ID: row.ID, SongID: row.SongID, LineID: row.LineID, LineKey: row.LineKey,
			SelectedText: row.SelectedText, StartOffset: row.StartOffset, EndOffset: row.EndOffset,
			Body: row.Body, Creator: MusicLyricCreatorDTO{ID: row.CreatedBy, Username: row.Username},
			Upvotes: row.Upvotes, Downvotes: row.Downvotes, NetScore: row.Upvotes - row.Downvotes,
			ViewerVote: viewerVote, Status: row.Status, CanEdit: user.ID != uuid.Nil && user.ID == row.CreatedBy,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	return dtos, nil
}

func (s *Service) getLyricAnnotationDTO(user authctx.CurrentUser, songID, annotationID uuid.UUID) (MusicLyricAnnotationDTO, error) {
	annotations, err := s.queryLyricAnnotationDTOs(user, songID, &annotationID)
	if err != nil {
		return MusicLyricAnnotationDTO{}, err
	}
	for _, annotation := range annotations {
		if annotation.ID == annotationID {
			return annotation, nil
		}
	}
	return MusicLyricAnnotationDTO{}, apperr.NotFound("music.annotation_not_found", "Annotation not found")
}
