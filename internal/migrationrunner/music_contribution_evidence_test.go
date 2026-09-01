package migrationrunner

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMusicContributionEvidenceBackfillDoesNotLogMissingPreviousLyricVersion(t *testing.T) {
	var logs bytes.Buffer
	db := testdb.OpenWithConfig(t, &gorm.Config{
		Logger: logger.New(log.New(&logs, "", 0), logger.Config{
			LogLevel: logger.Warn,
			Colorful: false,
		}),
	})
	testdb.Migrate(t, db,
		&model.User{},
		&model.Song{},
		&model.ReputationRun{},
		&model.Revision{},
		&model.MusicSongLyricVersion{},
		&model.MusicContributionEvent{},
		&model.MusicContributionEvidence{},
	)

	user := model.User{Username: "lyric-contribution-owner", Email: "lyric-contribution@example.test", Password: "hash"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	createdAt := time.Date(2026, 9, 1, 9, 59, 28, 0, time.UTC)
	song := model.Song{Base: model.Base{CreatedAt: createdAt.Add(-time.Hour)}, Title: "First lyric version"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	version := model.MusicSongLyricVersion{
		Base:      model.Base{CreatedAt: createdAt},
		SongID:    song.ID,
		Version:   1,
		Content:   "first version",
		Target:    "all",
		Format:    "plain",
		CreatedBy: user.UUID,
	}
	if err := db.Create(&version).Error; err != nil {
		t.Fatalf("create lyric version: %v", err)
	}

	logs.Reset()
	if err := runMusicContributionEvidenceBackfill(db); err != nil {
		t.Fatalf("backfill contributions: %v", err)
	}

	if strings.Contains(logs.String(), "version <") {
		t.Fatalf("backfill logged a missing previous lyric version: %s", logs.String())
	}
	var eventCount int64
	if err := db.Model(&model.MusicContributionEvent{}).Where("source_kind = ? AND source_id = ?", "lyrics", version.ID).Count(&eventCount).Error; err != nil {
		t.Fatalf("count lyric contribution events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("lyric contribution events = %d, want 1", eventCount)
	}
}
