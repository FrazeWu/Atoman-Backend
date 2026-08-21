package migrations

import (
	"errors"
	"fmt"
	"strings"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var bullyAlbumID = uuid.MustParse("019fccce-ab08-7530-a28c-fa00597b2370")

// RunMusicStandaloneSongsPreSchemaMigration moves one-track single/leak releases
// onto Song before schema checks are installed by AutoMigrate.
func RunMusicStandaloneSongsPreSchemaMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.Song{}) || !db.Migrator().HasTable(&model.Album{}) {
		return nil
	}
	for _, field := range []string{"Description", "ReleaseType", "SourcesJSON"} {
		if !db.Migrator().HasColumn(&model.Song{}, field) {
			if err := db.Migrator().AddColumn(&model.Song{}, field); err != nil {
				return fmt.Errorf("add song %s: %w", field, err)
			}
		}
	}
	if db.Migrator().HasTable(&model.AlbumImportSession{}) && !db.Migrator().HasColumn(&model.AlbumImportSession{}, "TargetSongID") {
		if err := db.Migrator().AddColumn(&model.AlbumImportSession{}, "TargetSongID"); err != nil {
			return fmt.Errorf("add import target song: %w", err)
		}
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Album{}).Where("id = ? AND LOWER(album_type) IN ?", bullyAlbumID, []string{"single", "leak"}).Update("album_type", "album").Error; err != nil {
			return fmt.Errorf("reclassify BULLY: %w", err)
		}

		var releases []model.Album
		if err := tx.Where("LOWER(album_type) IN ?", []string{"single", "leak"}).Order("created_at ASC, id ASC").Find(&releases).Error; err != nil {
			return fmt.Errorf("find standalone song wrappers: %w", err)
		}
		for _, release := range releases {
			if err := migrateStandaloneSongRelease(tx, release); err != nil {
				return err
			}
		}
		return nil
	})
}

func migrateStandaloneSongRelease(tx *gorm.DB, release model.Album) error {
	var songs []model.Song
	if err := tx.Where("album_id = ? AND COALESCE(status, 'open') <> ? AND COALESCE(lifecycle_status, 'active') = ?", release.ID, "closed", model.MusicLifecycleActive).
		Order("disc_number ASC, track_number ASC, created_at ASC").Find(&songs).Error; err != nil {
		return fmt.Errorf("find songs for release %s: %w", release.ID, err)
	}
	if len(songs) != 1 {
		return fmt.Errorf("single/leak release %s must contain exactly one active song, found %d", release.ID, len(songs))
	}

	song := songs[0]
	releaseType := strings.ToLower(strings.TrimSpace(release.AlbumType))
	updates := map[string]any{
		"title":                  strings.TrimSpace(release.Title),
		"description":            release.Description,
		"release_type":           releaseType,
		"release_date":           release.ReleaseDate,
		"release_date_precision": release.ReleaseDatePrecision,
		"sources_json":           release.SourcesJSON,
		"album_id":               nil,
		"disc_number":            1,
		"track_number":           1,
	}
	if strings.TrimSpace(release.CoverURL) != "" {
		updates["cover_url"] = release.CoverURL
		updates["cover_source"] = release.CoverSource
	}
	if err := tx.Model(&model.Song{}).Where("id = ?", song.ID).Updates(updates).Error; err != nil {
		return fmt.Errorf("migrate standalone song %s: %w", song.ID, err)
	}

	var songCreditCount int64
	if err := tx.Model(&model.SongArtist{}).Where("song_id = ?", song.ID).Count(&songCreditCount).Error; err != nil {
		return err
	}
	if songCreditCount == 0 {
		var releaseCredits []model.AlbumArtist
		if err := tx.Where("album_id = ?", release.ID).Order("position ASC, role ASC").Find(&releaseCredits).Error; err != nil {
			return err
		}
		rows := make([]model.SongArtist, 0, len(releaseCredits))
		for _, credit := range releaseCredits {
			rows = append(rows, model.SongArtist{
				SongID: song.ID, ArtistID: credit.ArtistID, Role: credit.Role,
				CustomRole: credit.CustomRole, Position: credit.Position,
			})
		}
		if len(rows) > 0 {
			if err := tx.Create(&rows).Error; err != nil {
				return fmt.Errorf("copy artist credits for song %s: %w", song.ID, err)
			}
		}
	}

	if tx.Migrator().HasTable(&model.AlbumImportSession{}) {
		if err := tx.Model(&model.AlbumImportSession{}).Where("target_album_id = ?", release.ID).
			Updates(map[string]any{"target_album_id": nil, "target_song_id": song.ID}).Error; err != nil {
			return fmt.Errorf("retarget imports for song %s: %w", song.ID, err)
		}
	}
	if tx.Migrator().HasTable(&model.AlbumBookmark{}) {
		if err := tx.Unscoped().Where("album_id = ?", release.ID).Delete(&model.AlbumBookmark{}).Error; err != nil {
			return err
		}
	}
	if tx.Migrator().HasTable(&model.AlbumCorrection{}) {
		if err := tx.Unscoped().Where("album_id = ?", release.ID).Delete(&model.AlbumCorrection{}).Error; err != nil {
			return err
		}
	}
	if tx.Migrator().HasTable(&model.EditConflict{}) {
		if err := tx.Unscoped().Where("content_type = ? AND content_id = ?", "album", release.ID).Delete(&model.EditConflict{}).Error; err != nil {
			return fmt.Errorf("delete album edit conflicts: %w", err)
		}
	}
	if tx.Migrator().HasTable(&model.Revision{}) {
		if err := tx.Model(&model.Revision{}).Where("content_type = ? AND content_id = ?", "album", release.ID).Update("previous_revision_id", nil).Error; err != nil {
			return fmt.Errorf("unlink album revisions: %w", err)
		}
		if err := tx.Unscoped().Where("content_type = ? AND content_id = ?", "album", release.ID).Delete(&model.Revision{}).Error; err != nil {
			return fmt.Errorf("delete album revisions: %w", err)
		}
	}
	if tx.Migrator().HasTable(&model.ContentProtection{}) {
		if err := tx.Unscoped().Where("content_type = ? AND content_id = ?", "album", release.ID).Delete(&model.ContentProtection{}).Error; err != nil {
			return fmt.Errorf("delete album protection: %w", err)
		}
	}
	if err := tx.Where("album_id = ?", release.ID).Delete(&model.AlbumArtist{}).Error; err != nil {
		return err
	}
	if err := tx.Model(&model.Album{}).Where("canonical_album_id = ?", release.ID).Update("canonical_album_id", nil).Error; err != nil {
		return err
	}
	if err := tx.Unscoped().Delete(&model.Album{}, "id = ?", release.ID).Error; err != nil {
		return fmt.Errorf("delete standalone release wrapper %s: %w", release.ID, err)
	}
	return nil
}

