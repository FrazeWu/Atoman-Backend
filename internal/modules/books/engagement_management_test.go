package books

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBookEngagementManagementSupportsVoteReviewDeleteAndReport(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookWork{}, &model.BookRating{}, &model.BookReview{}, &model.BookEdit{}, &model.BookEditVote{}, &model.UserBookImport{}, &model.UserBookAsset{}, &model.BookPublicationRequest{}, &model.PublishedBookAsset{}, &model.BookPublicationReport{})
	service := NewService(db)
	owner := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	voter := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	moderator := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleModerator}
	work := model.BookWork{Title: "Work", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}
	require.NoError(t, db.Create(&work).Error)

	require.NoError(t, db.Create(&model.BookReview{UserID: owner.ID, WorkID: work.ID, Content: "review", Visibility: model.BookReviewVisibilityPublic}).Error)
	require.NoError(t, service.DeleteBookReview(owner, work.ID))
	require.NoError(t, db.Create(&model.BookReview{UserID: owner.ID, WorkID: work.ID, Content: "review again", Visibility: model.BookReviewVisibilityPublic}).Error)

	edit := model.BookEdit{SubmittedBy: owner.ID, Type: model.BookEditTypeUpdate, EntityType: "work", EntityID: &work.ID, Status: model.BookEditStatusPending, PayloadJSON: `{"description":"updated"}`, ChangesJSON: `{}`}
	require.NoError(t, db.Create(&edit).Error)
	voted, err := service.VoteBookEdit(voter, edit.ID, model.BookEditVoteUp)
	require.NoError(t, err)
	require.Equal(t, int64(1), voted.UpvoteCount)
	voted, err = service.VoteBookEdit(voter, edit.ID, model.BookEditVoteDown)
	require.NoError(t, err)
	require.Equal(t, int64(1), voted.DownvoteCount)

	importID, sourceAssetID, publicAssetID := uuid.New(), uuid.New(), uuid.New()
	require.NoError(t, db.Create(&model.UserBookImport{Base: model.Base{ID: importID}, UserID: owner.ID, Title: "Source", OriginalFilename: "source.txt", Format: "txt", ContentType: "text/plain", Status: model.BookImportStatusMetadataReady}).Error)
	require.NoError(t, db.Create(&model.UserBookAsset{Base: model.Base{ID: sourceAssetID}, ImportID: importID, UserID: owner.ID, OriginalFilename: "source.txt", Format: "txt", ContentType: "text/plain", ProcessingStatus: model.BookAssetStatusPrivateAvailable}).Error)
	require.NoError(t, db.Create(&model.PublishedBookAsset{Base: model.Base{ID: publicAssetID}, SourceAssetID: sourceAssetID, WorkID: &work.ID, Format: "txt", ObjectKey: "public/source.txt", Status: model.BookPublicationStatusPublished}).Error)
	report, err := service.ReportPublishedBookAsset(voter, publicAssetID, "rights concern")
	require.NoError(t, err)
	_, err = service.ReviewPublicationReport(moderator, uuid.MustParse(report.ID), model.BookPublicationReportStatusRemoved, "removed")
	require.NoError(t, err)
	var published model.PublishedBookAsset
	require.NoError(t, db.First(&published, "id = ?", publicAssetID).Error)
	require.Equal(t, model.BookPublicationStatusRemoved, published.Status)
}
