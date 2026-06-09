package migrations

import (
	"encoding/json"
	"log"

	"gorm.io/gorm"

	"atoman/internal/model"
)

// MigrateToRevisionSystem migrates existing Album/Song data to the new revision system
func MigrateToRevisionSystem(db *gorm.DB) error {
	log.Println("Starting migration to revision system...")

	// Create new tables
	if err := db.AutoMigrate(
		&model.Revision{},
		&model.EditConflict{},
		&model.ContentProtection{},
		&model.Discussion{},
	); err != nil {
		log.Printf("Failed to create new tables: %v", err)
		return err
	}

	// Start transaction
	tx := db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
			log.Printf("Migration panicked and rolled back: %v", r)
		}
	}()

	// Migrate Albums
	if err := migrateAlbums(tx); err != nil {
		tx.Rollback()
		return err
	}

	// Migrate Songs
	if err := migrateSongs(tx); err != nil {
		tx.Rollback()
		return err
	}

	// Migrate AlbumCorrections
	if err := migrateAlbumCorrections(tx); err != nil {
		tx.Rollback()
		return err
	}

	// Migrate SongCorrections
	if err := migrateSongCorrections(tx); err != nil {
		tx.Rollback()
		return err
	}

	if err := tx.Commit().Error; err != nil {
		log.Printf("Failed to commit migration: %v", err)
		return err
	}

	log.Println("Migration to revision system completed successfully")
	return nil
}

func migrateAlbums(tx *gorm.DB) error {
	log.Println("Migrating albums...")

	var albums []model.Album
	if err := tx.Preload("Artists").Find(&albums).Error; err != nil {
		return err
	}

	for _, album := range albums {
		// Serialize current state as JSON
		snapshot, err := json.Marshal(album)
		if err != nil {
			log.Printf("Failed to serialize album %s: %v", album.ID, err)
			continue
		}

		// Determine editor (use uploader or system UUID)
		editorID := album.UploadedBy
		if editorID == nil {
			// Use system UUID for legacy data
			systemUUID := album.ID // Fallback to album ID
			editorID = &systemUUID
		}

		// Create initial revision
		initialRevision := model.Revision{
			ContentType:     "album",
			ContentID:       album.ID,
			VersionNumber:   1,
			ContentSnapshot: snapshot,
			EditorID:        *editorID,
			EditSummary:     "Initial version (migrated from existing data)",
			EditType:        "creation",
			Status:          album.Status, // Preserve original status
			IsCurrent:       true,
			CreatedAt:       album.CreatedAt,
		}

		if err := tx.Create(&initialRevision).Error; err != nil {
			log.Printf("Failed to create initial revision for album %s: %v", album.ID, err)
			return err
		}

		log.Printf("Migrated album: %s (version 1)", album.Title)
	}

	log.Printf("Successfully migrated %d albums", len(albums))
	return nil
}

func migrateSongs(tx *gorm.DB) error {
	log.Println("Migrating songs...")

	var songs []model.Song
	if err := tx.Preload("Artists").Find(&songs).Error; err != nil {
		return err
	}

	for _, song := range songs {
		snapshot, err := json.Marshal(song)
		if err != nil {
			log.Printf("Failed to serialize song %s: %v", song.ID, err)
			continue
		}

		editorID := song.UploadedBy
		if editorID == nil {
			systemUUID := song.ID
			editorID = &systemUUID
		}

		initialRevision := model.Revision{
			ContentType:     "song",
			ContentID:       song.ID,
			VersionNumber:   1,
			ContentSnapshot: snapshot,
			EditorID:        *editorID,
			EditSummary:     "Initial version (migrated from existing data)",
			EditType:        "creation",
			Status:          song.Status,
			IsCurrent:       true,
			CreatedAt:       song.CreatedAt,
		}

		if err := tx.Create(&initialRevision).Error; err != nil {
			log.Printf("Failed to create initial revision for song %s: %v", song.ID, err)
			return err
		}
	}

	log.Printf("Successfully migrated %d songs", len(songs))
	return nil
}

