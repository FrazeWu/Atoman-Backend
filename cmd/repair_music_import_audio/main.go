package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"atoman/internal/app"
	"atoman/internal/config"
	"atoman/internal/model"
	"atoman/internal/modules/music"
	"atoman/internal/platform/authctx"
	"atoman/internal/storage"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gorm.io/gorm"
)

const processedAudioError = "every track must have processed audio"

func main() {
	envFile := flag.String("env", ".env.prod", "environment file")
	idsValue := flag.String("ids", "", "comma-separated import session IDs; empty means all matching sessions")
	apply := flag.Bool("apply", false, "retry the matching sessions")
	flag.Parse()

	if err := godotenv.Load(*envFile); err != nil {
		log.Fatalf("load %s: %v", *envFile, err)
	}
	db, err := app.OpenDB(config.DBConfig{Type: os.Getenv("DATABASE_TYPE"), URL: os.Getenv("DATABASE_URL")})
	if err != nil {
		log.Fatalf("open database: %v", err)
	}

	ids, err := parseIDs(*idsValue)
	if err != nil {
		log.Fatal(err)
	}
	sessions, err := loadRepairSessions(db, ids)
	if err != nil {
		log.Fatalf("load repair sessions: %v", err)
	}
	if len(sessions) == 0 {
		log.Print("no matching processed-audio import sessions")
		return
	}
	for _, session := range sessions {
		log.Printf("candidate import=%s user=%s archive=%s", session.ID, session.UserID, albumImportArchiveName(session))
	}
	if !*apply {
		log.Printf("dry run only; rerun with -apply to retry %d session(s)", len(sessions))
		return
	}

	client, err := storage.InitS3Client()
	if err != nil {
		log.Fatalf("init object storage: %v", err)
	}
	service := music.NewServiceWithS3(db, client)
	for _, session := range sessions {
		if session.UserID == nil {
			log.Printf("skip import=%s: missing user", session.ID)
			continue
		}
		input, err := commitRequest(session)
		if err != nil {
			log.Printf("skip import=%s: %v", session.ID, err)
			continue
		}
		committed, err := service.CommitAlbumImportSession(authctx.CurrentUser{ID: *session.UserID, Role: authctx.RoleUser}, session.ID, input)
		if err != nil {
			log.Printf("repair failed import=%s: %v", session.ID, err)
			continue
		}
		log.Printf("repaired import=%s status=%s target_album=%s", committed.ID, committed.Status, uuidString(committed.TargetAlbumID))
	}
}

func parseIDs(raw string) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		id, err := uuid.Parse(value)
		if err != nil {
			return nil, fmt.Errorf("invalid import session ID %q: %w", value, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func loadRepairSessions(db *gorm.DB, ids []uuid.UUID) ([]model.AlbumImportSession, error) {
	query := db.
		Where("status = ? AND error_message = ?", music.AlbumImportStatusNeedsAttention, processedAudioError).
		Where(`EXISTS (
			SELECT 1 FROM music_album_import_files f
			WHERE f.import_id = music_album_import_sessions.id
			AND f.role = ?
			AND f.processing_status = ?
		)`, music.AlbumImportFileRoleAudio, music.AlbumImportFileProcessingStatusCompleted).
		Where(`NOT EXISTS (
			SELECT 1 FROM music_album_import_files f
			WHERE f.import_id = music_album_import_sessions.id
			AND f.role = ?
			AND f.processing_status <> ?
		)`, music.AlbumImportFileRoleAudio, music.AlbumImportFileProcessingStatusCompleted)
	if len(ids) > 0 {
		query = query.Where("id IN ?", ids)
	}
	var sessions []model.AlbumImportSession
	if err := query.Order("created_at ASC").Find(&sessions).Error; err != nil {
		return nil, err
	}
	return sessions, nil
}

func commitRequest(session model.AlbumImportSession) (music.CommitAlbumImportSessionInput, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(session.PayloadJSON), &payload); err != nil {
		return music.CommitAlbumImportSessionInput{}, fmt.Errorf("invalid payload: %w", err)
	}
	raw, ok := payload["commit_request"]
	if !ok || len(raw) == 0 {
		return music.CommitAlbumImportSessionInput{}, fmt.Errorf("missing commit request")
	}
	var input music.CommitAlbumImportSessionInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return music.CommitAlbumImportSessionInput{}, fmt.Errorf("invalid commit request: %w", err)
	}
	return input, nil
}

func albumImportArchiveName(session model.AlbumImportSession) string {
	for _, file := range session.Files {
		if file.Role == music.AlbumImportFileRoleArchive {
			return file.FileName
		}
	}
	return ""
}

func uuidString(value *uuid.UUID) string {
	if value == nil {
		return ""
	}
	return value.String()
}
