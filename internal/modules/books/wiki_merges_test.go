package books

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func newBookMergeTestService(t *testing.T) (*Service, authctx.CurrentUser, BookWorkDetail, BookWorkDetail) {
	t.Helper()
	service, user := newBookWikiTestService(t)
	testdb.Migrate(t, service.db, &model.BookReview{}, &model.UserBookShelf{}, &model.BookPostLink{})
	require.NoError(t, migrations.RunBooksV2Migration(service.db))
	ctx := context.Background()
	person, err := service.CreateBookPerson(ctx, user, CreateBookPersonInput{Name: "共同作者"})
	require.NoError(t, err)
	source, err := service.CreateBookWork(ctx, user, CreateBookWorkInput{Title: "重复作品", AuthorIDs: []uuid.UUID{person.ID}})
	require.NoError(t, err)
	target, err := service.CreateBookWork(ctx, user, CreateBookWorkInput{Title: "规范作品", AuthorIDs: []uuid.UUID{person.ID}})
	require.NoError(t, err)
	return service, user, source, target
}

func mergeBookTestWorks(t *testing.T, service *Service, user authctx.CurrentUser, source, target BookWorkDetail) model.BookWikiMerge {
	t.Helper()
	ids := PreviewBookWorkMergeInput{SourceID: uuid.MustParse(source.ID), TargetID: uuid.MustParse(target.ID)}
	preview, err := service.PreviewBookWorkMerge(context.Background(), user, ids)
	require.NoError(t, err)
	merge, err := service.MergeBookWorks(context.Background(), user, MergeBookWorksInput{PreviewBookWorkMergeInput: ids, PreviewToken: preview.PreviewToken, SourceRevisionID: preview.SourceRevisionID, TargetRevisionID: preview.TargetRevisionID})
	require.NoError(t, err)
	return merge
}

func TestBookWorkMergeRestoresRelationsWithoutDuplicatingRatings(t *testing.T) {
	service, user, source, target := newBookMergeTestService(t)
	ctx := context.Background()
	sourceID, targetID := uuid.MustParse(source.ID), uuid.MustParse(target.ID)
	for _, rating := range []model.BookRating{{UserID: user.ID, WorkID: sourceID, Score: 4}, {UserID: user.ID, WorkID: targetID, Score: 8}, {UserID: uuid.New(), WorkID: sourceID, Score: 6}} {
		require.NoError(t, service.db.Create(&rating).Error)
	}
	for _, review := range []model.BookReview{{UserID: user.ID, WorkID: sourceID, Content: "PRIVATE SOURCE REVIEW", Visibility: "private"}, {UserID: user.ID, WorkID: targetID, Content: "PRIVATE TARGET REVIEW", Visibility: "private"}} {
		require.NoError(t, service.db.Create(&review).Error)
	}
	for _, shelf := range []model.UserBookShelf{{UserID: user.ID, WorkID: sourceID, Status: "reading", Note: "source private note"}, {UserID: user.ID, WorkID: targetID, Status: "read", Note: "target private note"}} {
		require.NoError(t, service.db.Create(&shelf).Error)
	}
	postID := uuid.New()
	for _, workID := range []uuid.UUID{sourceID, targetID} {
		require.NoError(t, service.db.Create(&model.BookPostLink{WorkID: workID, PostID: postID}).Error)
	}
	_, err := service.CreateBookEdition(ctx, user, sourceID, CreateBookEditionInput{Title: "独立出版版本"})
	require.NoError(t, err)
	var assetIDs, revisionIDs []uuid.UUID
	for i, workID := range []uuid.UUID{sourceID, targetID} {
		asset := model.BookAsset{WorkID: workID, Language: "zh", Format: "epub", CreatedBy: user.ID}
		require.NoError(t, service.db.Create(&asset).Error)
		media := model.MediaAsset{Purpose: "books.file", Key: "private/" + workID.String(), ContentType: "application/epub+zip", Size: 12}
		require.NoError(t, service.db.Create(&media).Error)
		revision := model.BookAssetRevision{AssetID: asset.ID, Number: 1, MediaAssetID: media.ID, SHA256: strings.Repeat("a", 64), OriginalFilename: "book.epub", Status: "ready", CreatedBy: user.ID}
		require.NoError(t, service.db.Create(&revision).Error)
		require.NoError(t, service.db.Model(&asset).Update("current_revision_id", revision.ID).Error)
		require.NoError(t, service.db.Create(&model.BookDefaultAsset{WorkID: workID, Language: "zh", AssetID: asset.ID, UpdatedBy: user.ID}).Error)
		require.NoError(t, service.db.Create(&model.BookReadingSelection{UserID: user.ID, WorkID: workID, AssetID: asset.ID, RevisionID: revision.ID}).Error)
		require.NoError(t, service.db.Create(&model.BookReadingState{UserID: user.ID, RevisionID: revision.ID, CurrentPercent: float64(i) / 2, FurthestPercent: .7}).Error)
		assetIDs = append(assetIDs, asset.ID)
		revisionIDs = append(revisionIDs, revision.ID)
	}
	merge := mergeBookTestWorks(t, service, user, source, target)
	encoded, err := json.Marshal(merge)
	require.NoError(t, err)
	for _, private := range []string{"before_json", "after_json", "PRIVATE SOURCE REVIEW", "PRIVATE TARGET REVIEW", "private note"} {
		require.NotContains(t, string(encoded), private)
	}
	canonical, err := service.GetBookWork(ctx, sourceID)
	require.NoError(t, err)
	require.Equal(t, target.ID, canonical.ID)
	require.Equal(t, source.ID, canonical.RedirectedFrom)
	require.Len(t, canonical.Authors, 1)
	require.Len(t, canonical.Editions, 1)
	require.EqualValues(t, 2, canonical.RatingCount)
	require.Equal(t, 7.0, canonical.RatingScore)
	var assets []model.BookAsset
	require.NoError(t, service.db.Where("work_id = ?", targetID).Find(&assets).Error)
	require.Len(t, assets, 2, "both ebook files must survive the merge")
	var selection model.BookReadingSelection
	require.NoError(t, service.db.Where("user_id = ? AND work_id = ?", user.ID, targetID).First(&selection).Error)
	require.Equal(t, revisionIDs[1], selection.RevisionID, "keep the target's explicit selection")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/books"), service)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/books/works/"+source.ID, nil))
	require.Equal(t, http.StatusPermanentRedirect, response.Code)
	require.Equal(t, "/api/v1/books/works/"+target.ID, response.Header().Get("Location"))
	// Reading after a merge must neither block undo nor be rolled back by it.
	require.NoError(t, service.db.Model(&model.BookReadingState{}).Where("user_id = ? AND revision_id = ?", user.ID, revisionIDs[0]).Update("current_percent", .3).Error)
	reverted, err := service.RevertBookWorkMerge(ctx, user, merge.ID)
	require.NoError(t, err)
	require.Equal(t, "reverted", reverted.Status)
	restored, err := service.GetBookWork(ctx, sourceID)
	require.NoError(t, err)
	require.Equal(t, source.ID, restored.ID)
	require.Empty(t, restored.RedirectedFrom)
	for i, workID := range []uuid.UUID{sourceID, targetID} {
		var selected model.BookReadingSelection
		require.NoError(t, service.db.Where("user_id = ? AND work_id = ?", user.ID, workID).First(&selected).Error)
		require.Equal(t, revisionIDs[i], selected.RevisionID)
		var asset model.BookAsset
		require.NoError(t, service.db.First(&asset, "id = ?", assetIDs[i]).Error)
		require.Equal(t, workID, asset.WorkID)
	}
	var state model.BookReadingState
	require.NoError(t, service.db.First(&state, "user_id = ? AND revision_id = ?", user.ID, revisionIDs[0]).Error)
	require.Equal(t, .3, state.CurrentPercent)
	var ratings []model.BookRating
	require.NoError(t, service.db.Order("score ASC").Find(&ratings).Error)
	require.Len(t, ratings, 3)
	require.Equal(t, []int{4, 6, 8}, []int{ratings[0].Score, ratings[1].Score, ratings[2].Score})
	var privateReviews []model.BookReview
	require.NoError(t, service.db.Find(&privateReviews).Error)
	require.Len(t, privateReviews, 2)
	_, err = service.RevertBookWorkMerge(ctx, user, merge.ID)
	require.Equal(t, "books.merge_already_reverted", apperr.FromError(err).Code)
}

