package books

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"context"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestBookResourceModerationKeepsPrivateDataAndGuardsContent(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	asset, revision := readyBookReadingFile(t, service, user, uuid.MustParse(work.ID), "zh", "txt")
	mark, err := service.SaveBookReadingMark(ctx, user, revision.ID, uuid.New(), txtBookHighlight(0, 5, "私人批注"))
	require.NoError(t, err)
	_, err = service.SetBookResourceStatus(ctx, user, asset.ID, SetBookResourceStatusInput{Status: "removed", Reason: "版权请求"})
	require.Equal(t, "books.moderation_required", apperr.FromError(err).Code)
	moderator := user
	moderator.Role = authctx.RoleModerator
	_, err = service.SetBookResourceStatus(ctx, moderator, asset.ID, SetBookResourceStatusInput{Status: "removed", Reason: "版权请求"})
	require.NoError(t, err)
	_, err = service.readableBookRevision(ctx, revision.ID)
	require.Error(t, err)
	items, total, err := service.ListBookPrivateHighlights(ctx, user, "私人批注", "yellow", 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, mark.ID, items[0].ID)
	items, total, err = service.ListBookPrivateHighlights(ctx, authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}, "", "", 20, 0)
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, items)
	_, err = service.SetBookResourceStatus(ctx, moderator, asset.ID, SetBookResourceStatusInput{Status: "active", Reason: "申诉成立"})
	require.NoError(t, err)
	_, err = service.readableBookRevision(ctx, revision.ID)
	require.NoError(t, err)
	require.NoError(t, service.db.Model(&model.BookAssetRevision{}).Where("id = ?", revision.ID).Update("status", "quarantined").Error)
	_, err = service.SetBookResourceStatus(ctx, moderator, asset.ID, SetBookResourceStatusInput{Status: "active", Reason: "不能绕过扫描"})
	require.Error(t, err)
}
