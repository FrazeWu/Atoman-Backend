package books

import (
	"context"
	"encoding/json"
	"testing"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPreviewNotesAndOwnerOnlyImmutableShareHistory(t *testing.T) {
	s, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	workID := uuid.MustParse(work.ID)
	note, err := s.SaveBookPrivateNote(ctx, user, uuid.New(), SaveBookPrivateNoteInput{WorkID: workID, Title: "第一章", Body: "读者原创心得"})
	require.NoError(t, err)
	draft := NoteCollectionDraft{Title: "即将发布的笔记", Description: "请先预览这些条目", Items: []BookNoteReference{{Kind: "chapter_note", ID: note.ID}}}
	preview, err := s.PreviewNoteCollection(ctx, user, PreviewNoteCollectionInput{WorkID: workID, NoteCollectionDraft: draft})
	require.NoError(t, err)
	require.Len(t, preview.Items, 1)
	require.Len(t, preview.Digest, 64)
	require.Equal(t, "读者原创心得", preview.Items[0].Note)
	encoded, _ := json.Marshal(preview)
	require.NotContains(t, string(encoded), note.ID.String())
	list, err := s.ListNoteCollections(ctx, user, workID)
	require.NoError(t, err)
	require.Zero(t, list.Total, "preview must not create a private collection")
	stranger := authctx.CurrentUser{ID: uuid.New()}
	_, err = s.PreviewNoteCollection(ctx, stranger, PreviewNoteCollectionInput{WorkID: workID, NoteCollectionDraft: draft})
	require.Error(t, err)
	id := uuid.New()
	saved, err := s.SaveNoteCollection(ctx, user, id, SaveNoteCollectionInput{WorkID: workID, NoteCollectionDraft: draft})
	require.NoError(t, err)
	first, err := s.PublishNoteCollection(ctx, user, id, PublishNoteCollectionInput{BaseVersion: saved.Version, RequestID: uuid.New(), Visibility: "link", ConfirmPublish: true, PreviewDigest: preview.Digest})
	require.NoError(t, err)
	draft.Description = "修改了私人草稿并再发布"
	saved, err = s.SaveNoteCollection(ctx, user, id, SaveNoteCollectionInput{BaseVersion: first.Version, WorkID: workID, NoteCollectionDraft: draft})
	require.NoError(t, err)
	_, err = s.PublishNoteCollection(ctx, user, id, PublishNoteCollectionInput{BaseVersion: saved.Version, RequestID: uuid.New(), Visibility: "public", ConfirmPublish: true, PreviewDigest: preview.Digest})
	require.Equal(t, "books.note_preview_changed", apperr.FromError(err).Code)
	updatedPreview, err := s.PreviewNoteCollection(ctx, user, PreviewNoteCollectionInput{WorkID: workID, NoteCollectionDraft: draft})
	require.NoError(t, err)
	second, err := s.PublishNoteCollection(ctx, user, id, PublishNoteCollectionInput{BaseVersion: saved.Version, RequestID: uuid.New(), Visibility: "public", ConfirmPublish: true, PreviewDigest: updatedPreview.Digest})
	require.NoError(t, err)
	history, err := s.ListNoteShareHistory(ctx, user, id, 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 2, history.Total)
	require.EqualValues(t, 2, history.Items[0].Version)
	require.True(t, history.Items[0].Active)
	require.False(t, history.Items[1].Active)
	oldest, err := s.GetNoteShareHistoryVersion(ctx, user, id, 1)
	require.NoError(t, err)
	require.Equal(t, "请先预览这些条目", oldest.Description)
	require.False(t, oldest.Active)
	newest, err := s.GetNoteShareHistoryVersion(ctx, user, id, 2)
	require.NoError(t, err)
	require.Equal(t, "修改了私人草稿并再发布", newest.Description)
	require.True(t, newest.Active)
	_, err = s.ListNoteShareHistory(ctx, stranger, id, 20, 0)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	_, err = s.GetNoteShareHistoryVersion(ctx, stranger, id, 1)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	_, err = s.GetNoteShareHistoryVersion(ctx, user, id, 1234)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	_, err = s.RevokeNoteCollection(ctx, user, id, second.Version)
	require.NoError(t, err)
	after, err := s.GetNoteShareHistoryVersion(ctx, user, id, 2)
	require.NoError(t, err)
	require.False(t, after.Active, "revocation keeps history private and read-only")
	_, err = s.GetSharedNoteCollection(ctx, first.SharePath[len("/books/note-shares/"):])
	require.Error(t, err)
}