func TestBookMergePreviewAndUndoRejectNewerWrites(t *testing.T) {
	service, user, source, target := newBookMergeTestService(t)
	ctx := context.Background()
	ids := PreviewBookWorkMergeInput{SourceID: uuid.MustParse(source.ID), TargetID: uuid.MustParse(target.ID)}
	preview, err := service.PreviewBookWorkMerge(ctx, user, ids)
	require.NoError(t, err)
	rating := model.BookRating{UserID: user.ID, WorkID: ids.SourceID, Score: 5}
	require.NoError(t, service.db.Create(&rating).Error)
	_, err = service.MergeBookWorks(ctx, user, MergeBookWorksInput{PreviewBookWorkMergeInput: ids, PreviewToken: preview.PreviewToken, SourceRevisionID: preview.SourceRevisionID, TargetRevisionID: preview.TargetRevisionID})
	require.Equal(t, "books.merge_preview_stale", apperr.FromError(err).Code)
	merge := mergeBookTestWorks(t, service, user, source, target)
	require.NoError(t, service.db.Model(&model.BookRating{}).Where("id = ?", rating.ID).Update("score", 9).Error)
	_, err = service.RevertBookWorkMerge(ctx, user, merge.ID)
	require.Equal(t, "books.merge_revert_conflict", apperr.FromError(err).Code)
	var latest model.BookRating
	require.NoError(t, service.db.First(&latest, "id = ?", rating.ID).Error)
	require.Equal(t, 9, latest.Score)
	require.Equal(t, ids.TargetID, latest.WorkID)
}

func TestBookMergeUndoRejectsLaterWikiRevision(t *testing.T) {
	service, user, source, target := newBookMergeTestService(t)
	merge := mergeBookTestWorks(t, service, user, source, target)
	unchanged, err := service.PatchBookWork(context.Background(), user, uuid.MustParse(target.ID), PatchBookWorkInput{BaseRevisionID: merge.TargetRevisionID, Title: &target.Title})
	require.NoError(t, err)
	require.Equal(t, merge.TargetRevisionID, unchanged.RevisionID, "a no-op edit must not create a revision")
	title := "合并后的新标题"
	_, err = service.PatchBookWork(context.Background(), user, uuid.MustParse(target.ID), PatchBookWorkInput{BaseRevisionID: merge.TargetRevisionID, Title: &title})
	require.NoError(t, err)
	_, err = service.RevertBookWorkMerge(context.Background(), user, merge.ID)
	require.Equal(t, "books.merge_revert_conflict", apperr.FromError(err).Code)
}
