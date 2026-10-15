package books

import (
	"context"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBookContributionRequiresSourceAndAutoAppliesCreate(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookWork{}, &model.BookEdition{}, &model.BookPerson{}, &model.BookSource{}, &model.BookEdit{}, &model.AuditLog{})
	service := NewService(db)
	submitter := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}

	_, err := service.SubmitBookEdit(submitter, SubmitBookEditInput{Type: model.BookEditTypeCreate, EntityType: "work", Payload: map[string]any{"title": "No source"}})
	require.Error(t, err)

	edit, err := service.SubmitBookEdit(submitter, SubmitBookEditInput{
		Type: model.BookEditTypeCreate, EntityType: "work", Reason: "Add a public work",
		Payload: map[string]any{"title": "Reviewed work", "description": "Description"},
		Sources: []BookEditSourceInput{{URL: "https://example.test/books/reviewed-work", Title: "Publisher"}},
	})
	require.NoError(t, err)
	require.Equal(t, model.BookEditStatusApproved, edit.Status)
	require.NotEmpty(t, edit.EntityID)

	own, total, err := service.ListBookEdits(context.Background(), submitter, false, 20, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, own, 1)
	var work model.BookWork
	require.NoError(t, db.First(&work, "id = ?", edit.EntityID).Error)
	require.Equal(t, model.BookLifecycleStatusActive, work.LifecycleStatus)
	var source model.BookSource
	require.NoError(t, db.Where("book_edit_id = ?", edit.ID).First(&source).Error)
	require.Equal(t, work.ID, source.TargetID)
}

func TestAttachWorkEditionKeepsPublisherAndProducerSeparate(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookWork{}, &model.BookEdition{}, &model.BookPublisher{}, &model.BookProducer{})
	work := model.BookWork{Title: "中国历代政治得失", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}
	require.NoError(t, db.Create(&work).Error)

	err := attachWorkEdition(db, work, uuid.New(), map[string]any{
		"publisher": "生活·读书·新知三联书店",
		"producer":  "后浪出版公司",
	})
	require.NoError(t, err)

	var edition model.BookEdition
	require.NoError(t, db.Where("work_id = ?", work.ID).First(&edition).Error)
	require.Equal(t, "生活·读书·新知三联书店", edition.Publisher)
	require.Equal(t, "后浪出版公司", edition.Producer)
	require.NotNil(t, edition.PublisherID)
	require.NotNil(t, edition.ProducerID)
}

