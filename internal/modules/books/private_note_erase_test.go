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

func TestPrivateNoteManualErasureRequiresTrashAndPreservesTombstone(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	id := uuid.New()
	note, err := service.SaveBookPrivateNote(ctx, user, id, SaveBookPrivateNoteInput{WorkID: uuid.MustParse(work.ID), Title: "敏感原始标题", Body: "待销毁的私有内容", Tags: []string{"私密"}})
	require.NoError(t, err)
	_, err = service.PermanentlyEraseBookPrivateNote(ctx, user, id, note.Version)
	require.Equal(t, "books.private_note_not_trashed", apperr.FromError(err).Code)
	deleted, err := service.DeleteBookPrivateNote(ctx, user, id, note.Version)
	require.NoError(t, err)
	other := authctx.CurrentUser{ID: uuid.New()}
	_, err = service.PermanentlyEraseBookPrivateNote(ctx, other, id, deleted.Version)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	_, err = service.PermanentlyEraseBookPrivateNote(ctx, user, id, deleted.Version-1)
	require.Equal(t, 409, apperr.FromError(err).HTTPStatus)
	erased, err := service.PermanentlyEraseBookPrivateNote(ctx, user, id, deleted.Version)
	require.NoError(t, err)
	require.EqualValues(t, deleted.Version+1, erased.Version)
	var stored model.BookPrivateNote
	require.NoError(t, service.db.Unscoped().Where("id = ?", id).First(&stored).Error)
	require.NotNil(t, stored.PurgedAt)
	require.Empty(t, stored.Title)
	require.Empty(t, stored.Body)
	require.Equal(t, "[]", stored.TagsJSON)
	require.Equal(t, "{}", stored.ChapterAnchorJSON)
	_, err = service.RestoreBookPrivateNote(ctx, user, id, erased.Version)
	require.Equal(t, 410, apperr.FromError(err).HTTPStatus)
	_, err = service.SaveBookPrivateNote(ctx, user, id, SaveBookPrivateNoteInput{WorkID: uuid.MustParse(work.ID), Body: "离线旧请求"})
	require.Equal(t, 409, apperr.FromError(err).HTTPStatus)
}
