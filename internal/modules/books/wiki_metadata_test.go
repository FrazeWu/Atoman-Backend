package books

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestBookMetadataEditConflictAndRevert(t *testing.T) {
	service, user := newBookWikiTestService(t)
	ctx := context.Background()
	work, err := service.CreateBookWork(ctx, user, CreateBookWorkInput{Title: "测试", Authors: []string{"作者"}})
	require.NoError(t, err)
	edition, err := service.CreateBookEdition(ctx, user, uuid.MustParse(work.ID), CreateBookEditionInput{Publisher: "初版社"})
	require.NoError(t, err)
	for _, test := range []struct {
		kind          string
		id            uuid.UUID
		field         string
		before, after string
	}{{"person", uuid.MustParse(work.Authors[0].ID), "name", "作者", "更正作者"}, {"edition", uuid.MustParse(edition.ID), "publisher", "初版社", "新版社"}} {
		t.Run(test.kind, func(t *testing.T) {
			initial, err := service.GetBookWikiMetadata(ctx, test.kind, test.id)
			require.NoError(t, err)
			encoded, _ := json.Marshal(test.after)
			input := PatchBookMetadataInput{BaseRevisionID: initial.RevisionID, Fields: map[string]json.RawMessage{test.field: encoded}, Summary: "修正"}
			edited, err := service.PatchBookMetadata(ctx, user, test.kind, test.id, input, "edit")
			require.NoError(t, err)
			require.NotEqual(t, initial.RevisionID, edited.RevisionID)
			_, err = service.PatchBookMetadata(ctx, user, test.kind, test.id, input, "edit")
			require.Equal(t, "books.edit_conflict", apperr.FromError(err).Code)
			var revision model.Revision
			require.NoError(t, service.db.First(&revision, "id = ?", initial.RevisionID).Error)
			restored, err := service.RevertBookMetadata(ctx, user, revision, RevertBookWikiInput{BaseRevisionID: edited.RevisionID})
			require.NoError(t, err)
			var fields map[string]any
			require.NoError(t, json.Unmarshal(restored.Fields, &fields))
			require.Equal(t, test.before, fields[test.field])
			_, err = service.PatchBookMetadata(ctx, user, test.kind, test.id, PatchBookMetadataInput{BaseRevisionID: restored.RevisionID, Fields: map[string]json.RawMessage{"lifecycle_status": json.RawMessage(`"merged"`)}}, "edit")
			require.Equal(t, "books.invalid_metadata_field", apperr.FromError(err).Code)
		})
	}
}

func TestBookMetadataMergeDeduplicatesAndUndoRestores(t *testing.T) {
	service, user, work, _ := newBookMergeTestService(t)
	ctx := context.Background()
	first, err := service.CreateBookPerson(ctx, user, CreateBookPersonInput{Name: "重复作者"})
	require.NoError(t, err)
	second, err := service.CreateBookPerson(ctx, user, CreateBookPersonInput{Name: "规范作者"})
	require.NoError(t, err)
	workID := uuid.MustParse(work.ID)
	for _, id := range []uuid.UUID{first.ID, second.ID} {
		require.NoError(t, service.db.Create(&model.BookContribution{WorkID: &workID, PersonID: id, Role: "author", Position: 1}).Error)
	}
	sourceEdition, err := service.CreateBookEdition(ctx, user, workID, CreateBookEditionInput{Title: "重复版"})
	require.NoError(t, err)
	targetEdition, err := service.CreateBookEdition(ctx, user, workID, CreateBookEditionInput{Title: "规范版"})
	require.NoError(t, err)
	for _, test := range []struct {
		kind           string
		source, target uuid.UUID
	}{{"person", first.ID, second.ID}, {"edition", uuid.MustParse(sourceEdition.ID), uuid.MustParse(targetEdition.ID)}} {
		t.Run(test.kind, func(t *testing.T) {
			input := PreviewBookWorkMergeInput{EntityType: test.kind, SourceID: test.source, TargetID: test.target}
			preview, err := service.PreviewBookWorkMerge(ctx, user, input)
			require.NoError(t, err)
			journal, err := service.MergeBookWorks(ctx, user, MergeBookWorksInput{PreviewBookWorkMergeInput: input, PreviewToken: preview.PreviewToken, SourceRevisionID: preview.SourceRevisionID, TargetRevisionID: preview.TargetRevisionID})
			require.NoError(t, err)
			resolved, err := service.GetBookWikiMetadata(ctx, test.kind, test.source)
			require.NoError(t, err)
			require.Equal(t, test.target, resolved.EntityID)
			_, err = service.RevertBookWorkMerge(ctx, user, journal.ID)
			require.NoError(t, err)
			resolved, err = service.GetBookWikiMetadata(ctx, test.kind, test.source)
			require.NoError(t, err)
			require.Equal(t, test.source, resolved.EntityID)
			_, err = service.RevertBookWorkMerge(ctx, user, journal.ID)
			require.Equal(t, "books.merge_already_reverted", apperr.FromError(err).Code)
		})
	}
}

func TestBookMetadataMergeRefusesStalePreviewAndUndo(t *testing.T) {
	service, user, work, _ := newBookMergeTestService(t)
	ctx := context.Background()
	workID := uuid.MustParse(work.ID)
	a, err := service.CreateBookEdition(ctx, user, workID, CreateBookEditionInput{Title: "A"})
	require.NoError(t, err)
	b, err := service.CreateBookEdition(ctx, user, workID, CreateBookEditionInput{Title: "B"})
	require.NoError(t, err)
	ids := PreviewBookWorkMergeInput{EntityType: "edition", SourceID: uuid.MustParse(a.ID), TargetID: uuid.MustParse(b.ID)}
	preview, err := service.PreviewBookWorkMerge(ctx, user, ids)
	require.NoError(t, err)
	request := MergeBookWorksInput{PreviewBookWorkMergeInput: ids, PreviewToken: preview.PreviewToken, SourceRevisionID: preview.SourceRevisionID, TargetRevisionID: preview.TargetRevisionID}
	journal, err := service.MergeBookWorks(ctx, user, request)
	require.NoError(t, err)
	target, err := service.GetBookWikiMetadata(ctx, "edition", ids.TargetID)
	require.NoError(t, err)
	_, err = service.PatchBookMetadata(ctx, user, "edition", ids.TargetID, PatchBookMetadataInput{BaseRevisionID: target.RevisionID, Fields: map[string]json.RawMessage{"title": json.RawMessage(`"合并后新编辑"`)}}, "edit")
	require.NoError(t, err)
	_, err = service.RevertBookWorkMerge(ctx, user, journal.ID)
	require.Equal(t, "books.merge_revert_conflict", apperr.FromError(err).Code)
}