func TestBookWorkMergeMovesPrivateInteractionsAndRedirectsOldWork(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookWork{}, &model.BookEdition{}, &model.BookContribution{}, &model.BookSource{}, &model.BookEdit{}, &model.AuditLog{}, &model.BookRating{}, &model.BookReview{}, &model.UserBookShelf{}, &model.BookPostLink{}, &model.UserBookImport{}, &model.UserBookAsset{}, &model.BookPublicationRequest{}, &model.PublishedBookAsset{})
	service := NewService(db)
	submitter := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	moderator := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleModerator}
	oldWork := model.BookWork{Title: "Old", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}
	canonical := model.BookWork{Title: "Canonical", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}
	require.NoError(t, db.Create(&oldWork).Error)
	require.NoError(t, db.Create(&canonical).Error)
	require.NoError(t, db.Create(&model.BookRating{UserID: submitter.ID, WorkID: oldWork.ID, Score: 5}).Error)
	require.NoError(t, db.Create(&model.BookReview{UserID: submitter.ID, WorkID: oldWork.ID, Content: "keep", Visibility: model.BookReviewVisibilityPublic}).Error)
	require.NoError(t, db.Create(&model.UserBookShelf{UserID: submitter.ID, WorkID: oldWork.ID, Status: model.BookShelfStatusReading}).Error)
	importID, assetID := uuid.New(), uuid.New()
	require.NoError(t, db.Create(&model.UserBookImport{Base: model.Base{ID: importID}, UserID: submitter.ID, Title: "Uploaded", OriginalFilename: "uploaded.epub", Format: "epub", ContentType: "application/epub+zip", SizeBytes: 10, ObjectKey: "private/uploaded.epub", Status: model.BookImportStatusMetadataReady, WorkID: &oldWork.ID}).Error)
	require.NoError(t, db.Create(&model.UserBookAsset{Base: model.Base{ID: assetID}, ImportID: importID, UserID: submitter.ID, OriginalFilename: "uploaded.epub", ContentType: "application/epub+zip", Format: "epub", SizeBytes: 10, SHA256: "sha", ObjectKey: "private/uploaded.epub", ProcessingStatus: model.BookAssetStatusPrivateAvailable}).Error)
	request := model.BookPublicationRequest{SubmittedBy: submitter.ID, AssetID: assetID, WorkID: &oldWork.ID, Status: model.BookPublicationStatusPublished}
	require.NoError(t, db.Create(&request).Error)
	publishedAsset := model.PublishedBookAsset{PublicationRequestID: request.ID, SourceAssetID: assetID, WorkID: &oldWork.ID, Format: "epub", ObjectKey: "public/uploaded.epub", SHA256: "sha", Status: model.BookPublicationStatusPublished}
	require.NoError(t, db.Create(&publishedAsset).Error)
	require.NoError(t, db.Create(&model.BookSource{TargetType: "work", TargetID: oldWork.ID, URL: "https://example.test/work"}).Error)
	require.NoError(t, db.Create(&model.BookSource{TargetType: "book_work", TargetID: oldWork.ID, URL: "https://example.test/legacy-work"}).Error)

	edit, err := service.SubmitBookEdit(submitter, SubmitBookEditInput{
		Type: model.BookEditTypeMerge, EntityType: "work", EntityID: oldWork.ID.String(),
		Payload: map[string]any{"redirect_to": canonical.ID.String()},
		Sources: []BookEditSourceInput{{URL: "https://example.test/merge"}},
	})
	require.NoError(t, err)
	_, err = service.ReviewBookEdit(moderator, uuid.MustParse(edit.ID), model.BookEditStatusApproved, "duplicate work")
	require.NoError(t, err)

	var merged model.BookWork
	require.NoError(t, db.First(&merged, "id = ?", oldWork.ID).Error)
	require.Equal(t, model.BookLifecycleStatusMerged, merged.LifecycleStatus)
	require.Equal(t, canonical.ID, *merged.RedirectTo)
	var rating model.BookRating
	require.NoError(t, db.Where("user_id = ? AND work_id = ?", submitter.ID, canonical.ID).First(&rating).Error)
	var review model.BookReview
	require.NoError(t, db.Where("user_id = ? AND work_id = ?", submitter.ID, canonical.ID).First(&review).Error)
	var shelf model.UserBookShelf
	require.NoError(t, db.Where("user_id = ? AND work_id = ?", submitter.ID, canonical.ID).First(&shelf).Error)
	var migratedImport model.UserBookImport
	require.NoError(t, db.First(&migratedImport, "id = ?", importID).Error)
	require.NotNil(t, migratedImport.WorkID)
	require.Equal(t, canonical.ID, *migratedImport.WorkID)
	var migratedPublished model.PublishedBookAsset
	require.NoError(t, db.First(&migratedPublished, "id = ?", publishedAsset.ID).Error)
	require.NotNil(t, migratedPublished.WorkID)
	require.Equal(t, canonical.ID, *migratedPublished.WorkID)
	var migratedRequest model.BookPublicationRequest
	require.NoError(t, db.First(&migratedRequest, "id = ?", request.ID).Error)
	require.NotNil(t, migratedRequest.WorkID)
	require.Equal(t, canonical.ID, *migratedRequest.WorkID)
	var sourceCount int64
	require.NoError(t, db.Model(&model.BookSource{}).Where("target_type IN ? AND target_id = ?", []string{"work", "book_work"}, canonical.ID).Count(&sourceCount).Error)
	require.Equal(t, int64(3), sourceCount)
	redirected, err := service.GetPublicWork(context.Background(), oldWork.ID)
	require.NoError(t, err)
	require.Equal(t, canonical.ID.String(), redirected.ID)
	require.Equal(t, oldWork.ID.String(), redirected.RedirectedFrom)
}
