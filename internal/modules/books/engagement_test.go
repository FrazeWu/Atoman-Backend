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

func TestBookRatingsUpsertAndReviewsRespectPublicVisibility(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookWork{}, &model.BookRating{}, &model.BookReview{})
	work := model.BookWork{Base: model.Base{ID: uuid.New()}, Title: "Rated Work", LifecycleStatus: model.BookLifecycleStatusActive, EditStatus: model.BookEditStatusDevelopment}
	require.NoError(t, db.Create(&work).Error)
	owner := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	other := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	service := NewService(db)

	first, err := service.SetBookRating(owner, work.ID, 4)
	require.NoError(t, err)
	require.Equal(t, 4.0, first.RatingScore)
	require.Equal(t, int64(1), first.RatingCount)
	second, err := service.SetBookRating(owner, work.ID, 5)
	require.NoError(t, err)
	require.Equal(t, 5.0, second.RatingScore)
	require.Equal(t, int64(1), second.RatingCount)
	_, err = service.SetBookRating(other, work.ID, 4)
	require.NoError(t, err)
	summary, err := service.BookRatingSummary(context.Background(), work.ID, &owner.ID)
	require.NoError(t, err)
	require.Equal(t, 4.5, summary.RatingScore)
	require.Equal(t, int64(2), summary.RatingCount)
	require.NotNil(t, summary.ViewerRating)
	require.Equal(t, 5, *summary.ViewerRating)

	privateReview, err := service.SaveBookReview(owner, work.ID, SaveBookReviewInput{Content: "private review", Visibility: model.BookReviewVisibilityPrivate})
	require.NoError(t, err)
	require.Equal(t, owner.ID.String(), privateReview.AuthorID)
	privateList, total, err := service.ListPublicBookReviews(context.Background(), work.ID, 20, 0)
	require.NoError(t, err)
	require.Empty(t, privateList)
	require.Zero(t, total)

	var persisted []model.BookReview
	require.NoError(t, db.Where("user_id = ? AND work_id = ?", owner.ID, work.ID).Find(&persisted).Error)
	require.Len(t, persisted, 1)
	publicReview, err := service.SaveBookReview(owner, work.ID, SaveBookReviewInput{Content: "public review", Spoiler: true, Visibility: model.BookReviewVisibilityPublic})
	require.NoError(t, err)
	require.Equal(t, privateReview.ID, publicReview.ID)
	publicList, total, err := service.ListPublicBookReviews(context.Background(), work.ID, 20, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Equal(t, "public review", publicList[0].Content)
	require.True(t, publicList[0].Spoiler)

	_, err = service.SetBookRating(owner, uuid.New(), 5)
	require.Error(t, err)
	_, err = service.SaveBookReview(owner, work.ID, SaveBookReviewInput{Content: "", Visibility: model.BookReviewVisibilityPublic})
	require.Error(t, err)
}
