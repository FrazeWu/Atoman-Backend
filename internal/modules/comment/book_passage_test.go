package comment

import (
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBookPassageUsesUnifiedRepliesLikesReportsAndLifecycleVisibility(t *testing.T) {
	ctx := newCommentTestContext(t, TargetKindBookPassage, 0)
	testdb.Migrate(t, ctx.db, &model.BookWork{}, &model.BookAsset{}, &model.BookAssetRevision{}, &model.BookDiscussionPassage{})
	work := model.BookWork{Title: "段落讨论", LifecycleStatus: model.BookLifecycleStatusActive}
	require.NoError(t, ctx.db.Create(&work).Error)
	asset := model.BookAsset{WorkID: work.ID, Language: "zh", Format: "txt", CreatedBy: ctx.users[0].ID}
	require.NoError(t, ctx.db.Create(&asset).Error)
	revision := model.BookAssetRevision{AssetID: asset.ID, Number: 1, MediaAssetID: uuid.New(), SHA256: strings.Repeat("a", 64), OriginalFilename: "book.txt", Status: model.BookRevisionStatusReady, CreatedBy: ctx.users[0].ID}
	require.NoError(t, ctx.db.Create(&revision).Error)
	passage := model.BookDiscussionPassage{RevisionID: revision.ID, AnchorJSON: `{"txt_offset_end":5}`, AnchorDigest: "hash", CreatedBy: ctx.users[0].ID}
	require.NoError(t, ctx.db.Create(&passage).Error)
	ctx.service.registry = NewTargetRegistry(ctx.db)
	ctx.target = TargetRef{Kind: TargetKindBookPassage, ResourceID: passage.ID}
	root := ctx.create(t, 0, "对这个段落的评论", nil)
	reply := ctx.create(t, 1, "段落回复", &root.ID)
	require.Equal(t, root.ID, *reply.RootID)
	require.NoError(t, ctx.service.Like(ctx.users[1], root.ID))
	require.NoError(t, ctx.service.Report(ctx.users[2], root.ID, ReportInput{Reason: "spam"}))
	listInput := ListCommentsInput{Page: 1, PageSize: 20, Sort: SortOldest}
	list, err := ctx.service.List(authctx.CurrentUser{}, ctx.target, listInput)
	require.NoError(t, err)
	require.EqualValues(t, 1, list.TotalRoots)
	require.Equal(t, 1, list.Items[0].LikeCount)
	require.Len(t, list.Items[0].Replies, 1)
	require.NoError(t, ctx.db.Model(&model.BookAsset{}).Where("id = ?", asset.ID).Update("status", model.BookResourceStatusRemoved).Error)
	_, err = ctx.service.List(authctx.CurrentUser{}, ctx.target, listInput)
	require.ErrorIs(t, err, ErrTargetNotFound)
	_, err = ctx.service.Create(ctx.users[1], ctx.target, CreateCommentInput{Content: "下架后禁止新增"})
	require.ErrorIs(t, err, ErrTargetNotFound)
	_, err = ctx.service.ListReplies(Viewer{}, root.ID, 1, 20)
	require.ErrorIs(t, err, ErrTargetNotFound)
	require.ErrorIs(t, ctx.service.Like(ctx.users[2], root.ID), ErrTargetNotFound)
}
