package books

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestOpenLibraryProviderSearchMapsBookMetadataAndSendsAttributionHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/search.json", r.URL.Path)
		require.Equal(t, "The Hobbit", r.URL.Query().Get("q"))
		require.Equal(t, "10", r.URL.Query().Get("limit"))
		require.Equal(t, "Atoman book importer", r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"numFound": 1,
			"docs": [{
				"key": "/works/OL27448W",
				"title": "The Hobbit",
				"subtitle": "There and Back Again",
				"author_key": ["OL23919A"],
				"author_name": ["J. R. R. Tolkien"],
				"first_publish_year": 1937,
				"edition_key": ["OL7353617M"],
				"publisher": ["George Allen & Unwin"],
				"isbn": ["0261103303", "9780261103303"],
				"language": ["eng"],
				"number_of_pages_median": 310,
				"cover_i": 8406786
			}]
		}`))
	}))
	defer server.Close()

	provider, err := NewOpenLibraryProvider(server.Client(), server.URL, "Atoman book importer")
	require.NoError(t, err)

	results, err := provider.Search(context.Background(), "The Hobbit", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, CatalogBook{
		ExternalWorkID:    "OL27448W",
		ExternalEditionID: "OL7353617M",
		Title:             "The Hobbit",
		Subtitle:          "There and Back Again",
		Language:          "eng",
		Publisher:         "George Allen & Unwin",
		ISBN10:            "0261103303",
		ISBN13:            "9780261103303",
		PublishedYear:     1937,
		PageCount:         310,
		CoverURL:          "https://covers.openlibrary.org/b/id/8406786-M.jpg",
		WorkSourceURL:     "https://openlibrary.org/works/OL27448W",
		EditionSourceURL:  "https://openlibrary.org/books/OL7353617M",
		Authors:           []CatalogAuthor{{ExternalID: "OL23919A", Name: "J. R. R. Tolkien"}},
	}, results[0])
}

func TestImportCatalogRecordsIsIdempotentAndKeepsImportedRecordsDraft(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.BookWork{},
		&model.BookEdition{},
		&model.BookPerson{},
		&model.BookContribution{},
		&model.BookSource{},
	)

	importer := NewCatalogImporter(db)
	record := CatalogBook{
		ExternalWorkID:    "OL27448W",
		ExternalEditionID: "OL7353617M",
		Title:             "The Hobbit",
		Subtitle:          "There and Back Again",
		Language:          "eng",
		Publisher:         "George Allen & Unwin",
		ISBN10:            "0261103303",
		ISBN13:            "9780261103303",
		PublishedYear:     1937,
		PageCount:         310,
		WorkSourceURL:     "https://openlibrary.org/works/OL27448W",
		EditionSourceURL:  "https://openlibrary.org/books/OL7353617M",
		Authors:           []CatalogAuthor{{ExternalID: "OL23919A", Name: "J. R. R. Tolkien"}},
	}

	first, err := importer.Import(context.Background(), []CatalogBook{record})
	require.NoError(t, err)
	require.Equal(t, CatalogImportSummary{Records: 1, NewWorks: 1, NewEditions: 1, NewPeople: 1, NewContributions: 1}, first)

	second, err := importer.Import(context.Background(), []CatalogBook{record})
	require.NoError(t, err)
	require.Equal(t, CatalogImportSummary{Records: 1, ExistingWorks: 1, ExistingEditions: 1, ExistingPeople: 1, ExistingContributions: 1}, second)

	var work model.BookWork
	require.NoError(t, db.Where("title = ?", record.Title).First(&work).Error)
	require.Equal(t, model.BookLifecycleStatusDraft, work.LifecycleStatus)
	require.Equal(t, model.BookEditStatusDevelopment, work.EditStatus)

	var edition model.BookEdition
	require.NoError(t, db.Where("work_id = ?", work.ID).First(&edition).Error)
	require.Equal(t, record.ISBN13, edition.ISBN13)

	var person model.BookPerson
	require.NoError(t, db.Where("name = ?", "J. R. R. Tolkien").First(&person).Error)
	var contribution model.BookContribution
	require.NoError(t, db.Where("work_id = ? AND person_id = ? AND role = ?", work.ID, person.ID, "author").First(&contribution).Error)

	var sources []model.BookSource
	require.NoError(t, db.Where("target_id IN ?", []uuid.UUID{work.ID, edition.ID, person.ID}).Find(&sources).Error)
	require.Len(t, sources, 3)
}
