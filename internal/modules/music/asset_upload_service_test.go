package music

import (
	"context"
	"errors"
	"testing"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
)

func TestMusicAssetUploadCompletesSortedPartsAndCreatesMediaAsset(t *testing.T) {
	t.Setenv("S3_URL_PREFIX", "https://assets.example.test")
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{uploadID: "audio-upload"}
	svc.assetUploadMultipart = store
	size := int64(musicAssetUploadPartSize + 1024)

	session, err := svc.CreateMusicAssetUpload(user, CreateMusicAssetUploadInput{
		FileName: "track.flac", ContentType: "audio/flac", Size: size,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []MusicAssetUploadPart{
		{PartNumber: 2, ETag: "etag-2", Size: 1024},
		{PartNumber: 1, ETag: "etag-1", Size: musicAssetUploadPartSize},
	} {
		if _, err := svc.CompleteMusicAssetUploadPart(user, uuid.MustParse(session.ID), part.PartNumber, part); err != nil {
			t.Fatal(err)
		}
	}

	asset, err := svc.CompleteMusicAssetUpload(user, uuid.MustParse(session.ID))
	if err != nil {
		t.Fatal(err)
	}
	if asset.Purpose != "music.audio" || asset.Size != size || asset.URL != "https://assets.example.test/"+store.completeKey {
		t.Fatalf("unexpected media asset: %#v", asset)
	}
	if store.completeCalls != 1 || len(store.completedPartNumbers) != 2 || store.completedPartNumbers[0] != 1 || store.completedPartNumbers[1] != 2 {
		t.Fatalf("expected sorted multipart completion, got %#v", store)
	}

	again, err := svc.CompleteMusicAssetUpload(user, uuid.MustParse(session.ID))
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != asset.ID || store.completeCalls != 1 {
		t.Fatalf("expected idempotent completion, got %#v", again)
	}
	var count int64
	if err := db.Model(&model.MediaAsset{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one media asset, got %d", count)
	}
}

func TestMusicAssetUploadRejectsObjectSizeMismatch(t *testing.T) {
	t.Setenv("S3_URL_PREFIX", "https://assets.example.test")
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{objectSizeOverride: 512}
	svc.assetUploadMultipart = store
	session, err := svc.CreateMusicAssetUpload(user, CreateMusicAssetUploadInput{
		FileName: "track.mp3", ContentType: "audio/mpeg", Size: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteMusicAssetUploadPart(user, uuid.MustParse(session.ID), 1, MusicAssetUploadPart{PartNumber: 1, ETag: "etag-1", Size: 1024}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteMusicAssetUpload(user, uuid.MustParse(session.ID)); err == nil {
		t.Fatal("expected object size mismatch")
	}
	var count int64
	if err := db.Model(&model.MediaAsset{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected no media assets after size mismatch, got %d", count)
	}
}

func TestCleanupExpiredMusicAssetUploadsRetriesAbortFailures(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{abortErr: errors.New("storage unavailable")}
	svc.assetUploadMultipart = store
	session := model.MusicAssetUploadSession{
		UserID: user.ID, Status: musicAssetUploadStatusUploading, FileName: "expired.mp3", ContentType: "audio/mpeg",
		Size: 1, ObjectKey: "music/audio/expired.mp3", UploadID: "expired-upload", PartSize: musicAssetUploadPartSize,
		CompletedPartsJSON: "[]", ExpiresAt: time.Now().UTC().Add(-time.Minute),
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.CleanupExpiredMusicAssetUploads(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&session, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if session.Status != musicAssetUploadStatusExpiring || session.ErrorMessage == "" {
		t.Fatalf("expected retryable cleanup state, got %#v", session)
	}

	store.abortErr = nil
	if err := svc.CleanupExpiredMusicAssetUploads(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&session, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if session.Status != musicAssetUploadStatusCanceled || session.ErrorMessage != "" {
		t.Fatalf("expected canceled cleanup state, got %#v", session)
	}
}
