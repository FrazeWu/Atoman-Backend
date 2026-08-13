package music

import (
	"context"
	"fmt"
	"strings"

	"atoman/internal/model"
	"atoman/internal/musiclyrics"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type CatalogLyricsRepairResult struct {
	SongID  uuid.UUID
	Title   string
	Repair  bool
	Applied bool
	Reason  string
}

// RepairCatalogTimedLyrics upgrades legacy plain rows that actually contain LRC timestamps.
func RepairCatalogTimedLyrics(ctx context.Context, db *gorm.DB, apply bool, songID string) ([]CatalogLyricsRepairResult, error) {
	var lyrics []model.MusicSongLyric
	query := db.WithContext(ctx).Where("format = ?", "plain")
	if strings.TrimSpace(songID) != "" {
		query = query.Where("song_id = ?", strings.TrimSpace(songID))
	}
	if err := query.Order("created_at ASC").Find(&lyrics).Error; err != nil {
		return nil, err
	}
	results := make([]CatalogLyricsRepairResult, 0)
	for _, lyric := range lyrics {
		if _, err := musiclyrics.ParseLRC(lyric.Content, lyric.Translation); err != nil {
			continue
		}
		result := CatalogLyricsRepairResult{SongID: lyric.SongID, Repair: true}
		var song model.Song
		if err := db.WithContext(ctx).First(&song, "id = ?", lyric.SongID).Error; err != nil {
			result.Reason = err.Error()
			results = append(results, result)
			continue
		}
		result.Title = song.Title
		if !apply {
			results = append(results, result)
			continue
		}
		if lyric.UpdatedBy == uuid.Nil {
			result.Reason = "lyrics row has no updater"
			results = append(results, result)
			continue
		}
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return musiclyrics.SyncLegacySongLyricsWithFormat(tx, lyric.UpdatedBy, lyric.SongID, lyric.Content, lyric.Translation, "lrc", "修复 LRC 歌词时间轴")
		})
		if err != nil {
			result.Reason = err.Error()
		} else {
			result.Applied = true
		}
		results = append(results, result)
	}
	return results, nil
}

func FormatCatalogLyricsRepairResult(result CatalogLyricsRepairResult) string {
	status := "MATCH"
	if result.Applied {
		status = "APPLIED"
	}
	if result.Reason != "" {
		status = "SKIP"
	}
	return fmt.Sprintf("%s\t%s\t%s\t%s", status, result.SongID, result.Title, result.Reason)
}
