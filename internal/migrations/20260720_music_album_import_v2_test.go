package migrations

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"
	"github.com/google/uuid"
)

type legacyAlbumImportSession struct {
	model.Base
	Status      string `gorm:"not null;default:'pending_upload'"`
	PayloadJSON string `gorm:"type:text;not null"`
	CommittedAt *time.Time
	CommittedBy *uuid.UUID `gorm:"type:uuid"`
}

func (legacyAlbumImportSession) TableName() string {
	return "music_album_import_sessions"
}

func TestRunMusicAlbumImportsMigrationCreatesV2Schema(t *testing.T) {
	db := testdb.Open(t)

	if err := RunMusicAlbumImportsMigration(db); err != nil {
		t.Fatalf("run music album imports migration: %v", err)
	}

	for _, table := range []string{
		"music_album_import_sessions",
		"music_album_import_files",
		"music_album_import_jobs",
	} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("expected table %s to exist", table)
		}
	}

	for _, column := range []string{
		"user_id",
		"input_mode",
		"stage",
		"progress_current",
		"progress_total",
		"error_message",
		"expires_at",
	} {
		if !db.Migrator().HasColumn("music_album_import_sessions", column) {
			t.Fatalf("expected music_album_import_sessions.%s to exist", column)
		}
	}

	for _, column := range []string{
		"import_id",
		"relative_path",
		"file_name",
		"role",
		"detected_format",
		"content_type",
		"size",
		"source_key",
		"upload_id",
		"part_size",
		"completed_parts_json",
		"cleanup_json",
		"playback_key",
		"upload_status",
		"processing_status",
		"disc_number",
		"track_number",
		"title",
		"duration_seconds",
		"metadata_json",
		"error_message",
	} {
		if !db.Migrator().HasColumn("music_album_import_files", column) {
			t.Fatalf("expected music_album_import_files.%s to exist", column)
		}
	}

	for _, column := range []string{
		"import_id",
		"status",
		"stage",
		"attempts",
		"max_attempts",
		"locked_by",
		"locked_at",
		"heartbeat_at",
		"started_at",
		"finished_at",
		"next_attempt_at",
		"last_error",
	} {
		if !db.Migrator().HasColumn("music_album_import_jobs", column) {
			t.Fatalf("expected music_album_import_jobs.%s to exist", column)
		}
	}

	for _, column := range []string{"source_key", "playback_key"} {
		if !db.Migrator().HasColumn(&model.Song{}, column) {
			t.Fatalf("expected Songs.%s to exist", column)
		}
	}
}

func TestRunMusicAlbumImportsMigrationPreservesLegacySession(t *testing.T) {
	db := testdb.Open(t)
	if err := db.AutoMigrate(&legacyAlbumImportSession{}); err != nil {
		t.Fatalf("create legacy album import table: %v", err)
	}

	id := uuid.New()
	legacy := legacyAlbumImportSession{
		Base:        model.Base{ID: id},
		Status:      "pending_upload",
		PayloadJSON: `{"archive_name":"legacy.zip"}`,
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("insert legacy session: %v", err)
	}

	if err := RunMusicAlbumImportsMigration(db); err != nil {
		t.Fatalf("run music album imports migration: %v", err)
	}

	var session model.AlbumImportSession
	if err := db.First(&session, "id = ?", id).Error; err != nil {
		t.Fatalf("load migrated legacy session: %v", err)
	}
	if session.Status != "pending_upload" || session.PayloadJSON != `{"archive_name":"legacy.zip"}` {
		t.Fatalf("legacy session changed during migration: %#v", session)
	}
}

func TestAlbumImportSessionPreloadsFilesAndJob(t *testing.T) {
	db := testdb.Open(t)
	if err := RunMusicAlbumImportsMigration(db); err != nil {
		t.Fatalf("run music album imports migration: %v", err)
	}

	session := model.AlbumImportSession{
		Status:      "queued",
		PayloadJSON: "{}",
		Files: []model.AlbumImportFile{{
			FileName:         "01 - Track.flac",
			Role:             "audio",
			UploadStatus:     "uploaded",
			ProcessingStatus: "pending",
		}},
		Job: &model.AlbumImportJob{
			Status:      "queued",
			Stage:       "analyzing",
			MaxAttempts: 3,
		},
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("create album import session graph: %v", err)
	}

	var loaded model.AlbumImportSession
	if err := db.Preload("Files").Preload("Job").First(&loaded, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("preload album import graph: %v", err)
	}
	if len(loaded.Files) != 1 || loaded.Files[0].ImportID != session.ID {
		t.Fatalf("expected one preloaded file for %s, got %#v", session.ID, loaded.Files)
	}
	if loaded.Job == nil || loaded.Job.ImportID != session.ID {
		t.Fatalf("expected preloaded job for %s, got %#v", session.ID, loaded.Job)
	}
}
