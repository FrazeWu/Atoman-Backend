package comment

import (
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestBookListCommentsRequireCurrentPublicSnapshot(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBookList, 0)
	testdb.Migrate(t, ctx.db, &model.BookList{})
	now := time.Now().UTC()
	list := model.BookList{UserID: ctx.users[0].ID, Visibility: "public", Version: 1, PublishedVersion: 1, DraftJSON: `{"title":"私密草稿"}`, PublishedJSON: `{"title":"公开书单"}`, ChangesJSON: `{}`, PublishedAt: &now}
	require.NoError(t, ctx.db.Create(&list).Error)
	ctx.service.registry = NewTargetRegistry(ctx.db)
	ctx.target = TargetRef{Kind: TargetKindBookList, ResourceID: list.ID}
	root := ctx.create(t, 0, "公开书单讨论", nil)
	require.NoError(t, ctx.service.Like(ctx.users[1], root.ID))
	require.NoError(t, ctx.db.Model(&list).Update("visibility", "private").Error)
	_, err := ctx.service.List(authctx.CurrentUser{}, ctx.target, ListCommentsInput{Page: 1, PageSize: 20, Sort: SortOldest})
	require.ErrorIs(t, err, ErrTargetNotFound)
	_, err = ctx.service.Create(ctx.users[0], ctx.target, CreateCommentInput{Content: "撤下后不能评论"})
	require.ErrorIs(t, err, ErrTargetNotFound)
}
