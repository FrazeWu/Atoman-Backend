package books

import (
	"context"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestChapterAnchorValidationAndPrivateNoteLifecycle(t *testing.T) {
	ctx := context.Background()
	s, user, work, _ := newBookResourceTestService(t)
	workID := uuid.MustParse(work.ID)
	_, revision := readyBookReadingFile(t, s, user, workID, "zh", "txt")
	noteID := uuid.New()
	anchor := BookChapterAnchor{Format: "txt", TXTOffset: 87, ChapterTitle: "第2章阅读位置"}
	input := SaveBookPrivateNoteInput{WorkID: workID, SourceRevisionID: &revision.ID, ChapterAnchor: &anchor, Title: "本章思考", Body: "关于第二章", Tags: []string{"研究"}}
	original, err := s.SaveBookPrivateNote(ctx, user, noteID, input)
	require.NoError(t, err)
	require.NotNil(t, original.ChapterAnchor)
	require.EqualValues(t, 87, original.ChapterAnchor.TXTOffset)
	retry, err := s.SaveBookPrivateNote(ctx, user, noteID, input)
	require.NoError(t, err)
	require.EqualValues(t, 1, retry.Version)
	got, err := s.GetBookPrivateNote(ctx, user, noteID)
	require.NoError(t, err)
	require.Equal(t, "第2章阅读位置", got.ChapterAnchor.ChapterTitle)
	input.BaseVersion = 1
	input.ChapterAnchor = nil // older editors must not erase a source position
	input.Body = "已经编辑过的内容"
	updated, err := s.SaveBookPrivateNote(ctx, user, noteID, input)
	require.NoError(t, err)
	require.EqualValues(t, 2, updated.Version)
	require.EqualValues(t, 87, updated.ChapterAnchor.TXTOffset)
	timeline, err := s.ListWorkNotes(ctx, user, workID, WorkNotesFilter{Kind: "chapter_note", RevisionID: revision.ID})
	require.NoError(t, err)
	require.EqualValues(t, 1, timeline.Total)
	require.NotNil(t, timeline.Items[0].ChapterAnchor)
	require.EqualValues(t, 87, timeline.Items[0].ChapterAnchor.TXTOffset)
	// Invalid/foreign format must never be written as a valid exact anchor.
	_, err = s.SaveBookPrivateNote(ctx, user, uuid.New(), SaveBookPrivateNoteInput{WorkID: workID, SourceRevisionID: &revision.ID, ChapterAnchor: &BookChapterAnchor{Format: "pdf", PDFPage: 7}, Body: "bad"})
	require.Error(t, err)
	_, err = s.SaveBookPrivateNote(ctx, user, uuid.New(), SaveBookPrivateNoteInput{WorkID: workID, ChapterAnchor: &anchor, Body: "bad"})
	require.Error(t, err)
	backup, err := s.ExportBookPrivateNotes(ctx, user)
	require.NoError(t, err)
	require.Len(t, backup.Notes, 1)
	require.NotNil(t, backup.Notes[0].ChapterAnchor)
	require.EqualValues(t, 87, backup.Notes[0].ChapterAnchor.TXTOffset)
	input.BaseVersion = 2
	input.ClearChapterAnchor = true
	input.ChapterAnchor = nil
	cleared, err := s.SaveBookPrivateNote(ctx, user, noteID, input)
	require.NoError(t, err)
	require.Nil(t, cleared.ChapterAnchor)
}

func TestChapterAnchorSupportsPrecisePDFEPUBAndTXTOnly(t *testing.T) {
	values := []struct {
		format string
		value  BookChapterAnchor
	}{
		{"epub", BookChapterAnchor{Format: "epub", EPUBCFI: "epubcfi(/6/2!/4/1:0)", EPUBHref: "chapter02.xhtml"}},
		{"pdf", BookChapterAnchor{Format: "pdf", PDFPage: 12, PDFOffset: 0.67}},
		{"txt", BookChapterAnchor{Format: "txt", TXTOffset: 12003}},
		{"cbz", BookChapterAnchor{Format: "cbz", PDFPage: 9}},
	}
	for _, it := range values {
		_, err := normalizeBookChapterAnchor(it.value, it.format)
		require.NoError(t, err)
	}
	invalid := []BookChapterAnchor{
		{Format: "pdf", PDFPage: 0}, {Format: "pdf", PDFPage: 1, PDFOffset: 1.1},
		{Format: "epub", EPUBCFI: "anything"}, {Format: "txt", TXTOffset: -1},
	}
	for _, value := range invalid {
		_, err := normalizeBookChapterAnchor(value, value.Format)
		require.Error(t, err)
	}
}
