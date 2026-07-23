package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/dm"
	"atoman/internal/testdb"

	"github.com/google/uuid"
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

type memoryStore struct{ data map[string][]byte }

func (s *memoryStore) Put(_ context.Context, key, _ string, body io.Reader, _ int64) error {
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
func (*memoryStore) Delete(context.Context, string) error                             { return nil }
func (*memoryStore) IsLocal() bool                                                    { return true }

var _ dm.ImageStore = (*memoryStore)(nil)
