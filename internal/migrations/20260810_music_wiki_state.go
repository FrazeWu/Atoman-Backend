package migrations

import (
	"fmt"
	"log"

	"atoman/internal/model"

	"gorm.io/gorm"
)

// RunMusicWikiStateMigration separates catalog visibility from Wiki editability.
// It is intentionally idempotent because migration steps run on every startup.
func RunMusicWikiStateMigration(db *gorm.DB) error {
	if err := db.AutoMigrate(&model.MusicEntryStateRequest{}, &model.MusicEntryStateEvent{}); err != nil {
		return err
	}

	updates := []struct {
		name string
		sql  string
	}{
		{
			name: "artist lifecycle",
			sql: `UPDATE "Artists" SET lifecycle_status = CASE
				WHEN redirect_to IS NOT NULL THEN 'merged'
				WHEN entry_status = 'draft' AND lifecycle_status = 'active' THEN 'draft'
				WHEN entry_status = 'closed' AND lifecycle_status = 'active' THEN 'retired'
				ELSE lifecycle_status END`,
		},
		{
			name: "album lifecycle",
			sql: `UPDATE "Albums" SET lifecycle_status = CASE
				WHEN redirect_to IS NOT NULL THEN 'merged'
				WHEN (entry_status = 'closed' OR status IN ('closed', 'rejected')) AND lifecycle_status = 'active' THEN 'retired'
				WHEN (entry_status = 'draft' OR status IN ('draft', 'pending')) AND lifecycle_status = 'active' THEN 'draft'
				ELSE lifecycle_status END`,
		},
		{
			name: "song lifecycle",
			sql: `UPDATE "Songs" SET lifecycle_status = CASE
				WHEN status IN ('closed', 'rejected') AND lifecycle_status = 'active' THEN 'retired'
				WHEN status IN ('draft', 'pending') AND lifecycle_status = 'active' THEN 'draft'
				ELSE lifecycle_status END`,
		},
		{
			name: "active content protection edit state for artists",
			sql: `UPDATE "Artists" SET edit_status = 'locked' WHERE edit_status = 'development' AND EXISTS (
				SELECT 1 FROM content_protections p WHERE p.content_type = 'artist' AND p.content_id = "Artists".id
				AND p.protection_level = 'full' AND p.deleted_at IS NULL AND (p.expires_at IS NULL OR p.expires_at > CURRENT_TIMESTAMP))`,
		},
		{
			name: "active content protection edit state for albums",
			sql: `UPDATE "Albums" SET edit_status = 'locked' WHERE edit_status = 'development' AND EXISTS (
				SELECT 1 FROM content_protections p WHERE p.content_type = 'album' AND p.content_id = "Albums".id
				AND p.protection_level = 'full' AND p.deleted_at IS NULL AND (p.expires_at IS NULL OR p.expires_at > CURRENT_TIMESTAMP))`,
		},
		{
			name: "active content protection edit state for songs",
			sql: `UPDATE "Songs" SET edit_status = 'locked' WHERE edit_status = 'development' AND EXISTS (
				SELECT 1 FROM content_protections p WHERE p.content_type = 'song' AND p.content_id = "Songs".id
				AND p.protection_level = 'full' AND p.deleted_at IS NULL AND (p.expires_at IS NULL OR p.expires_at > CURRENT_TIMESTAMP))`,
		},
		{
			name: "retire migrated full content protections",
			sql: `UPDATE content_protections SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
				WHERE protection_level = 'full' AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
				AND content_type IN ('artist', 'album', 'song')`,
		},
	}
	for _, update := range updates {
		if err := db.Exec(update.sql).Error; err != nil {
			return fmt.Errorf("migrate music %s: %w", update.name, err)
		}
	}

	logLegacyMusicStates(db, &model.Artist{}, "entry_status", []string{"confirmed", "disputed", "protected"})
	logLegacyMusicStates(db, &model.Album{}, "status", []string{"pending", "verified", "rejected"})
	logLegacyMusicStates(db, &model.Song{}, "status", []string{"pending", "verified", "rejected"})
	return nil
}

func logLegacyMusicStates(db *gorm.DB, entity any, column string, states []string) {
	for _, state := range states {
		var count int64
		if err := db.Model(entity).Where(column+" = ?", state).Count(&count).Error; err == nil && count > 0 {
			log.Printf("music wiki migration: %d rows retain legacy %s=%q for manual audit", count, column, state)
		}
	}
}
