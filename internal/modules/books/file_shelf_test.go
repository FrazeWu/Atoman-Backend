package books

import (
	"context"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestEditionBestFilePreservesShelfAndHistory(t *testing.T) {
	s, user, work, store := newBookResourceTestService(t)
	ctx := context.Background()
	workID := uuid.MustParse(work.ID)
	edition, err := s.CreateBookEdition(ctx, user, workID, CreateBookEditionInput{Language: "zh", Publisher: "测试出版社", EditionStatement: "初版"})
	require.NoError(t, err)
	editionID := uuid.MustParse(edition.ID)
	firstAsset, err := s.CreateBookResource(ctx, user, workID, CreateBookResourceInput{EditionID: &editionID, Language: "zh", Format: "txt"})
	require.NoError(t, err)
	session := queueBookTestFile(t, s, user, store, firstAsset, "old.txt", "text/plain", []byte("old file content"))
	_, err = s.ProcessBookResourceUploads(ctx)
	require.NoError(t, err)
	oldFile, err := s.GetBookResourceUpload(ctx, user, session.ID)
	require.NoError(t, err)
	require.NotNil(t, oldFile.RevisionID)
	firstBest := model.BookEditionBestFile{}
	require.NoError(t, s.db.First(&firstBest, "edition_id = ?", editionID).Error)
	require.Equal(t, *oldFile.RevisionID, firstBest.RevisionID)

	shelf, err := s.SaveBookFileShelf(ctx, user, *oldFile.RevisionID, SaveBookFileShelfInput{})
	require.NoError(t, err)
	require.Equal(t, model.BookFileShelfUnstarted, shelf.Status)
	require.NoError(t, s.db.Create(&model.BookReadingState{UserID: user.ID, RevisionID: *oldFile.RevisionID, CurrentPercent: .35, FurthestPercent: .7}).Error)

	newAsset, err := s.CreateBookResource(ctx, user, workID, CreateBookResourceInput{EditionID: &editionID, Language: "zh", Format: "txt"})
	require.NoError(t, err)
	second := queueBookTestFile(t, s, user, store, newAsset, "better.txt", "text/plain", []byte("better edited content"))
	_, err = s.ProcessBookResourceUploads(ctx)
	require.NoError(t, err)
	newFile, err := s.GetBookResourceUpload(ctx, user, second.ID)
	require.NoError(t, err)
	require.NotNil(t, newFile.RevisionID)
	require.NotEqual(t, *oldFile.RevisionID, *newFile.RevisionID)

	// The new upload does not silently change the edition's recommendation.
	require.NoError(t, s.db.First(&firstBest, "edition_id = ?", editionID).Error)
	require.Equal(t, *oldFile.RevisionID, firstBest.RevisionID)
	result, err := s.SetBookEditionBestFile(ctx, user, editionID, *newFile.RevisionID)
	require.NoError(t, err)
	require.Equal(t, *newFile.RevisionID, result.RevisionID)
	files, err := s.ListBookEditionFiles(ctx, editionID)
	require.NoError(t, err)
	require.Len(t, files, 2)
	require.Equal(t, *newFile.RevisionID, files[0].RevisionID)
	require.True(t, files[0].IsBest)
	require.NotNil(t, files[0].Quality, "verified uploads should expose inspection evidence")
	require.Equal(t, "txt", files[0].Quality.Format)
	require.NotNil(t, files[0].Quality.TXTValidUTF8)
	require.True(t, *files[0].Quality.TXTValidUTF8)
	require.NotNil(t, files[1].Quality, "an old file keeps its quality evidence")

	archived := true
	_, err = s.SaveBookFileShelf(ctx, user, *oldFile.RevisionID, SaveBookFileShelfInput{Status: model.BookFileShelfPaused, Archived: &archived})
	require.NoError(t, err)
	rows, total, err := s.ListBookFileShelf(ctx, user, "", "activity", 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, rows, 1)
	require.Equal(t, *oldFile.RevisionID, rows[0].RevisionID)
	require.Equal(t, .7, rows[0].FurthestPercent)
	require.True(t, rows[0].Archived)
	require.True(t, rows[0].HasBetter)
	require.Equal(t, model.BookFileShelfPaused, rows[0].Status)

	other := authctx.CurrentUser{ID: uuid.New()}
	otherRows, otherTotal, err := s.ListBookFileShelf(ctx, other, "", "activity", 20, 0)
	require.NoError(t, err)
	require.Empty(t, otherRows)
	require.Zero(t, otherTotal)

	// A missing or foreign revision cannot be recommended.
	_, err = s.SetBookEditionBestFile(ctx, user, editionID, uuid.New())
	require.Error(t, err)
	// Removing the shelf entry does not erase the reading state.
	require.NoError(t, s.DeleteBookFileShelf(ctx, user, *oldFile.RevisionID))
	var progress model.BookReadingState
	require.NoError(t, s.db.Where("user_id = ? AND revision_id = ?", user.ID, *oldFile.RevisionID).First(&progress).Error)
	require.Equal(t, .7, progress.FurthestPercent)

	// Returning to an older recommendation is an explicit, audited action.
	_, err = s.SetBookEditionBestFile(ctx, user, editionID, *oldFile.RevisionID)
	require.NoError(t, err)
	var events []model.BookBestFileEvent
	require.NoError(t, s.db.Where("edition_id = ?", editionID).Order("created_at").Find(&events).Error)
	require.Len(t, events, 3)
}
