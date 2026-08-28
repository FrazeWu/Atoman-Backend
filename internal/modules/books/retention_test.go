package books

import (
	"context"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProcessBookRetentionRemovesExpiredEvidenceAndAppealMaterials(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookPublicationRequest{}, &model.BookRightsDeclaration{}, &model.BookPublicationAppeal{}, &model.AuditLog{})
	store := &fakeBookUploadStore{objects: map[string][]byte{}}
	service := NewService(db).WithBookUploadStore(store)
	now := time.Now().UTC()
	old := now.AddDate(-2, 0, -1)
	recent := now.AddDate(-2, 0, 1)
	recentAppeal := now.AddDate(-1, 0, 0)
	owner := uuid.New()

	expired := createBookRetentionRequest(t, db, owner, old, "books/private/publication-evidence/expired.pdf", false)
	require.NoError(t, db.Create(&model.BookPublicationAppeal{
		PublicationRequestID: expired.ID,
		PublishedAssetID:     uuid.New(),
		SubmittedBy:          owner,
		Reason:               "expired appeal",
		Status:               model.BookPublicationAppealStatusRejected,
		ReviewedAt:           &old,
	}).Error)
	recentRequest := createBookRetentionRequest(t, db, owner, recent, "books/private/publication-evidence/recent.pdf", false)
	recentAppealRequest := createBookRetentionRequest(t, db, owner, old, "books/private/publication-evidence/recent-appeal.pdf", false)
	require.NoError(t, db.Create(&model.BookPublicationAppeal{
		PublicationRequestID: recentAppealRequest.ID,
		PublishedAssetID:     uuid.New(),
		SubmittedBy:          owner,
		Reason:               "recently resolved appeal",
		Status:               model.BookPublicationAppealStatusApproved,
		ReviewedAt:           &recentAppeal,
	}).Error)
	pendingRequest := createBookRetentionRequest(t, db, owner, old, "books/private/publication-evidence/pending.pdf", false)
	require.NoError(t, db.Create(&model.BookPublicationAppeal{
		PublicationRequestID: pendingRequest.ID,
		PublishedAssetID:     uuid.New(),
		SubmittedBy:          owner,
		Reason:               "pending appeal",
		Status:               model.BookPublicationAppealStatusPending,
	}).Error)
	holdRequest := createBookRetentionRequest(t, db, owner, old, "books/private/publication-evidence/hold.pdf", true)

	cleaned, err := service.ProcessBookRetention(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, cleaned)
	require.ElementsMatch(t, []string{"books/private/publication-evidence/expired.pdf"}, store.deletedKeys)

	var rights model.BookRightsDeclaration
	require.NoError(t, db.First(&rights, "request_id = ?", expired.ID).Error)
	require.Empty(t, rights.EvidenceObjectKey)
	require.Empty(t, rights.EvidenceFileName)
	require.NotNil(t, rights.EvidenceDeletedAt)
	var appeals []model.BookPublicationAppeal
	require.NoError(t, db.Where("publication_request_id = ?", expired.ID).Find(&appeals).Error)
	require.Empty(t, appeals)

	for _, request := range []model.BookPublicationRequest{recentRequest, recentAppealRequest, pendingRequest, holdRequest} {
		var remainingRights model.BookRightsDeclaration
		require.NoError(t, db.First(&remainingRights, "request_id = ?", request.ID).Error)
		require.NotEmpty(t, remainingRights.EvidenceObjectKey)
	}
	var auditCount int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "books.retention.cleanup").Count(&auditCount).Error)
	require.Equal(t, int64(1), auditCount)
}

func TestSetPublicationRetentionHoldRequiresReviewerAndWritesAudit(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookPublicationRequest{}, &model.AuditLog{})
	owner := uuid.New()
	moderator := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleModerator}
	request := model.BookPublicationRequest{SubmittedBy: owner, AssetID: uuid.New(), Status: model.BookPublicationStatusPublished}
	require.NoError(t, db.Create(&request).Error)
	service := NewService(db)

	hold, err := service.SetPublicationRetentionHold(moderator, request.ID, true, "legal preservation request")
	require.NoError(t, err)
	require.True(t, hold.Held)
	require.Equal(t, "legal preservation request", hold.Reason)

	var persisted model.BookPublicationRequest
	require.NoError(t, db.First(&persisted, "id = ?", request.ID).Error)
	require.True(t, persisted.RetentionHold)
	require.NotNil(t, persisted.RetentionHoldSetAt)

	hold, err = service.SetPublicationRetentionHold(moderator, request.ID, false, "preservation released")
	require.NoError(t, err)
	require.False(t, hold.Held)
	require.Empty(t, hold.Reason)
	var released model.BookPublicationRequest
	require.NoError(t, db.First(&released, "id = ?", request.ID).Error)
	require.False(t, released.RetentionHold)
	require.Nil(t, released.RetentionHoldSetAt)

	var auditCount int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("entity_type = ? AND entity_id = ?", "book_publication_request", request.ID).Count(&auditCount).Error)
	require.Equal(t, int64(2), auditCount)
}

func createBookRetentionRequest(t *testing.T, db *gorm.DB, owner uuid.UUID, reviewedAt time.Time, evidenceKey string, retentionHold bool) model.BookPublicationRequest {
	t.Helper()
	request := model.BookPublicationRequest{
		SubmittedBy:   owner,
		AssetID:       uuid.New(),
		Status:        model.BookPublicationStatusPublished,
		ReviewedAt:    &reviewedAt,
		RetentionHold: retentionHold,
	}
	require.NoError(t, db.Create(&request).Error)
	require.NoError(t, db.Create(&model.BookRightsDeclaration{
		RequestID:           request.ID,
		LicenseType:         "authorized_distribution",
		Declaration:         "retention test",
		EvidenceObjectKey:   evidenceKey,
		EvidenceFileName:    "evidence.pdf",
		EvidenceContentType: "application/pdf",
		EvidenceSizeBytes:   12,
	}).Error)
	return request
}
