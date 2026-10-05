package books

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/stretchr/testify/require"
)

func TestOpenLibraryDumpImportKeepsOneBestChineseEditionPerWork(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.BookWork{}, &model.BookEdition{}, &model.BookPerson{},
		&model.BookContribution{}, &model.BookSource{},
	)

	dir := t.TempDir()
	worksPath := writeOpenLibraryDump(t, dir, "works.txt", []string{
		`/type/work	/works/OL1W	1	2026-01-01T00:00:00.000000	{"title":"中文作品","description":{"value":"作品简介"},"authors":[{"author":{"key":"/authors/OL2A"}}]}`,
		`/type/work	/works/OL2W	1	2026-01-01T00:00:00.000000	{"title":"没有中文版本"}`,
	})
	editionsPath := writeOpenLibraryDump(t, dir, "editions.txt", []string{
		`/type/edition	/books/OL10M	1	2026-01-01T00:00:00.000000	{"title":"中文作品英文版","languages":[{"key":"/languages/eng"}],"works":[{"key":"/works/OL1W"}]}`,
		`/type/edition	/books/OL11M	1	2026-01-01T00:00:00.000000	{"title":"中文作品旧版","languages":[{"key":"/languages/chi"}],"works":[{"key":"/works/OL1W"}],"publish_date":"2001","publishers":["旧出版社"]}`,
		`/type/edition	/books/OL12M	1	2026-01-01T00:00:00.000000	{"title":"中文作品新版","languages":[{"key":"/languages/chi"}],"works":[{"key":"/works/OL1W"}],"publish_date":"2020","publishers":["新出版社"],"isbn_13":["9781234567890"],"number_of_pages":300,"physical_format":"Paperback","covers":[123]}`,
	})
	authorsPath := writeOpenLibraryDump(t, dir, "authors.txt", []string{
		`/type/author	/authors/OL2A	1	2026-01-01T00:00:00.000000	{"name":"作者甲"}`,
	})

	summary, err := NewOpenLibraryDumpImporter(db).ImportChinese(context.Background(), OpenLibraryDumpImportOptions{
		WorksPath: worksPath, EditionsPath: editionsPath, AuthorsPath: authorsPath,
	})
	require.NoError(t, err)
	require.Equal(t, 3, summary.EditionsScanned)
	require.Equal(t, 2, summary.ChineseEditions)
	require.Equal(t, 1, summary.WorksSelected)
	require.Equal(t, 1, summary.RecordsImported)

	var work model.BookWork
	require.NoError(t, db.First(&work).Error)
	require.Equal(t, "chi", work.Language)
	require.Equal(t, "作品简介", work.Description)

	var edition model.BookEdition
	require.NoError(t, db.Where("work_id = ?", work.ID).First(&edition).Error)
	require.Equal(t, "9781234567890", edition.ISBN13)
	require.Equal(t, "Paperback", edition.Binding)
	require.Equal(t, "chi", edition.Language)
	var editionSource model.BookSource
	require.NoError(t, db.Where("target_id = ?", edition.ID).First(&editionSource).Error)
	require.Equal(t, "https://openlibrary.org/books/OL12M", editionSource.URL)

	var person model.BookPerson
	require.NoError(t, db.First(&person).Error)
	require.Equal(t, "作者甲", person.Name)
}

func TestOpenLibraryDumpRecordRejectsMalformedLine(t *testing.T) {
	_, err := parseOpenLibraryDumpRecord([]byte("not-a-dump-line"))
	require.Error(t, err)
}

func writeOpenLibraryDump(t *testing.T, dir, name string, lines []string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(joinDumpLines(lines)), 0o600))
	return path
}

func joinDumpLines(lines []string) string {
	result := ""
	for _, line := range lines {
		result += strings.ReplaceAll(line, `\t`, "\t") + "\n"
	}
	return result
}
