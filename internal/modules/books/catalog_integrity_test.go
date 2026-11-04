package books

import (
	"context"
	"encoding/json"
	"testing"

	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCatalogEditionValidation(t *testing.T) {
	tests := []struct {
		name string
		data CreateBookEditionInput
		code string
	}{
		{"language required", CreateBookEditionInput{Publisher: "出版社", PublishedDate: "2024-01-01"}, "books.edition_language_required"},
		{"identity required", CreateBookEditionInput{Language: "zh", Title: "普通书名"}, "books.edition_identifier_required"},
		{"language invalid", CreateBookEditionInput{Language: "中文", ISBN13: "9780306406157"}, "books.invalid_language"},
		{"isbn checksum invalid", CreateBookEditionInput{Language: "zh", ISBN13: "9787100057419"}, "books.invalid_isbn"},
		{"isbn shape invalid", CreateBookEditionInput{Language: "zh", ISBN13: "978-71000-xxxx"}, "books.invalid_isbn"},
		{"isbn valid", CreateBookEditionInput{Language: "zh", ISBN13: "9780306406157"}, ""},
		{"isbn10 valid", CreateBookEditionInput{Language: "en", ISBN10: "0-306-40615-2"}, ""},
		{"isbn mismatch", CreateBookEditionInput{Language: "en", ISBN10: "0-306-40615-2", ISBN13: "9783161484100"}, "books.isbn_mismatch"},
		{"publisher year without isbn", CreateBookEditionInput{Language: "en", Publisher: "出版社", PublishedDate: "2024-01-01"}, ""},
		{"publisher edition without isbn", CreateBookEditionInput{Language: "zh-Hans", Publisher: "出版社", EditionStatement: "修订版"}, ""},
		{"publisher translator without isbn", CreateBookEditionInput{Language: "zh", Publisher: "出版社", Translators: []string{"甲"}}, ""},
		{"translator with dated title without publisher", CreateBookEditionInput{Language: "zh", Title: "译本", PublishedDate: "2024-01-01", Translators: []string{"甲"}}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.data
			err := validateCatalogEditionCreate(&in)
			if tc.code == "" {
				require.NoError(t, err)
			} else {
				require.Equal(t, tc.code, apperr.FromError(err).Code)
			}
		})
	}
}

func TestCatalogExistingAuthorRequiresExplicitSelectionOrNewPerson(t *testing.T) {
	service, user := newBookWikiTestService(t)
	ctx := context.Background()
	person, err := service.CreateBookPerson(ctx, user, CreateBookPersonInput{Name: " 同名作者 "})
	require.NoError(t, err)
	_, err = service.CreateBookWork(ctx, user, CreateBookWorkInput{Title: "作品甲", Authors: []string{"同名作者"}})
	require.Equal(t, "books.author_name_exists", apperr.FromError(err).Code)

	reused, err := service.CreateBookWork(ctx, user, CreateBookWorkInput{Title: "作品甲", AuthorIDs: []uuid.UUID{person.ID}, Authors: []string{"同名作者"}})
	require.NoError(t, err)
	require.Len(t, reused.Authors, 1)
	require.Equal(t, person.ID.String(), reused.Authors[0].ID)

	distinct, err := service.CreateBookWork(ctx, user, CreateBookWorkInput{
		Title: "作品乙", Authors: []string{"同名作者"}, NewAuthorNames: []string{"同名作者"},
	})
	require.NoError(t, err)
	require.Len(t, distinct.Authors, 1)
	require.NotEqual(t, person.ID.String(), distinct.Authors[0].ID)
}

func TestCatalogDuplicateChecksAreSuggestionsNotMerges(t *testing.T) {
	service, user := newBookWikiTestService(t)
	ctx := context.Background()
	work, err := service.CreateBookWork(ctx, user, CreateBookWorkInput{Title: "沉思录", OriginalTitle: "Meditations", Authors: []string{"马可·奥勒留"}})
	require.NoError(t, err)
	candidates, err := service.CheckBookWorkDuplicates(ctx, CheckBookWorkDuplicatesInput{Title: "沉思录", Authors: []string{"马可·奥勒留"}})
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, work.ID, candidates[0].Work.ID)
	require.GreaterOrEqual(t, candidates[0].Score, 90)

	workID := uuid.MustParse(work.ID)
	edition, err := service.CreateBookEdition(ctx, user, workID, CreateBookEditionInput{
		Language: "zh", Publisher: "某出版社", EditionStatement: "第一版",
	})
	require.NoError(t, err)
	editions, err := service.CheckBookEditionDuplicates(ctx, workID, CreateBookEditionInput{
		Language: "zh", Publisher: "某出版社", EditionStatement: "第一版",
	})
	require.NoError(t, err)
	require.Len(t, editions, 1)
	require.Equal(t, edition.ID, editions[0].Edition.ID)
	require.Equal(t, "same_publication", editions[0].Reason)

	// The existing edition is not silently reused or merged.
	second, err := service.CreateBookEdition(ctx, user, workID, CreateBookEditionInput{
		Language: "zh", Publisher: "某出版社", EditionStatement: "第一版",
	})
	require.NoError(t, err)
	require.NotEqual(t, edition.ID, second.ID)
}

// Wiki edits must not turn a valid public edition into an unidentified row.
func TestCatalogEditionWikiCannotRemoveRequiredIdentity(t *testing.T) {
	service, user := newBookWikiTestService(t)
	ctx := context.Background()
	work, err := service.CreateBookWork(ctx, user, CreateBookWorkInput{
		Title: "标识测试作品", Authors: []string{"标识测试作者"},
	})
	require.NoError(t, err)
	edition, err := service.CreateBookEdition(ctx, user, uuid.MustParse(work.ID), CreateBookEditionInput{
		Language: "zh", ISBN13: "9780306406157",
	})
	require.NoError(t, err)
	editionID := uuid.MustParse(edition.ID)
	head, err := service.GetBookWikiMetadata(ctx, "edition", editionID)
	require.NoError(t, err)
	for field, raw := range map[string]string{
		"language": `""`,
		"isbn13":   `""`,
	} {
		_, err := service.PatchBookMetadata(ctx, user, "edition", editionID,
			PatchBookMetadataInput{BaseRevisionID: head.RevisionID, Fields: map[string]json.RawMessage{
				field: json.RawMessage(raw),
			}}, "edit")
		require.Error(t, err, field)
	}
	unchanged, err := service.GetBookWikiMetadata(ctx, "edition", editionID)
	require.NoError(t, err)
	require.Equal(t, head.RevisionID, unchanged.RevisionID)
}
