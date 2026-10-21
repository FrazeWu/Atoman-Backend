package books

import (
	"context"
	"encoding/json"
	"testing"

	"atoman/internal/migrations"
	"atoman/internal/model"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBookEditionMetadataAndFilesStaySeparateFromWork(t *testing.T) {
	s, user, work, store := newBookResourceTestService(t)
	ctx := context.Background()
	workID := uuid.MustParse(work.ID)
	first, err := s.CreateBookEdition(ctx, user, workID, CreateBookEditionInput{Title: "中文译本", Subtitle: "版本副标题", EditionStatement: "第 2 版", Translators: []string{"译者甲"}, Publisher: "出版社", ISBN13: "9787511575920", Language: "zh", PublishedDate: "2020-01-02"})
	require.NoError(t, err)
	require.Equal(t, "版本副标题", first.Subtitle)
	require.Equal(t, "第 2 版", first.EditionStatement)
	require.Len(t, first.Translators, 1)
	require.Equal(t, "译者甲", first.Translators[0].Name)
	second, err := s.CreateBookEdition(ctx, user, workID, CreateBookEditionInput{Title: "另一译本", Translators: []string{"译者乙"}})
	require.NoError(t, err)
	public, err := s.GetBookWork(ctx, workID)
	require.NoError(t, err)
	require.Len(t, public.Authors, 1)
	require.Len(t, public.Editions, 2)
	require.Empty(t, public.Subtitle, "edition subtitle must not leak into the work")
	require.Empty(t, public.Language)

	editionID := uuid.MustParse(first.ID)
	asset, err := s.CreateBookResource(ctx, user, workID, CreateBookResourceInput{EditionID: &editionID, Format: "txt", Language: "zh"})
	require.NoError(t, err)
	require.Equal(t, &editionID, asset.EditionID)
	upload := queueBookTestFile(t, s, user, store, asset, "edition.txt", "text/plain", []byte("版本正文"))
	_, err = s.ProcessBookResourceUploads(ctx)
	require.NoError(t, err)
	ready, err := s.GetBookResourceUpload(ctx, user, upload.ID)
	require.NoError(t, err)
	require.NotNil(t, ready.RevisionID)
	listed, err := s.ListBookResources(ctx, workID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, &editionID, listed[0].EditionID)

	other, err := s.CreateBookWork(ctx, user, CreateBookWorkInput{Title: "另一本作品", Authors: []string{"其他作者"}})
	require.NoError(t, err)
	_, err = s.CreateBookResource(ctx, user, uuid.MustParse(other.ID), CreateBookResourceInput{EditionID: &editionID, Format: "txt"})
	require.Error(t, err, "a file cannot use an edition from another work")
	require.Error(t, s.db.Model(&model.BookAsset{}).Where("id = ?", asset.ID).Update("work_id", other.ID).Error, "database must also enforce ownership")

	legacy, err := s.CreateBookResource(ctx, user, workID, CreateBookResourceInput{Format: "pdf"})
	require.NoError(t, err)
	require.NoError(t, migrations.RunBooksV2Migration(s.db))
	require.NoError(t, s.db.First(&legacy, "id = ?", legacy.ID).Error)
	require.Nil(t, legacy.EditionID, "migration must not guess old file ownership")

	// Edition merging moves ownership, not the immutable revision or reader state.
	selection := model.BookReadingSelection{UserID: user.ID, WorkID: workID, AssetID: asset.ID, RevisionID: *ready.RevisionID}
	require.NoError(t, s.db.Create(&selection).Error)
	ids := PreviewBookWorkMergeInput{EntityType: "edition", SourceID: editionID, TargetID: uuid.MustParse(second.ID)}
	preview, err := s.PreviewBookWorkMerge(ctx, user, ids)
	require.NoError(t, err)
	journal, err := s.MergeBookWorks(ctx, user, MergeBookWorksInput{PreviewBookWorkMergeInput: ids, PreviewToken: preview.PreviewToken, SourceRevisionID: preview.SourceRevisionID, TargetRevisionID: preview.TargetRevisionID})
	require.NoError(t, err)
	require.NoError(t, s.db.First(&asset, "id = ?", asset.ID).Error)
	require.Equal(t, ids.TargetID, *asset.EditionID)
	_, err = s.RevertBookWorkMerge(ctx, user, journal.ID)
	require.NoError(t, err)
	require.NoError(t, s.db.First(&asset, "id = ?", asset.ID).Error)
	require.Equal(t, editionID, *asset.EditionID)
	require.NoError(t, s.db.First(&selection, "user_id = ? AND work_id = ?", user.ID, workID).Error)
	require.Equal(t, *ready.RevisionID, selection.RevisionID)
}

func TestBookEditionTranslatorRevisionCanBeEditedAndReverted(t *testing.T) {
	s, user := newBookWikiTestService(t)
	ctx := context.Background()
	work, err := s.CreateBookWork(ctx, user, CreateBookWorkInput{Title: "作品", Authors: []string{"作者"}})
	require.NoError(t, err)
	edition, err := s.CreateBookEdition(ctx, user, uuid.MustParse(work.ID), CreateBookEditionInput{Translators: []string{"译者"}, Subtitle: "副标题"})
	require.NoError(t, err)
	id := uuid.MustParse(edition.ID)
	head, err := s.GetBookWikiMetadata(ctx, "edition", id)
	require.NoError(t, err)
	edited, err := s.PatchBookMetadata(ctx, user, "edition", id, PatchBookMetadataInput{BaseRevisionID: head.RevisionID, Fields: map[string]json.RawMessage{"translator_ids": json.RawMessage(`[]`), "subtitle": json.RawMessage(`"更正副标题"`)}}, "edit")
	require.NoError(t, err)
	current, err := s.GetBookWork(ctx, uuid.MustParse(work.ID))
	require.NoError(t, err)
	require.Empty(t, current.Editions[0].Translators)
	var original model.Revision
	require.NoError(t, s.db.First(&original, "id = ?", head.RevisionID).Error)
	_, err = s.RevertBookMetadata(ctx, user, original, RevertBookWikiInput{BaseRevisionID: edited.RevisionID})
	require.NoError(t, err)
	current, err = s.GetBookWork(ctx, uuid.MustParse(work.ID))
	require.NoError(t, err)
	require.Equal(t, "副标题", current.Editions[0].Subtitle)
	require.Equal(t, "译者", current.Editions[0].Translators[0].Name)
}
