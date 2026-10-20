package books

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type bookResourceStoreFake struct{ *fakeBookUploadStore }

func (s *bookResourceStoreFake) PresignUploadPart(_, _ string, _ int, _ time.Duration) (string, error) {
	return "https://upload.example.test/part", nil
}

func newBookResourceTestService(t *testing.T) (*Service, authctx.CurrentUser, BookWorkDetail, *bookResourceStoreFake) {
	t.Helper()
	service, user := newBookWikiTestService(t)
	require.NoError(t, migrations.RunBooksV2Migration(service.db))
	work, err := service.CreateBookWork(context.Background(), user, CreateBookWorkInput{Title: "电子书作品", Authors: []string{"作者"}})
	require.NoError(t, err)
	store := &bookResourceStoreFake{&fakeBookUploadStore{uploadID: "multipart", objects: map[string][]byte{}}}
	service.WithBookResourceStore(store).WithVirusScanner(&recordingBookVirusScanner{})
	return service, user, work, store
}

func queueBookTestFile(t *testing.T, service *Service, user authctx.CurrentUser, store *bookResourceStoreFake, asset model.BookAsset, filename, contentType string, body []byte) BookResourceUploadDTO {
	t.Helper()
	ctx := context.Background()
	session, err := service.CreateBookResourceUpload(ctx, user, asset.ID, CreateBookResourceUploadInput{FileName: filename, ContentType: contentType, Size: int64(len(body)), PublicDistributionConfirmed: true})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(store.lastKey, "book/revisions/"))
	store.objects[store.lastKey] = body
	_, err = service.CompleteBookResourceUploadPart(ctx, user, session.ID, 1, BookUploadPart{ETag: "etag", Size: int64(len(body))})
	require.NoError(t, err)
	session, err = service.CompleteBookResourceUpload(ctx, user, session.ID)
	require.NoError(t, err)
	require.Equal(t, "uploaded", session.Status)
	require.Nil(t, session.RevisionID, "upload completion must not bypass safety checks")
	return session
}

