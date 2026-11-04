package books

import (
	"context"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestReadingMarkTrashRestoreAndErase(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	_, revision := readyBookReadingFile(t, service, user, uuid.MustParse(work.ID), "zh", "txt")
	id := uuid.New()
	created, err := service.SaveBookReadingMark(ctx, user, revision.ID, id, txtBookHighlight(4, 11, "不应丢失的思想"))
	require.NoError(t, err)
	other := authctx.CurrentUser{ID: uuid.New()}
	_, err = service.ListBookReadingMarkTrash(ctx, other, 20, 0)
	require.NoError(t, err)
	_, err = service.RestoreBookReadingMark(ctx, other, revision.ID, id, 2)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	deleted, err := service.DeleteBookReadingMark(ctx, user, revision.ID, id, created.Version)
	require.NoError(t, err)
	require.EqualValues(t, 2, deleted.Version)
	trash, err := service.ListBookReadingMarkTrash(ctx, user, 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, trash.Total)
	require.Equal(t, id, trash.Items[0].ID)
	require.Equal(t, "不应丢失的思想", trash.Items[0].Note)
	require.Equal(t, revision.ID, trash.Items[0].RevisionID)
	require.True(t, trash.Items[0].RestoreUntil.After(time.Now().UTC().Add(29*24*time.Hour)))
	_, err = service.RestoreBookReadingMark(ctx, user, revision.ID, id, 1)
	require.Equal(t, "books.reading_mark_conflict", apperr.FromError(err).Code)
	restored, err := service.RestoreBookReadingMark(ctx, user, revision.ID, id, deleted.Version)
	require.NoError(t, err)
	require.EqualValues(t, 3, restored.Version)
	require.Equal(t, "不应丢失的思想", restored.Note)
	again, err := service.RestoreBookReadingMark(ctx, user, revision.ID, id, deleted.Version)
	require.NoError(t, err)
	require.Equal(t, restored.Version, again.Version)
	marks, err := service.ListBookReadingMarks(ctx, user, revision.ID)
	require.NoError(t, err)
	require.Len(t, marks.Items, 1)
	require.Empty(t, marks.Deleted)
	// Restored notes can be deleted once more, then permanently erased after explicit consent.
	erasedCandidate, err := service.DeleteBookReadingMark(ctx, user, revision.ID, id, restored.Version)
	require.NoError(t, err)
	_, err = service.PermanentlyEraseBookReadingMark(ctx, user, revision.ID, id, erasedCandidate.Version-1)
	require.Equal(t, "books.reading_mark_conflict", apperr.FromError(err).Code)
	erased, err := service.PermanentlyEraseBookReadingMark(ctx, user, revision.ID, id, erasedCandidate.Version)
	require.NoError(t, err)
	require.EqualValues(t, erasedCandidate.Version+1, erased.Version)
	var stored model.BookReadingMark
	require.NoError(t, service.db.Unscoped().First(&stored, "id = ?", id).Error)
	require.NotNil(t, stored.PurgedAt)
	require.Empty(t, stored.Note)
	require.Empty(t, stored.Quote)
	require.Equal(t, "{}", stored.AnchorJSON)
	trash, err = service.ListBookReadingMarkTrash(ctx, user, 20, 0)
	require.NoError(t, err)
	require.Zero(t, trash.Total)
	_, err = service.RestoreBookReadingMark(ctx, user, revision.ID, id, erased.Version)
	require.Equal(t, 410, apperr.FromError(err).HTTPStatus)
	_, err = service.SaveBookReadingMark(ctx, user, revision.ID, id, txtBookHighlight(4, 11, "离线旧写入"))
	require.Equal(t, "books.reading_mark_conflict", apperr.FromError(err).Code)
}
func TestReadingMarkTrashAutoExpiresAfter30DaysWithoutResurrection(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	_, revision := readyBookReadingFile(t, service, user, uuid.MustParse(work.ID), "zh", "txt")
	id := uuid.New()
	created, err := service.SaveBookReadingMark(ctx, user, revision.ID, id, txtBookHighlight(8, 14, "已经超过恢复期"))
	require.NoError(t, err)
	deleted, err := service.DeleteBookReadingMark(ctx, user, revision.ID, id, created.Version)
	require.NoError(t, err)
	older := time.Now().UTC().Add(-31 * 24 * time.Hour)
	require.NoError(t, service.db.Unscoped().Model(&model.BookReadingMark{}).Where("id = ?", id).Update("deleted_at", older).Error)
	// The daily worker uses this same sweep; an on-demand trash read enforces it too.
	trash, err := service.ListBookReadingMarkTrash(ctx, user, 20, 0)
	require.NoError(t, err)
	require.Empty(t, trash.Items)
	var stored model.BookReadingMark
	require.NoError(t, service.db.Unscoped().First(&stored, "id = ?", id).Error)
	require.NotNil(t, stored.PurgedAt)
	require.EqualValues(t, deleted.Version+1, stored.Version)
	require.Empty(t, stored.Note)
	require.Empty(t, stored.Quote)
	_, err = service.RestoreBookReadingMark(ctx, user, revision.ID, id, deleted.Version)
	require.Equal(t, 410, apperr.FromError(err).HTTPStatus)
	_, err = service.SaveBookReadingMark(ctx, user, revision.ID, id, txtBookHighlight(8, 14, "还原已删划线"))
	require.Equal(t, "books.reading_mark_conflict", apperr.FromError(err).Code)
}
func TestReadingMarkTrashRejectsActiveEraseAndOtherUsers(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	_, revision := readyBookReadingFile(t, service, user, uuid.MustParse(work.ID), "zh", "txt")
	id := uuid.New()
	item, err := service.SaveBookReadingMark(ctx, user, revision.ID, id, txtBookHighlight(1, 4, "私人划线"))
	require.NoError(t, err)
	_, err = service.PermanentlyEraseBookReadingMark(ctx, user, revision.ID, id, item.Version)
	require.Equal(t, "books.reading_mark_not_trashed", apperr.FromError(err).Code)
	_, err = service.DeleteBookReadingMark(ctx, user, revision.ID, id, item.Version)
	require.NoError(t, err)
	outsider := authctx.CurrentUser{ID: uuid.New()}
	list, err := service.ListBookReadingMarkTrash(ctx, outsider, 20, 0)
	require.NoError(t, err)
	require.Zero(t, list.Total)
	_, err = service.PermanentlyEraseBookReadingMark(ctx, outsider, revision.ID, id, 2)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	// A concurrent restore and purge use row locking; only a matching version wins.
	_, err = service.RestoreBookReadingMark(ctx, user, revision.ID, id, 2)
	require.NoError(t, err)
	_, err = service.PermanentlyEraseBookReadingMark(ctx, user, revision.ID, id, 2)
	require.Error(t, err)
	// Tombstone has been removed from the trash but remains a valid live mark.
	var live model.BookReadingMark
	require.NoError(t, service.db.Where("id = ?", id).First(&live).Error)
	require.False(t, live.DeletedAt.Valid)
	require.NoError(t, service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return nil }))
}