func migrateAlbumCorrections(tx *gorm.DB) error {
	log.Println("Migrating album corrections...")

	var corrections []model.AlbumCorrection
	if err := tx.Order("created_at ASC").Find(&corrections).Error; err != nil {
		return err
	}

	for _, correction := range corrections {
		// Get base album
		var album model.Album
		if err := tx.Preload("Artists").First(&album, "id = ?", correction.AlbumID).Error; err != nil {
			log.Printf("Album not found for correction %s: %v", correction.ID, err)
			continue
		}

		// Find the current revision
		var baseRevision model.Revision
		if err := tx.Where("content_id = ? AND content_type = ? AND is_current = ?",
			correction.AlbumID, "album", true).
			Order("version_number DESC").
			First(&baseRevision).Error; err != nil {
			log.Printf("Base revision not found for album %s: %v", correction.AlbumID, err)
			continue
		}

		// Apply corrections to album snapshot
		modifiedAlbum := album
		if correction.CorrectedTitle != "" {
			modifiedAlbum.Title = correction.CorrectedTitle
		}
		if correction.CorrectedCoverURL != "" {
			modifiedAlbum.CoverURL = correction.CorrectedCoverURL
			modifiedAlbum.CoverSource = correction.CorrectedCoverSource
		}
		if correction.CorrectedReleaseDate != nil {
			modifiedAlbum.ReleaseDate = *correction.CorrectedReleaseDate
		}
		// TODO: Handle artist IDs if needed

		snapshot, err := json.Marshal(modifiedAlbum)
		if err != nil {
			log.Printf("Failed to serialize corrected album: %v", err)
			continue
		}

		editorID := correction.UserID
		if editorID == nil {
			systemUUID := album.ID
			editorID = &systemUUID
		}

		// Create correction revision
		correctionRevision := model.Revision{
			ContentType:        "album",
			ContentID:          correction.AlbumID,
			VersionNumber:      baseRevision.VersionNumber + 1,
			PreviousRevisionID: &baseRevision.ID,
			ContentSnapshot:    snapshot,
			EditorID:           *editorID,
			EditSummary:        correction.Reason,
			EditType:           "edit",
			Status:             correction.Status,
			ReviewerID:         correction.ApprovedBy,
			ReviewedAt:         correction.ApprovedAt,
			IsCurrent:          (correction.Status == "approved"),
			CreatedAt:          correction.CreatedAt,
		}

		if err := tx.Create(&correctionRevision).Error; err != nil {
			log.Printf("Failed to create correction revision: %v", err)
			return err
		}

		// If approved, update previous revision to not current
		if correction.Status == "approved" {
			tx.Model(&baseRevision).Update("is_current", false)
		}

		log.Printf("Migrated correction for album %s (version %d)", album.Title, correctionRevision.VersionNumber)
	}

	log.Printf("Successfully migrated %d album corrections", len(corrections))
	return nil
}

func migrateSongCorrections(tx *gorm.DB) error {
	log.Println("Migrating song corrections...")

	var corrections []model.SongCorrection
	if err := tx.Order("created_at ASC").Find(&corrections).Error; err != nil {
		return err
	}

	for _, correction := range corrections {
		var song model.Song
		if err := tx.Preload("Artists").First(&song, "id = ?", correction.SongID).Error; err != nil {
			log.Printf("Song not found for correction %s: %v", correction.ID, err)
			continue
		}

		var baseRevision model.Revision
		if err := tx.Where("content_id = ? AND content_type = ? AND is_current = ?",
			correction.SongID, "song", true).
			Order("version_number DESC").
			First(&baseRevision).Error; err != nil {
			log.Printf("Base revision not found for song %s: %v", correction.SongID, err)
			continue
		}

		// Apply correction based on field name
		modifiedSong := song
		switch correction.FieldName {
		case "title":
			modifiedSong.Title = correction.CorrectedValue
		case "lyrics":
			modifiedSong.Lyrics = correction.CorrectedValue
		// Add other fields as needed
		}

		snapshot, err := json.Marshal(modifiedSong)
		if err != nil {
			log.Printf("Failed to serialize corrected song: %v", err)
			continue
		}

		editorID := correction.UserID
		if editorID == nil {
			systemUUID := song.ID
			editorID = &systemUUID
		}

		correctionRevision := model.Revision{
			ContentType:        "song",
			ContentID:          correction.SongID,
			VersionNumber:      baseRevision.VersionNumber + 1,
			PreviousRevisionID: &baseRevision.ID,
			ContentSnapshot:    snapshot,
			EditorID:           *editorID,
			EditSummary:        correction.Reason,
			EditType:           "edit",
			Status:             correction.Status,
			ReviewerID:         correction.ApprovedBy,
			ReviewedAt:         correction.ApprovedAt,
			IsCurrent:          (correction.Status == "approved"),
			CreatedAt:          correction.CreatedAt,
		}

		if err := tx.Create(&correctionRevision).Error; err != nil {
			log.Printf("Failed to create song correction revision: %v", err)
			return err
		}

		if correction.Status == "approved" {
			tx.Model(&baseRevision).Update("is_current", false)
		}

		log.Printf("Migrated correction for song %s (version %d)", song.Title, correctionRevision.VersionNumber)
	}

	log.Printf("Successfully migrated %d song corrections", len(corrections))
	return nil
}

// RollbackRevisionSystem removes revision system tables (use with caution!)
func RollbackRevisionSystem(db *gorm.DB) error {
	log.Println("WARNING: Rolling back revision system migration...")

	return db.Migrator().DropTable(
		&model.Revision{},
		&model.EditConflict{},
		&model.ContentProtection{},
		&model.Discussion{},
	)
}