func TestBookResourceUploadsAppendRevisionsWithoutChangingPrivateState(t *testing.T) {
	service, user, work, store := newBookResourceTestService(t)
	ctx := context.Background()
	workID := uuid.MustParse(work.ID)
	asset, err := service.CreateBookResource(ctx, user, workID, CreateBookResourceInput{Language: "zh", Format: "epub"})
	require.NoError(t, err)
	independent, err := service.CreateBookResource(ctx, user, workID, CreateBookResourceInput{Language: "en", Format: "pdf"})
	require.NoError(t, err)
	require.NotEqual(t, asset.ID, independent.ID)
	_, err = service.SetBookDefaultAsset(ctx, user, workID, "zh", asset.ID)
	require.Error(t, err, "unscanned files cannot become defaults")
	body := testEPUBBody(t)
	session := queueBookTestFile(t, service, user, store, asset, "first.epub", "application/epub+zip", body)
	count, err := service.ProcessBookResourceUploads(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	ready, err := service.GetBookResourceUpload(ctx, user, session.ID)
	require.NoError(t, err)
	require.Equal(t, "ready", ready.Status)
	require.NotNil(t, ready.RevisionID)
	oldRevisionID := *ready.RevisionID
	encoded, err := json.Marshal(ready)
	require.NoError(t, err)
	for _, secret := range []string{"object_key", "upload_id", "user_id", "book/revisions/"} {
		require.NotContains(t, string(encoded), secret)
	}
	require.NoError(t, service.db.Create(&model.BookReadingSelection{UserID: user.ID, WorkID: workID, AssetID: asset.ID, RevisionID: oldRevisionID}).Error)
	require.NoError(t, service.db.Create(&model.BookReadingState{UserID: user.ID, RevisionID: oldRevisionID, EPUBCFI: "epubcfi(/6/2!/4/1:5)", CurrentPercent: .4, FurthestPercent: .8}).Error)
	_, err = service.SetBookDefaultAsset(ctx, user, workID, "ZH", asset.ID)
	require.NoError(t, err)
	second := queueBookTestFile(t, service, user, store, asset, "second.epub", "application/epub+zip", body)
	count, err = service.ProcessBookResourceUploads(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	updated, err := service.GetBookResourceUpload(ctx, user, second.ID)
	require.NoError(t, err)
	require.NotEqual(t, oldRevisionID, *updated.RevisionID)
	completedAgain, err := service.CompleteBookResourceUpload(ctx, user, second.ID)
	require.NoError(t, err)
	require.Equal(t, updated.RevisionID, completedAgain.RevisionID)
	revisions, err := service.ListBookAssetRevisions(ctx, asset.ID)
	require.NoError(t, err)
	require.Len(t, revisions, 2)
	require.Equal(t, 2, revisions[0].Number)
	oldContent, err := service.OpenBookRevision(ctx, oldRevisionID)
	require.NoError(t, err)
	oldBody, err := io.ReadAll(oldContent.Body)
	require.NoError(t, err)
	require.NoError(t, oldContent.Body.Close())
	require.Equal(t, body, oldBody)
	var selection model.BookReadingSelection
	require.NoError(t, service.db.Where("user_id = ? AND work_id = ?", user.ID, workID).First(&selection).Error)
	require.Equal(t, oldRevisionID, selection.RevisionID)
	var state model.BookReadingState
	require.NoError(t, service.db.First(&state, "user_id = ? AND revision_id = ?", user.ID, oldRevisionID).Error)
	require.Equal(t, .4, state.CurrentPercent)
	require.Equal(t, .8, state.FurthestPercent)
	publicAssets, err := service.ListBookResources(ctx, workID)
	require.NoError(t, err)
	require.Len(t, publicAssets, 1, "the PDF has not uploaded a readable revision yet")
	_, err = service.SetBookDefaultAsset(ctx, user, workID, "en", asset.ID)
	require.Error(t, err, "community default must match file language")
	other, err := service.CreateBookWork(ctx, user, CreateBookWorkInput{Title: "另一作品", Authors: []string{"其他作者"}})
	require.NoError(t, err)
	_, err = service.SetBookDefaultAsset(ctx, user, uuid.MustParse(other.ID), "zh", asset.ID)
	require.Error(t, err, "community default cannot reference another work")
}

type infectedBookScanner struct{}

func (*infectedBookScanner) Scan(context.Context, io.Reader) error { return ErrBookVirusDetected }

func TestBookResourceSafetyFailuresNeverPublishOrReplaceFiles(t *testing.T) {
	service, user, work, store := newBookResourceTestService(t)
	ctx := context.Background()
	asset, err := service.CreateBookResource(ctx, user, uuid.MustParse(work.ID), CreateBookResourceInput{Format: "txt", Language: "en"})
	require.NoError(t, err)
	queued := queueBookTestFile(t, service, user, store, asset, "safe.txt", "text/plain", []byte("safe file"))
	service.WithVirusScanner(nil)
	count, err := service.ProcessBookResourceUploads(ctx)
	require.Error(t, err)
	require.Zero(t, count)
	waiting, err := service.GetBookResourceUpload(ctx, user, queued.ID)
	require.NoError(t, err)
	require.Equal(t, "uploaded", waiting.Status)
	service.WithVirusScanner(&recordingBookVirusScanner{})
	_, err = service.ProcessBookResourceUploads(ctx)
	require.NoError(t, err)
	ready, err := service.GetBookResourceUpload(ctx, user, queued.ID)
	require.NoError(t, err)
	infected := queueBookTestFile(t, service, user, store, asset, "infected.txt", "text/plain", []byte("infected file"))
	service.WithVirusScanner(&infectedBookScanner{})
	_, err = service.ProcessBookResourceUploads(ctx)
	require.ErrorIs(t, err, ErrBookVirusDetected)
	quarantined, err := service.GetBookResourceUpload(ctx, user, infected.ID)
	require.NoError(t, err)
	require.Equal(t, "quarantined", quarantined.Status)
	require.Nil(t, quarantined.RevisionID)
	var current model.BookAsset
	require.NoError(t, service.db.First(&current, "id = ?", asset.ID).Error)
	require.Equal(t, ready.RevisionID, current.CurrentRevisionID)
	invalid, err := service.CreateBookResource(ctx, user, uuid.MustParse(work.ID), CreateBookResourceInput{Format: "epub"})
	require.NoError(t, err)
	malformed := queueBookTestFile(t, service, user, store, invalid, "invalid.epub", "application/epub+zip", []byte("PK\x03\x04not an epub archive"))
	service.WithVirusScanner(&recordingBookVirusScanner{})
	_, err = service.ProcessBookResourceUploads(ctx)
	require.Error(t, err)
	failed, err := service.GetBookResourceUpload(ctx, user, malformed.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", failed.Status)
	require.Nil(t, failed.RevisionID)
}

func TestBookResourceUploadOwnershipAndPartValidation(t *testing.T) {
	service, user, work, store := newBookResourceTestService(t)
	ctx := context.Background()
	asset, err := service.CreateBookResource(ctx, user, uuid.MustParse(work.ID), CreateBookResourceInput{Format: "txt"})
	require.NoError(t, err)
	input := CreateBookResourceUploadInput{FileName: "book.txt", ContentType: "text/plain", Size: 12}
	_, err = service.CreateBookResourceUpload(ctx, user, asset.ID, input)
	require.Equal(t, "books.public_distribution_confirmation_required", apperr.FromError(err).Code)
	input.PublicDistributionConfirmed = true
	session, err := service.CreateBookResourceUpload(ctx, user, asset.ID, input)
	require.NoError(t, err)
	other := authctx.CurrentUser{ID: uuid.New()}
	_, err = service.GetBookResourceUpload(ctx, other, session.ID)
	require.Error(t, err)
	_, err = service.CompleteBookResourceUpload(ctx, other, session.ID)
	require.Error(t, err)
	require.Empty(t, store.completedKeys)
	_, err = service.CreateBookResourceUploadPart(ctx, user, session.ID, 0)
	require.Error(t, err)
	_, err = service.CreateBookResourceUploadPart(ctx, user, session.ID, 2)
	require.Error(t, err)
	part, err := service.CreateBookResourceUploadPart(ctx, user, session.ID, 1)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(part.UploadURL, "https://"))
	_, err = service.CompleteBookResourceUpload(ctx, user, session.ID)
	require.Equal(t, "books.upload_incomplete", apperr.FromError(err).Code)
	_, err = service.CompleteBookResourceUploadPart(ctx, user, session.ID, 1, BookUploadPart{ETag: "bad", Size: 11})
	require.Error(t, err)
	require.NoError(t, service.db.Model(&model.BookUploadSession{}).Where("id = ?", session.ID).Update("expires_at", time.Now().Add(-time.Minute)).Error)
	_, err = service.CreateBookResourceUploadPart(ctx, user, session.ID, 1)
	require.Error(t, err)
}
