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

func TestBookContributionRequiresSourceAndSupportsReviewWorkflow(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookWork{}, &model.BookEdition{}, &model.BookPerson{}, &model.BookSource{}, &model.BookEdit{}, &model.AuditLog{})
	service := NewService(db)
	submitter := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	moderator := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleModerator}

	_, err := service.SubmitBookEdit(submitter, SubmitBookEditInput{Type: model.BookEditTypeCreate, EntityType: "work", Payload: map[string]any{"title": "No source"}})
	require.Error(t, err)

	edit, err := service.SubmitBookEdit(submitter, SubmitBookEditInput{
		Type: model.BookEditTypeCreate, EntityType: "work", Reason: "Add a public work",
		Payload: map[string]any{"title": "Reviewed work", "description": "Description"},
		Sources: []BookEditSourceInput{{URL: "https://example.test/books/reviewed-work", Title: "Publisher"}},
	})
	require.NoError(t, err)
	require.Equal(t, model.BookEditStatusPending, edit.Status)

	own, total, err := service.ListBookEdits(context.Background(), submitter, false, 20, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, own, 1)
	_, err = service.ReviewBookEdit(submitter, uuid.MustParse(edit.ID), model.BookEditStatusApproved, "self review")
	require.Error(t, err)

	approved, err := service.ReviewBookEdit(moderator, uuid.MustParse(edit.ID), model.BookEditStatusApproved, "source checked")
	require.NoError(t, err)
	require.Equal(t, model.BookEditStatusApproved, approved.Status)
	require.NotEmpty(t, approved.EntityID)
	var work model.BookWork
	require.NoError(t, db.First(&work, "id = ?", approved.EntityID).Error)
	require.Equal(t, model.BookLifecycleStatusActive, work.LifecycleStatus)
	var source model.BookSource
	require.NoError(t, db.Where("book_edit_id = ?", edit.ID).First(&source).Error)
	require.Equal(t, work.ID, source.TargetID)
	var auditEntry model.AuditLog
	require.NoError(t, db.Where("entity_type = ? AND entity_id = ?", "book_edit", uuid.MustParse(edit.ID)).First(&auditEntry).Error)
	require.Equal(t, "books.edit.approved", auditEntry.Action)
}

func TestBookWorkMergeMovesPrivateInteractionsAndRedirectsOldWork(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookWork{}, &model.BookEdition{}, &model.BookContribution{}, &model.BookSource{}, &model.BookEdit{}, &model.AuditLog{}, &model.BookRating{}, &model.BookReview{}, &model.UserBookShelf{}, &model.BookPostLink{})
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
	redirected, err := service.GetPublicWork(context.Background(), oldWork.ID)
	require.NoError(t, err)
	require.Equal(t, canonical.ID.String(), redirected.ID)
	require.Equal(t, oldWork.ID.String(), redirected.RedirectedFrom)
}
