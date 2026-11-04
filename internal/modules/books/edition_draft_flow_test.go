package books

import (
	"context"
	"testing"

	"atoman/internal/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestEditionDraftBecomesPublicOnlyAfterSafeFile(t *testing.T) {
	s, user, work, store := newBookResourceTestService(t)
	ctx := context.Background()
	workID := uuid.MustParse(work.ID)
	input := CreateBookEditionInput{Language: "zh", Publisher: "测试出版社", EditionStatement: "首版"}
	result, err := s.CreateBookEditionDraftIdempotent(ctx, user, workID, input, uuid.Nil)
	require.NoError(t, err)
	id := uuid.MustParse(result.ID)
	var edition model.BookEdition
	require.NoError(t, s.db.First(&edition, "id = ?", id).Error)
	require.Equal(t, model.BookLifecycleStatusDraft, edition.LifecycleStatus)
	visible, err := s.GetBookWork(ctx, workID)
	require.NoError(t, err)
	for _, item := range visible.Editions {
		require.NotEqual(t, result.ID, item.ID)
	}
	asset, err := s.CreateBookResource(ctx, user, workID, CreateBookResourceInput{EditionID: &id, Language: "zh", Format: "txt"})
	require.NoError(t, err)
	session := queueBookTestFile(t, s, user, store, asset, "draft.txt", "text/plain", []byte("a valid book"))
	_, err = s.ProcessBookResourceUploads(ctx)
	require.NoError(t, err)
	ready, err := s.GetBookResourceUpload(ctx, user, session.ID)
	require.NoError(t, err)
	require.Equal(t, "ready", ready.Status)
	require.NoError(t, s.db.First(&edition, "id = ?", id).Error)
	require.Equal(t, model.BookLifecycleStatusActive, edition.LifecycleStatus)
	visible, err = s.GetBookWork(ctx, workID)
	require.NoError(t, err)
	var found bool
	for _, item := range visible.Editions {
		if item.ID == id.String() {
			found = true
		}
	}
	require.True(t, found)
}