func RunMusicStandaloneSongsConstraintsMigration(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.Song{}) || !db.Migrator().HasColumn(&model.Song{}, "ReleaseType") {
		return nil
	}
	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_songs_standalone_release_date
		ON "Songs" (release_type, release_date DESC, id)
		WHERE deleted_at IS NULL AND album_id IS NULL AND release_type IS NOT NULL`).Error; err != nil {
		return err
	}
	if db.Dialector.Name() != "postgres" {
		return nil
	}
	var constraintDefinition string
	if err := db.Raw(`SELECT COALESCE(pg_get_constraintdef(con.oid), '')
		FROM pg_constraint con
		JOIN pg_class rel ON rel.oid = con.conrelid
		JOIN pg_namespace ns ON ns.oid = rel.relnamespace
		WHERE con.conname = 'chk_songs_release_ownership'
		  AND rel.relname = 'Songs' AND ns.nspname = current_schema()
		LIMIT 1`).Scan(&constraintDefinition).Error; err != nil {
		return err
	}
	if strings.Contains(constraintDefinition, "album_id IS NOT NULL") && strings.Contains(constraintDefinition, "release_type IS NOT NULL") {
		return nil
	}
	if constraintDefinition != "" {
		if err := db.Exec(`ALTER TABLE "Songs" DROP CONSTRAINT chk_songs_release_ownership`).Error; err != nil {
			return err
		}
	}
	if err := db.Exec(`ALTER TABLE "Songs" ADD CONSTRAINT chk_songs_release_ownership CHECK (
		(album_id IS NULL AND release_type IS NOT NULL AND release_type IN ('single', 'leak'))
		OR (album_id IS NOT NULL AND release_type IS NULL)
	)`).Error; err != nil {
		return err
	}
	return nil
}

func standaloneReleaseType(value *string) string {
	if value == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(*value))
}

func validateStandaloneSongOwnership(song model.Song) error {
	releaseType := standaloneReleaseType(song.ReleaseType)
	if song.AlbumID == nil && releaseType == "" {
		return errors.New("standalone songs must define release_type")
	}
	if song.AlbumID != nil && releaseType != "" {
		return errors.New("album tracks cannot define release_type")
	}
	if releaseType != "" && releaseType != "single" && releaseType != "leak" {
		return errors.New("release_type must be single or leak")
	}
	return nil
}
