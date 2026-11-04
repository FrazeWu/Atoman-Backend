package books

import (
	"context"
	"sync"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCatalogCreateIdempotencyAcrossRetriesAndRequests(t *testing.T) {
	service, user := newBookWikiTestService(t)
	require.NoError(t, service.db.AutoMigrate(&model.BookCatalogCreateReceipt{}))
	ctx := context.Background()
	workInput := CreateBookWorkInput{Title: "幂等作品", Authors: []string{"作者"}}
	requestID := uuid.New()
	first, err := service.CreateBookWorkIdempotent(ctx, user, workInput, requestID)
	require.NoError(t, err)
	retry, err := service.CreateBookWorkIdempotent(ctx, user, workInput, requestID)
	require.NoError(t, err)
	require.Equal(t, first.ID, retry.ID)
	_, err = service.CreateBookWorkIdempotent(ctx, user, CreateBookWorkInput{Title: "另一作品", Authors: []string{"作者"}}, requestID)
	require.Equal(t, "books.create_request_reused", apperr.FromError(err).Code)

	editionInput := CreateBookEditionInput{Language: "zh", Publisher: "出版社", EditionStatement: "第 1 版"}
	editionID := uuid.New()
	edition, err := service.CreateBookEditionIdempotent(ctx, user, uuid.MustParse(first.ID), editionInput, editionID)
	require.NoError(t, err)
	editionRetry, err := service.CreateBookEditionIdempotent(ctx, user, uuid.MustParse(first.ID), editionInput, editionID)
	require.NoError(t, err)
	require.Equal(t, edition.ID, editionRetry.ID)
	_, err = service.CreateBookEditionIdempotent(ctx, user, uuid.MustParse(first.ID), editionInput, requestID)
	require.Equal(t, "books.create_request_reused", apperr.FromError(err).Code)
}

func TestCatalogConcurrentCreateSameIdempotencyKey(t *testing.T) {
	service, user := newBookWikiTestService(t)
	require.NoError(t, service.db.AutoMigrate(&model.BookCatalogCreateReceipt{}))
	var wg sync.WaitGroup
	requestID := uuid.New()
	type result struct {
		id  string
		err error
	}
	responses := make(chan result, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			work, err := service.CreateBookWorkIdempotent(context.Background(), user,
				CreateBookWorkInput{Title: "并发作品", Authors: []string{"唯一作者"}}, requestID)
			responses <- result{id: work.ID, err: err}
		}()
	}
	wg.Wait()
	close(responses)
	var ids []string
	for r := range responses {
		require.NoError(t, r.err)
		ids = append(ids, r.id)
	}
	require.Len(t, ids, 2)
	require.Equal(t, ids[0], ids[1])
	var count int64
	require.NoError(t, service.db.Model(&model.BookWork{}).Where("title = ?", "并发作品").Count(&count).Error)
	require.EqualValues(t, 1, count)
}
