package books

import (
	"bytes"
	"context"
	"io"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPublicationEvidenceUploadStoresPrivateR2Object(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookPublicationRequest{}, &model.BookRightsDeclaration{})
	store := &fakeBookUploadStore{objects: map[string][]byte{}}
	service := NewService(db).WithBookUploadStore(store)
	owner := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	moderator := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleModerator}
	request := model.BookPublicationRequest{SubmittedBy: owner.ID, AssetID: uuid.New(), Status: model.BookPublicationStatusPendingReview}
	require.NoError(t, db.Create(&request).Error)
	require.NoError(t, db.Create(&model.BookRightsDeclaration{RequestID: request.ID, LicenseType: "authorized_distribution", Declaration: "I have permission"}).Error)

	body := []byte("%PDF-1.7 evidence")
	result, err := service.UploadPublicationEvidence(owner, request.ID, "permission.pdf", "application/pdf", int64(len(body)), bytes.NewReader(body))
	require.NoError(t, err)
	require.True(t, result.EvidenceUploaded)
	require.NotEmpty(t, store.objects)
	var rights model.BookRightsDeclaration
	require.NoError(t, db.First(&rights, "request_id = ?", request.ID).Error)
	require.NotEmpty(t, rights.EvidenceObjectKey)
	require.NotContains(t, rights.EvidenceObjectKey, "permission.pdf")
	require.Equal(t, "permission.pdf", rights.EvidenceFileName)
	require.Equal(t, int64(len(body)), rights.EvidenceSizeBytes)
	require.Equal(t, body, store.objects[rights.EvidenceObjectKey])

	evidence, reader, err := service.OpenPublicationEvidence(moderator, request.ID)
	require.NoError(t, err)
	require.Equal(t, "application/pdf", evidence.ContentType)
	readBody, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.Equal(t, body, readBody)
	_, _, err = service.OpenPublicationEvidence(authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}, request.ID)
	require.Error(t, err)
}

func TestPublicationAppealRestoresRemovedAssetWithAudit(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookPublicationRequest{}, &model.PublishedBookAsset{}, &model.BookPublicationAppeal{}, &model.AuditLog{})
	owner := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	moderator := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleModerator}
	request := model.BookPublicationRequest{SubmittedBy: owner.ID, AssetID: uuid.New(), Status: model.BookPublicationStatusPublished}
	require.NoError(t, db.Create(&request).Error)
	asset := model.PublishedBookAsset{PublicationRequestID: request.ID, SourceAssetID: uuid.New(), Format: "pdf", ObjectKey: "books/public/assets/asset.pdf", Status: model.BookPublicationStatusRemoved}
	require.NoError(t, db.Create(&asset).Error)

	service := NewService(db)
	appeal, err := service.SubmitPublicationAppeal(owner, request.ID, "授权链路已核实，请恢复公开正文")
	require.NoError(t, err)
	require.Equal(t, model.BookPublicationAppealStatusPending, appeal.Status)
	_, err = service.SubmitPublicationAppeal(owner, request.ID, "重复申诉")
	require.Error(t, err)
	_, err = service.ReviewPublicationAppeal(owner, uuid.MustParse(appeal.ID), model.BookPublicationAppealStatusApproved, "self review")
	require.Error(t, err)

	approved, err := service.ReviewPublicationAppeal(moderator, uuid.MustParse(appeal.ID), model.BookPublicationAppealStatusApproved, "rights rechecked")
	require.NoError(t, err)
	require.Equal(t, model.BookPublicationAppealStatusApproved, approved.Status)
	var restored model.PublishedBookAsset
	require.NoError(t, db.First(&restored, "id = ?", asset.ID).Error)
	require.Equal(t, model.BookPublicationStatusPublished, restored.Status)
	require.Nil(t, restored.RemovedAt)
	var auditCount int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("entity_type = ? AND entity_id = ?", "book_publication_appeal", appeal.ID).Count(&auditCount).Error)
	require.Equal(t, int64(2), auditCount)
}

func TestPublicationRequestRequiresRightsAndCreatesIndependentPublicAsset(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookWork{}, &model.BookEdition{}, &model.BookPerson{}, &model.BookContribution{}, &model.BookSource{}, &model.BookRating{}, &model.UserBookImport{}, &model.UserBookAsset{}, &model.BookPublicationRequest{}, &model.BookRightsDeclaration{}, &model.PublishedBookAsset{}, &model.UserBookReadingState{})
	store := &fakeBookUploadStore{objects: map[string][]byte{}}
	service := NewService(db).WithBookUploadStore(store)
	owner := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	moderator := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleModerator}
	work := model.BookWork{Title: "Public domain work", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}
	require.NoError(t, db.Create(&work).Error)
	importID, assetID := uuid.New(), uuid.New()
	sourceKey := "books/private/source.txt"
	body := []byte("public domain text")
	store.objects[sourceKey] = body
	require.NoError(t, db.Create(&model.UserBookImport{Base: model.Base{ID: importID}, UserID: owner.ID, Title: "Private source", OriginalFilename: "source.txt", Format: "txt", ContentType: "text/plain", SizeBytes: int64(len(body)), ObjectKey: sourceKey, Status: model.BookImportStatusMetadataReady}).Error)
	require.NoError(t, db.Create(&model.UserBookAsset{Base: model.Base{ID: assetID}, ImportID: importID, UserID: owner.ID, OriginalFilename: "source.txt", Format: "txt", ContentType: "text/plain", SizeBytes: int64(len(body)), ObjectKey: sourceKey, ProcessingStatus: model.BookAssetStatusPrivateAvailable}).Error)

	_, err := service.SubmitPublicationRequest(owner, assetID, SubmitPublicationInput{WorkID: work.ID.String(), LicenseType: "public_domain", Declaration: "missing source"})
	require.Error(t, err)
	request, err := service.SubmitPublicationRequest(owner, assetID, SubmitPublicationInput{WorkID: work.ID.String(), LicenseType: "public_domain", RightsHolder: "Public domain", SourceURL: "https://example.test/rights", Declaration: "This work is in the public domain."})
	require.NoError(t, err)
	require.Equal(t, model.BookPublicationStatusPendingReview, request.Status)

	approved, err := service.ReviewPublicationRequest(moderator, uuid.MustParse(request.ID), model.BookPublicationStatusPublished, "rights verified")
	require.NoError(t, err)
	require.Equal(t, model.BookPublicationStatusPublished, approved.Status)
	published, total, err := service.ListPublishedBookAssets(context.Background(), work.ID, uuid.Nil, 20, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, published, 1)
	require.NotEqual(t, sourceKey, published[0].FileName)
	publicID := uuid.MustParse(published[0].ID)
	var publishedRow model.PublishedBookAsset
	require.NoError(t, db.First(&publishedRow, "id = ?", publicID).Error)
	require.NotEqual(t, sourceKey, publishedRow.ObjectKey)
	_, bodyReader, err := service.OpenPublishedBookAsset(context.Background(), publicID)
	require.NoError(t, err)
	readBody, err := io.ReadAll(bodyReader)
	require.NoError(t, err)
	require.NoError(t, bodyReader.Close())
	require.Equal(t, body, readBody)

	require.NoError(t, service.DeleteBookImport(owner, importID))
	_, bodyReader, err = service.OpenPublishedBookAsset(context.Background(), publicID)
	require.NoError(t, err)
	readBody, err = io.ReadAll(bodyReader)
	require.NoError(t, err)
	require.NoError(t, bodyReader.Close())
	require.Equal(t, body, readBody)
}
