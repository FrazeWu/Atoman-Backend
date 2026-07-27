package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/dm"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestMigrateLegacyDMImageRejectsUnknownURLBeforeDatabaseWrites(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.DMMessage{}, &model.DMImage{})
	message := model.DMMessage{ConversationID: uuid.New(), SenderID: uuid.New(), ActorUserID: uuid.New(), ClientMessageID: uuid.New(), SenderType: model.DMPartyUser, ImageURL: "https://unknown.example/image.png"}
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}

	err := MigrateLegacyDMImages(context.Background(), db, &memoryStore{}, LegacyDMImageMigrationConfig{UploadsRoot: t.TempDir(), S3URLPrefix: "https://cdn.example.test", PublicBucket: "public"}, nil)
	if err == nil {
		t.Fatal("expected unsupported legacy URL error")
	}
	var stored model.DMMessage
	if err := db.First(&stored, "id = ?", message.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ImageID != nil {
		t.Fatalf("unknown URL must not bind image: %#v", stored.ImageID)
	}
	var images int64
	if err := db.Model(&model.DMImage{}).Count(&images).Error; err != nil || images != 0 {
		t.Fatalf("unknown URL must not create images: %d %v", images, err)
	}
}

func TestMigrateLegacyDMImageRejectsLocalPathTraversalBeforeDatabaseWrites(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.DMMessage{}, &model.DMImage{})
	message := model.DMMessage{ConversationID: uuid.New(), SenderID: uuid.New(), ActorUserID: uuid.New(), ClientMessageID: uuid.New(), SenderType: model.DMPartyUser, ImageURL: "/uploads/dm/images/../../.env"}
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{}
	err := MigrateLegacyDMImages(context.Background(), db, store, LegacyDMImageMigrationConfig{UploadsRoot: root}, nil)
	if err == nil {
		t.Fatal("expected traversal rejection")
	}
	if store.putCalls != 0 {
		t.Fatalf("traversal path must not be read, put calls=%d", store.putCalls)
	}
	assertNoLegacyDMImageWrites(t, db, message.ID)
}

func TestMigrateLegacyDMImageCopiesLocalImageAndBindsIt(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.DMMessage{}, &model.DMImage{})
	actor := uuid.New()
	message := model.DMMessage{ConversationID: uuid.New(), SenderID: actor, ActorUserID: actor, ClientMessageID: uuid.New(), SenderType: model.DMPartyUser, ImageURL: "/uploads/dm/images/legacy.png"}
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	file := filepath.Join(root, "dm", "images", "legacy.png")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("legacy image"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{}
	if err := MigrateLegacyDMImages(context.Background(), db, store, LegacyDMImageMigrationConfig{UploadsRoot: root}, nil); err != nil {
		t.Fatal(err)
	}
	var stored model.DMMessage
	if err := db.First(&stored, "id = ?", message.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ImageID == nil {
		t.Fatal("expected legacy image to be bound")
	}
	var image model.DMImage
	if err := db.First(&image, "id = ?", *stored.ImageID).Error; err != nil {
		t.Fatal(err)
	}
	if image.UploadedByUserID != actor || image.SizeBytes != int64(len("legacy image")) {
		t.Fatalf("unexpected migrated image: %#v", image)
	}
	if len(store.data) != 1 {
		t.Fatalf("expected one copied object, got %d", len(store.data))
	}
}

func TestMigrateLegacyDMImageReturnsCleanupFailureAfterDatabaseFailure(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.DMMessage{}, &model.DMImage{})
	actor := uuid.New()
	message := model.DMMessage{ConversationID: uuid.New(), SenderID: actor, ActorUserID: actor, ClientMessageID: uuid.New(), SenderType: model.DMPartyUser, ImageURL: "/uploads/dm/images/legacy.png"}
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	file := filepath.Join(root, "dm", "images", "legacy.png")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("legacy image"), 0o600); err != nil {
		t.Fatal(err)
	}
	databaseErr := errors.New("database write failed")
	cleanupErr := errors.New("private object cleanup failed")
	callback := "test:dm_image_create_failure"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*model.DMImage); ok {
			tx.AddError(databaseErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
	store := &memoryStore{deleteErr: cleanupErr}
	err := MigrateLegacyDMImages(context.Background(), db, store, LegacyDMImageMigrationConfig{UploadsRoot: root}, nil)
	if !errors.Is(err, databaseErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("expected database and cleanup failures, got %v", err)
	}
	if store.deleteCalls != 1 {
		t.Fatalf("expected cleanup attempt, got %d", store.deleteCalls)
	}
	assertNoLegacyDMImageWrites(t, db, message.ID)
}

func assertNoLegacyDMImageWrites(t *testing.T, db *gorm.DB, messageID uuid.UUID) {
	t.Helper()
	var message model.DMMessage
	if err := db.First(&message, "id = ?", messageID).Error; err != nil {
		t.Fatal(err)
	}
	if message.ImageID != nil {
		t.Fatalf("expected no image binding, got %#v", message.ImageID)
	}
	var count int64
	if err := db.Model(&model.DMImage{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("expected no image rows, got %d: %v", count, err)
	}
}

type memoryStore struct {
	data        map[string][]byte
	putCalls    int
	deleteCalls int
	deleteErr   error
}

func (s *memoryStore) Put(_ context.Context, key, _ string, body io.Reader, _ int64) error {
	s.putCalls++
	if s.data == nil {
		s.data = map[string][]byte{}
	}
	data, err := io.ReadAll(body)
	if err == nil {
		s.data[key] = data
	}
	return err
}
func (*memoryStore) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}
func (*memoryStore) SignedURL(context.Context, string, time.Duration) (string, error) { return "", nil }
func (s *memoryStore) Delete(context.Context, string) error {
	s.deleteCalls++
	return s.deleteErr
}
func (*memoryStore) IsLocal() bool { return true }

var _ dm.ImageStore = (*memoryStore)(nil)
