package books

import (
	"context"
	"errors"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newBookWikiTestService(t *testing.T) (*Service, authctx.CurrentUser) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Revision{}, &model.EditConflict{}, &model.BookWork{}, &model.BookEdition{}, &model.BookPublisher{}, &model.BookProducer{}, &model.BookPerson{}, &model.BookContribution{}, &model.BookSource{}, &model.BookRating{})
	user := model.User{UUID: uuid.New(), Username: "editor", Email: "editor@example.test", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	return NewService(db), authctx.CurrentUser{ID: user.UUID, Role: authctx.RoleUser}
}

func TestCreateBookWorkNeedsOnlyTitleAndAuthor(t *testing.T) {
	service, user := newBookWikiTestService(t)
	ctx := context.Background()
	work, err := service.CreateBookWork(ctx, user, CreateBookWorkInput{Title: " 图书 ", Authors: []string{" 作者 ", "作者", ""}})
	require.NoError(t, err)
	require.Equal(t, "图书", work.Title)
	require.Len(t, work.Authors, 1)
	require.Empty(t, work.Editions)
	require.NotEqual(t, uuid.Nil, work.RevisionID)
	_, count, err := service.SearchPublicCatalog(ctx, "图书", 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	var revisions []model.Revision
	require.NoError(t, service.db.Where("content_id = ?", work.ID).Find(&revisions).Error)
	require.Len(t, revisions, 1)
	require.Equal(t, "approved", revisions[0].Status)
	require.True(t, revisions[0].IsCurrent)
	require.Contains(t, string(revisions[0].ContentSnapshot), `"author_ids"`)

	person, err := service.CreateBookPerson(ctx, user, CreateBookPersonInput{Name: "已存在的作者"})
	require.NoError(t, err)
	selected, err := service.CreateBookWork(ctx, user, CreateBookWorkInput{Title: "选择已有作者", AuthorIDs: []uuid.UUID{person.ID, person.ID}})
	require.NoError(t, err)
	require.Len(t, selected.Authors, 1)
	require.Equal(t, person.ID.String(), selected.Authors[0].ID)
	_, err = service.CreateBookEdition(ctx, user, uuid.MustParse(work.ID), CreateBookEditionInput{Title: "第一版", Publisher: "出版社", PublishedDate: "2026-10-15"})
	require.NoError(t, err)
	updated, err := service.GetBookWork(ctx, uuid.MustParse(work.ID))
	require.NoError(t, err)
	require.Len(t, updated.Editions, 1)
	require.Equal(t, work.RevisionID, updated.RevisionID, "edition history is separate from work history")
}

func TestCreateBookWorkValidationAndRollback(t *testing.T) {
	service, user := newBookWikiTestService(t)
	ctx := context.Background()
	for _, input := range []CreateBookWorkInput{
		{Title: "书名"}, {Title: " ", Authors: []string{"作者"}},
		{Title: "书名", Authors: []string{" "}},
		{Title: "书名", AuthorIDs: []uuid.UUID{uuid.New()}},
	} {
		_, err := service.CreateBookWork(ctx, user, input)
		require.Error(t, err)
	}
	require.NoError(t, service.db.Callback().Create().Before("gorm:create").Register("test:fail_contribution", func(tx *gorm.DB) {
		if tx.Statement.Table == "book_contributions" {
			tx.AddError(errors.New("test contribution failure"))
		}
	}))
	_, err := service.CreateBookWork(ctx, user, CreateBookWorkInput{Title: "失败作品", Authors: []string{"也不应残留的作者"}})
	require.Error(t, err)
	for _, table := range []any{&model.BookWork{}, &model.BookPerson{}, &model.BookContribution{}, &model.Revision{}} {
		var count int64
		require.NoError(t, service.db.Model(table).Count(&count).Error)
		require.Zero(t, count)
	}
}
