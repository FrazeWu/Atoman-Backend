package books

import (
	"context"
	"sync"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBookWikiEditsAreImmediateVersionedAndRevertible(t *testing.T) {
	service, user := newBookWikiTestService(t)
	ctx := context.Background()
	created, err := service.CreateBookWork(ctx, user, CreateBookWorkInput{Title: "初始书名", Authors: []string{"作者"}})
	require.NoError(t, err)
	id := uuid.MustParse(created.ID)
	title := "修正书名"
	updated, err := service.PatchBookWork(ctx, user, id, PatchBookWorkInput{BaseRevisionID: created.RevisionID, Title: &title, Summary: "更正标题"})
	require.NoError(t, err)
	require.Equal(t, title, updated.Title)
	require.NotEqual(t, created.RevisionID, updated.RevisionID)
	history, total, err := service.ListBookWikiRevisions(ctx, "work", id, 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	require.Len(t, history[0].Changes, 1)
	require.Equal(t, "title", history[0].Changes[0].Field)
	require.JSONEq(t, `"初始书名"`, string(history[0].Changes[0].Before))
	require.JSONEq(t, `"修正书名"`, string(history[0].Changes[0].After))
	_, err = service.RevertBookWikiRevision(ctx, user, created.RevisionID, RevertBookWikiInput{BaseRevisionID: created.RevisionID})
	require.Equal(t, "books.edit_conflict", apperr.FromError(err).Code)
	reverted, err := service.RevertBookWikiRevision(ctx, user, created.RevisionID, RevertBookWikiInput{BaseRevisionID: updated.RevisionID, Summary: "撤销错误编辑"})
	require.NoError(t, err)
	require.Equal(t, created.Title, reverted.Title)
	history, total, err = service.ListBookWikiRevisions(ctx, "work", id, 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 3, total)
	require.Equal(t, "revert", history[0].EditType)
	require.Equal(t, updated.RevisionID, *history[0].PreviousRevisionID)
	_, err = service.PatchBookWork(ctx, user, id, PatchBookWorkInput{Title: &title})
	require.Equal(t, "books.base_revision_required", apperr.FromError(err).Code)
	invalidIDs := []uuid.UUID{uuid.New()}
	_, err = service.PatchBookWork(ctx, user, id, PatchBookWorkInput{BaseRevisionID: reverted.RevisionID, Title: &title, AuthorIDs: &invalidIDs})
	require.Error(t, err)
	current, err := service.GetBookWork(ctx, id)
	require.NoError(t, err)
	require.Equal(t, reverted.RevisionID, current.RevisionID)
	require.Equal(t, reverted.Title, current.Title)
}

func TestBookWikiConcurrentEditsNeverSilentlyOverwrite(t *testing.T) {
	service, user := newBookWikiTestService(t)
	ctx := context.Background()
	second := model.User{UUID: uuid.New(), Username: "second", Email: "second@example.test", Password: "hash", IsActive: true}
	require.NoError(t, service.db.Create(&second).Error)
	created, err := service.CreateBookWork(ctx, user, CreateBookWorkInput{Title: "初始书名", Authors: []string{"作者"}})
	require.NoError(t, err)
	id := uuid.MustParse(created.ID)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for index, editor := range []authctx.CurrentUser{user, {ID: second.UUID, Role: authctx.RoleUser}} {
		wg.Add(1)
		go func(index int, editor authctx.CurrentUser) {
			defer wg.Done()
			<-start
			title := []string{"编辑甲", "编辑乙"}[index]
			_, err := service.PatchBookWork(ctx, editor, id, PatchBookWorkInput{BaseRevisionID: created.RevisionID, Title: &title})
			results <- err
		}(index, editor)
	}
	close(start)
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			require.Equal(t, "books.edit_conflict", apperr.FromError(err).Code)
			conflicts++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	var records []model.EditConflict
	require.NoError(t, service.db.Where("content_id = ?", id).Find(&records).Error)
	require.Len(t, records, 1)
	require.Equal(t, "title", records[0].FieldName)
	require.Equal(t, `"初始书名"`, records[0].BaseValue)
	require.NotEqual(t, records[0].Value1, records[0].Value2)
	_, total, err := service.ListBookWikiRevisions(ctx, "work", id, 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
}
