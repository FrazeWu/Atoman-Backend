package books

import (
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNoteCollectionPublishesImmutableSnapshotsWithStableLink(t *testing.T) {
	s, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	workID := uuid.MustParse(work.ID)
	_, revision := readyBookReadingFile(t, s, user, workID, "zh", "txt")
	privateID := uuid.New()
	note, err := s.SaveBookPrivateNote(ctx, user, privateID, SaveBookPrivateNoteInput{WorkID: workID, Title: "原始题目", Body: "我自己的思考"})
	require.NoError(t, err)
	mark, err := s.SaveBookReadingMark(ctx, user, revision.ID, uuid.New(), txtBookHighlight(1, 5, "批注内容"))
	require.NoError(t, err)
	draft := NoteCollectionDraft{Title: "我的阅读摘录", Description: "亲自挑选的片段", Items: []BookNoteReference{{Kind: "chapter_note", ID: note.ID}, {Kind: "highlight", ID: mark.ID}}}
	id := uuid.New()
	saved, err := s.SaveNoteCollection(ctx, user, id, SaveNoteCollectionInput{BaseVersion: 0, WorkID: workID, NoteCollectionDraft: draft})
	require.NoError(t, err)
	require.EqualValues(t, 1, saved.Version)
	require.Equal(t, "private", saved.Visibility)
	require.Empty(t, saved.SharePath)
	anonymous, _ := s.GetSharedNoteCollection(ctx, "not-valid")
	require.Empty(t, anonymous.Title)
	_, err = s.PublishNoteCollection(ctx, user, id, PublishNoteCollectionInput{BaseVersion: 1, RequestID: uuid.New(), Visibility: "link", ConfirmPublish: false})
	require.Error(t, err)
	request := PublishNoteCollectionInput{BaseVersion: 1, RequestID: uuid.New(), Visibility: "link", ConfirmPublish: true}
	published, err := s.PublishNoteCollection(ctx, user, id, request)
	require.NoError(t, err)
	require.EqualValues(t, 2, published.Version)
	require.EqualValues(t, 1, published.PublishedVersion)
	require.Contains(t, published.SharePath, "/books/note-shares/")
	shareKey := published.SharePath[len("/books/note-shares/"):]
	public, err := s.GetSharedNoteCollection(ctx, shareKey)
	require.NoError(t, err)
	require.Equal(t, "link", public.Visibility)
	unlisted, err := s.SearchPublicNoteShares(ctx, 20, 0)
	require.NoError(t, err)
	require.Zero(t, unlisted.Total)
	require.Equal(t, "原始题目", public.Items[0].Title)
	require.Equal(t, "我自己的思考", public.Items[0].Note)
	encoded, _ := json.Marshal(public)
	for _, secret := range []string{user.ID.String(), note.ID.String(), mark.ID.String(), revision.ID.String(), "chapter_anchor", "source_revision_id", "share_key"} {
		require.NotContains(t, string(encoded), secret)
	}
	// Editing only private drafts must NOT change a published share.
	edited, err := s.SaveBookPrivateNote(ctx, user, note.ID, SaveBookPrivateNoteInput{BaseVersion: note.Version, WorkID: workID, Title: "已编辑", Body: "这不应自动泄露到分享", Tags: []string{}})
	require.NoError(t, err)
	require.EqualValues(t, 2, edited.Version)
	newDraft := draft
	newDraft.Description = "更新说明"
	updated, err := s.SaveNoteCollection(ctx, user, id, SaveNoteCollectionInput{BaseVersion: published.Version, WorkID: workID, NoteCollectionDraft: newDraft})
	require.NoError(t, err)
	require.Equal(t, "更新说明", updated.Description, "draft save returns current content")
	reloaded, err := s.GetNoteCollection(ctx, user, id)
	require.NoError(t, err)
	require.Equal(t, updated.Description, reloaded.Description)
	unchanged, err := s.GetSharedNoteCollection(ctx, shareKey)
	require.NoError(t, err)
	require.Equal(t, "原始题目", unchanged.Items[0].Title)
	require.Equal(t, "亲自挑选的片段", unchanged.Description)
	// Explicit publication updates only active snapshot; URL stays the same.
	newer, err := s.PublishNoteCollection(ctx, user, id, PublishNoteCollectionInput{BaseVersion: updated.Version, RequestID: uuid.New(), Visibility: "public", ConfirmPublish: true})
	require.NoError(t, err)
	require.Equal(t, published.SharePath, newer.SharePath)
	require.EqualValues(t, 2, newer.PublishedVersion)
	latest, err := s.GetSharedNoteCollection(ctx, shareKey)
	require.NoError(t, err)
	require.Equal(t, "已编辑", latest.Items[0].Title)
	require.Equal(t, "public", latest.Visibility)
	index, err := s.SearchPublicNoteShares(ctx, 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, index.Total)
	require.Equal(t, newer.SharePath, index.Items[0].SharePath)
	var frozen model.BookNoteShareSnapshot
	require.NoError(t, s.db.Where("collection_id = ? AND version = ?", id, 1).First(&frozen).Error)
	require.Error(t, s.db.Model(&frozen).Update("content_json", `{ "title": "tampered", "items": [] }`).Error)
	// Same request replay cannot republish a stale snapshot after revocation.
	repeated, err := s.PublishNoteCollection(ctx, user, id, request)
	require.NoError(t, err)
	require.Equal(t, newer.SharePath, repeated.SharePath)
	revoked, err := s.RevokeNoteCollection(ctx, user, id, newer.Version)
	require.NoError(t, err)
	require.Equal(t, "private", revoked.Visibility)
	require.Empty(t, revoked.SharePath)
	notIndexed, err := s.SearchPublicNoteShares(ctx, 20, 0)
	require.NoError(t, err)
	require.Zero(t, notIndexed.Total)
	_, err = s.GetSharedNoteCollection(ctx, shareKey)
	require.Error(t, err)
	_, err = s.PublishNoteCollection(ctx, user, id, request)
	require.NoError(t, err)
	_, err = s.GetSharedNoteCollection(ctx, shareKey)
	require.Error(t, err)
	// Other user cannot enumerate private collections.
	stranger := authctx.CurrentUser{ID: uuid.New()}
	_, err = s.GetNoteCollection(ctx, stranger, id)
	require.Error(t, err)
	others, err := s.ListNoteCollections(ctx, stranger, uuid.Nil)
	require.NoError(t, err)
	require.Zero(t, others.Total)
}

func TestNoteCollectionRejectsCrossUserAndCrossWorkSelections(t *testing.T) {
	s, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	workID := uuid.MustParse(work.ID)
	note, err := s.SaveBookPrivateNote(ctx, user, uuid.New(), SaveBookPrivateNoteInput{WorkID: workID, Body: "私有内容"})
	require.NoError(t, err)
	other := authctx.CurrentUser{ID: uuid.New()}
	_, err = s.SaveNoteCollection(ctx, other, uuid.New(), SaveNoteCollectionInput{WorkID: workID, NoteCollectionDraft: NoteCollectionDraft{Title: "试图分享别人内容", Items: []BookNoteReference{{Kind: "chapter_note", ID: note.ID}}}})
	require.Error(t, err)
	_, err = s.SaveNoteCollection(ctx, user, uuid.New(), SaveNoteCollectionInput{WorkID: workID, NoteCollectionDraft: NoteCollectionDraft{Title: "重复", Items: []BookNoteReference{{Kind: "chapter_note", ID: note.ID}, {Kind: "chapter_note", ID: note.ID}}}})
	require.Error(t, err)
}

func TestDeletingSharedNotesDefaultsToRevokeButCanExplicitlyKeepSnapshot(t *testing.T) {
	s, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	workID := uuid.MustParse(work.ID)
	note, err := s.SaveBookPrivateNote(ctx, user, uuid.New(), SaveBookPrivateNoteInput{WorkID: workID, Body: "需要保存的个人批注"})
	require.NoError(t, err)
	createAndPublish := func(noteID uuid.UUID) (NoteCollectionDTO, string) {
		t.Helper()
		id := uuid.New()
		draft := NoteCollectionDraft{Title: "共享心得", Items: []BookNoteReference{{Kind: "chapter_note", ID: noteID}}}
		saved, err := s.SaveNoteCollection(ctx, user, id, SaveNoteCollectionInput{WorkID: workID, NoteCollectionDraft: draft})
		require.NoError(t, err)
		pub, err := s.PublishNoteCollection(ctx, user, id, PublishNoteCollectionInput{BaseVersion: saved.Version, RequestID: uuid.New(), Visibility: "link", ConfirmPublish: true})
		require.NoError(t, err)
		return pub, pub.SharePath[len("/books/note-shares/"):]
	}
	first, key := createAndPublish(note.ID)
	impact, err := s.NoteShareImpact(ctx, user, BookNoteReference{Kind: "chapter_note", ID: note.ID})
	require.NoError(t, err)
	require.Equal(t, 1, impact.Total)
	require.Equal(t, first.ID, impact.Items[0].CollectionID)
	_, err = s.DeleteBookPrivateNote(ctx, user, note.ID, note.Version)
	require.NoError(t, err)
	_, err = s.GetSharedNoteCollection(ctx, key)
	require.Error(t, err)
	draft, err := s.GetNoteCollection(ctx, user, first.ID)
	require.NoError(t, err)
	require.Equal(t, "private", draft.Visibility)
	require.Len(t, draft.Items, 1)
	// Explicit consent can retain an immutable public copy when deleting the source.
	secondNote, err := s.SaveBookPrivateNote(ctx, user, uuid.New(), SaveBookPrivateNoteInput{WorkID: workID, Body: "明确保留公开快照"})
	require.NoError(t, err)
	_, secondKey := createAndPublish(secondNote.ID)
	_, err = s.DeleteBookPrivateNoteWithShares(ctx, user, secondNote.ID, secondNote.Version, true)
	require.NoError(t, err)
	shared, err := s.GetSharedNoteCollection(ctx, secondKey)
	require.NoError(t, err)
	require.Equal(t, "明确保留公开快照", shared.Items[0].Note)
}

func TestDeletingSharedHighlightRevokesWholeLink(t *testing.T) {
	s, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	workID := uuid.MustParse(work.ID)
	_, revision := readyBookReadingFile(t, s, user, workID, "zh", "txt")
	mark, err := s.SaveBookReadingMark(ctx, user, revision.ID, uuid.New(), txtBookHighlight(1, 5, "阅读批注"))
	require.NoError(t, err)
	id := uuid.New()
	draft := NoteCollectionDraft{Title: "摘录", Items: []BookNoteReference{{Kind: "highlight", ID: mark.ID}}}
	saved, err := s.SaveNoteCollection(ctx, user, id, SaveNoteCollectionInput{WorkID: workID, NoteCollectionDraft: draft})
	require.NoError(t, err)
	published, err := s.PublishNoteCollection(ctx, user, id, PublishNoteCollectionInput{BaseVersion: saved.Version, RequestID: uuid.New(), Visibility: "public", ConfirmPublish: true})
	require.NoError(t, err)
	impact, err := s.NoteShareImpact(ctx, user, BookNoteReference{Kind: "highlight", ID: mark.ID})
	require.NoError(t, err)
	require.Equal(t, 1, impact.Total)
	key := published.SharePath[len("/books/note-shares/"):]
	_, err = s.DeleteBookReadingMark(ctx, user, revision.ID, mark.ID, mark.Version)
	require.NoError(t, err)
	_, err = s.GetSharedNoteCollection(ctx, key)
	require.Error(t, err)
}
