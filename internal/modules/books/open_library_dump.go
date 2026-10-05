package books

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	openLibraryChineseLanguage = "chi"
	openLibraryChineseZho      = "zho"
	openLibraryDumpBatchSize   = 500
)

var openLibraryYearPattern = regexp.MustCompile(`(?:^|[^0-9])([0-9]{4})(?:[^0-9]|$)`)

// OpenLibraryDumpImportOptions describes the three Open Library dump files.
// The importer keeps records in draft status; activation remains an explicit review operation.
type OpenLibraryDumpImportOptions struct {
	WorksPath    string
	EditionsPath string
	AuthorsPath  string
	Limit        int
}

// OpenLibraryDumpImportSummary describes a complete Chinese-catalog import.
type OpenLibraryDumpImportSummary struct {
	EditionsScanned int
	ChineseEditions int
	WorksScanned    int
	WorksSelected   int
	AuthorsScanned  int
	AuthorsSelected int
	RecordsImported int
	RecordsFailed   int
	CatalogImportSummary
}

type openLibraryDumpImporter struct {
	db *gorm.DB
}

type openLibraryDumpEdition struct {
	EditionID     string
	Title         string
	Publisher     string
	ISBN10        string
	ISBN13        string
	Language      string
	PublishedYear int
	PageCount     int
	Binding       string
	CoverURL      string
}

type openLibraryDumpWork struct {
	WorkID      string
	Title       string
	Subtitle    string
	Description string
	AuthorIDs   []string
}

type openLibraryDumpAuthor struct {
	ID   string
	Name string
}

type openLibraryDumpRecord struct {
	Kind string
	Key  string
	Body json.RawMessage
}

type openLibraryDumpEditionPayload struct {
	Title          string   `json:"title"`
	Publishers     []string `json:"publishers"`
	PublishDate    string   `json:"publish_date"`
	NumberOfPages  int      `json:"number_of_pages"`
	PhysicalFormat string   `json:"physical_format"`
	ISBN10         []string `json:"isbn_10"`
	ISBN13         []string `json:"isbn_13"`
	Languages      []struct {
		Key string `json:"key"`
	} `json:"languages"`
	Works []struct {
		Key string `json:"key"`
	} `json:"works"`
	Covers []int `json:"covers"`
}

type openLibraryDumpWorkPayload struct {
	Title       string          `json:"title"`
	Subtitle    string          `json:"subtitle"`
	Description json.RawMessage `json:"description"`
	Authors     []struct {
		Author struct {
			Key string `json:"key"`
		} `json:"author"`
	} `json:"authors"`
}

type openLibraryDumpAuthorPayload struct {
	Name         string `json:"name"`
	PersonalName string `json:"personal_name"`
}

// NewOpenLibraryDumpImporter creates an importer for Open Library's TSV/JSON dumps.
func NewOpenLibraryDumpImporter(db *gorm.DB) *openLibraryDumpImporter {
	return &openLibraryDumpImporter{db: db}
}

// ImportChinese imports every work with at least one Chinese edition and selects
// exactly one representative Chinese edition per work.
func (i *openLibraryDumpImporter) ImportChinese(ctx context.Context, options OpenLibraryDumpImportOptions) (OpenLibraryDumpImportSummary, error) {
	var summary OpenLibraryDumpImportSummary
	if i == nil || i.db == nil {
		return summary, errors.New("book catalog database is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateOpenLibraryDumpPaths(options); err != nil {
		return summary, err
	}

	editions, err := collectChineseOpenLibraryEditions(ctx, options.EditionsPath, &summary)
	if err != nil {
		return summary, err
	}
	worksFile, err := os.CreateTemp("", "atoman-open-library-works-*.jsonl")
	if err != nil {
		return summary, fmt.Errorf("create selected work staging file: %w", err)
	}
	worksPath := worksFile.Name()
	defer os.Remove(worksPath)
	workAuthorIDs, err := collectSelectedOpenLibraryWorks(ctx, options.WorksPath, editions, worksFile, &summary, options.Limit)
	if closeErr := worksFile.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return summary, err
	}
	authors, err := collectSelectedOpenLibraryAuthors(ctx, options.AuthorsPath, workAuthorIDs, &summary)
	if err != nil {
		return summary, err
	}

	file, err := os.Open(worksPath)
	if err != nil {
		return summary, fmt.Errorf("open selected work staging file: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	var tx *gorm.DB
	batchCount := 0
	commitBatch := func() error {
		if tx == nil {
			return nil
		}
		err := tx.Commit().Error
		tx = nil
		batchCount = 0
		return err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback().Error
		}
	}()
	for {
		var work openLibraryDumpWork
		if err := decoder.Decode(&work); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return summary, fmt.Errorf("decode selected work: %w", err)
		}
		edition := editions[work.WorkID]
		record := CatalogBook{
			ExternalWorkID:    strings.TrimPrefix(work.WorkID, "/works/"),
			ExternalEditionID: strings.TrimPrefix(edition.EditionID, "/books/"),
			Title:             work.Title,
			Subtitle:          work.Subtitle,
			Description:       work.Description,
			Language:          openLibraryChineseLanguage,
			Publisher:         edition.Publisher,
			ISBN10:            edition.ISBN10,
			ISBN13:            edition.ISBN13,
			PublishedYear:     edition.PublishedYear,
			PageCount:         edition.PageCount,
			Binding:           edition.Binding,
			CoverURL:          edition.CoverURL,
			WorkSourceURL:     "https://openlibrary.org" + work.WorkID,
			EditionSourceURL:  "https://openlibrary.org" + edition.EditionID,
		}
		for _, authorID := range work.AuthorIDs {
			if author, ok := authors[authorID]; ok && strings.TrimSpace(author.Name) != "" {
				record.Authors = append(record.Authors, CatalogAuthor{ExternalID: strings.TrimPrefix(authorID, "/authors/"), Name: author.Name})
			}
		}
		var recordSummary CatalogImportSummary
		if tx == nil {
			tx = i.db.WithContext(ctx).Begin()
			if tx.Error != nil {
				return summary, fmt.Errorf("begin catalog import batch: %w", tx.Error)
			}
		}
		if err := importCatalogRecord(tx, record, &recordSummary); err != nil {
			summary.RecordsFailed++
			_ = tx.Rollback().Error
			return summary, fmt.Errorf("import work %s: %w", work.WorkID, err)
		}
		summary.CatalogImportSummary.add(recordSummary)
		summary.RecordsImported++
		batchCount++
		if batchCount >= openLibraryDumpBatchSize {
			if err := commitBatch(); err != nil {
				return summary, fmt.Errorf("commit catalog import batch: %w", err)
			}
		}
	}
	if err := commitBatch(); err != nil {
		return summary, fmt.Errorf("commit catalog import batch: %w", err)
	}
	return summary, nil
}

func validateOpenLibraryDumpPaths(options OpenLibraryDumpImportOptions) error {
	for name, path := range map[string]string{
		"works": options.WorksPath, "editions": options.EditionsPath, "authors": options.AuthorsPath,
	} {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("%s dump path is required", name)
		}
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			return fmt.Errorf("%s dump path is unavailable: %s", name, path)
		}
	}
	if options.Limit < 0 {
		return errors.New("dump import limit cannot be negative")
	}
	return nil
}

func collectChineseOpenLibraryEditions(ctx context.Context, path string, summary *OpenLibraryDumpImportSummary) (map[string]openLibraryDumpEdition, error) {
	selected := make(map[string]openLibraryDumpEdition)
	err := scanOpenLibraryDump(ctx, path, func(record openLibraryDumpRecord) error {
		if record.Kind != "type/edition" {
			return nil
		}
		summary.EditionsScanned++
		var payload openLibraryDumpEditionPayload
		if err := json.Unmarshal(record.Body, &payload); err != nil {
			return fmt.Errorf("decode edition %s: %w", record.Key, err)
		}
		language := openLibraryChineseEditionLanguage(payload.Languages)
		if language == "" || len(payload.Works) == 0 {
			return nil
		}
		summary.ChineseEditions++
		candidate := buildOpenLibraryDumpEdition(record.Key, payload, language)
		for _, work := range payload.Works {
			workID := normalizeOpenLibraryKey(work.Key, "works", "W")
			if workID == "" {
				continue
			}
			if candidate.EditionID == "" {
				continue
			}
			if current, ok := selected[workID]; !ok || betterOpenLibraryEdition(candidate, current) {
				selected[workID] = candidate
			}
		}
		return nil
	})
	return selected, err
}

func collectSelectedOpenLibraryWorks(ctx context.Context, path string, editions map[string]openLibraryDumpEdition, output io.Writer, summary *OpenLibraryDumpImportSummary, limit int) (map[string]struct{}, error) {
	selectedAuthors := make(map[string]struct{})
	encoder := json.NewEncoder(output)
	err := scanOpenLibraryDump(ctx, path, func(record openLibraryDumpRecord) error {
		if record.Kind != "type/work" {
			return nil
		}
		summary.WorksScanned++
		workID := normalizeOpenLibraryKey(record.Key, "works", "W")
		if workID == "" {
			return nil
		}
		if _, ok := editions[workID]; !ok {
			return nil
		}
		if limit > 0 && summary.WorksSelected >= limit {
			return nil
		}
		var payload openLibraryDumpWorkPayload
		if err := json.Unmarshal(record.Body, &payload); err != nil {
			return fmt.Errorf("decode work %s: %w", record.Key, err)
		}
		if strings.TrimSpace(payload.Title) == "" {
			return nil
		}
		work := openLibraryDumpWork{WorkID: record.Key, Title: strings.TrimSpace(payload.Title), Subtitle: strings.TrimSpace(payload.Subtitle), Description: openLibraryTextValue(payload.Description)}
		for _, author := range payload.Authors {
			authorID := normalizeOpenLibraryKey(author.Author.Key, "authors", "A")
			if authorID == "" {
				continue
			}
			work.AuthorIDs = append(work.AuthorIDs, authorID)
			selectedAuthors[authorID] = struct{}{}
		}
		if err := encoder.Encode(&work); err != nil {
			return err
		}
		summary.WorksSelected++
		return nil
	})
	return selectedAuthors, err
}

func collectSelectedOpenLibraryAuthors(ctx context.Context, path string, selected map[string]struct{}, summary *OpenLibraryDumpImportSummary) (map[string]openLibraryDumpAuthor, error) {
	authors := make(map[string]openLibraryDumpAuthor, len(selected))
	err := scanOpenLibraryDump(ctx, path, func(record openLibraryDumpRecord) error {
		if record.Kind != "type/author" {
			return nil
		}
		authorID := normalizeOpenLibraryKey(record.Key, "authors", "A")
		if _, ok := selected[authorID]; !ok {
			return nil
		}
		summary.AuthorsScanned++
		var payload openLibraryDumpAuthorPayload
		if err := json.Unmarshal(record.Body, &payload); err != nil {
			return fmt.Errorf("decode author %s: %w", record.Key, err)
		}
		name := strings.TrimSpace(payload.Name)
		if name == "" {
			name = strings.TrimSpace(payload.PersonalName)
		}
		if name != "" {
			authors[authorID] = openLibraryDumpAuthor{ID: authorID, Name: name}
			summary.AuthorsSelected++
		}
		return nil
	})
	return authors, err
}

func scanOpenLibraryDump(ctx context.Context, path string, visit func(openLibraryDumpRecord) error) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open dump %s: %w", path, err)
	}
	defer file.Close()
	var reader io.Reader = file
	var gzipReader *gzip.Reader
	if strings.EqualFold(filepath.Ext(path), ".gz") {
		gzipReader, err = gzip.NewReader(file)
		if err != nil {
			return fmt.Errorf("open gzip dump %s: %w", path, err)
		}
		defer gzipReader.Close()
		reader = gzipReader
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		record, err := parseOpenLibraryDumpRecord(scanner.Bytes())
		if err != nil {
			return fmt.Errorf("parse %s line %d: %w", path, line, err)
		}
		if err := visit(record); err != nil {
			return fmt.Errorf("process %s line %d: %w", path, line, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read dump %s: %w", path, err)
	}
	return nil
}

func parseOpenLibraryDumpRecord(line []byte) (openLibraryDumpRecord, error) {
	fields := strings.SplitN(string(line), "\t", 5)
	if len(fields) < 5 || strings.TrimSpace(fields[1]) == "" || len(fields[4]) == 0 {
		return openLibraryDumpRecord{}, errors.New("invalid TSV dump record")
	}
	var body json.RawMessage
	if err := json.Unmarshal([]byte(fields[4]), &body); err != nil {
		return openLibraryDumpRecord{}, fmt.Errorf("invalid JSON body: %w", err)
	}
	return openLibraryDumpRecord{Kind: strings.TrimPrefix(strings.TrimSpace(fields[0]), "/"), Key: strings.TrimSpace(fields[1]), Body: body}, nil
}

func openLibraryChineseEditionLanguage(languages []struct {
	Key string `json:"key"`
}) string {
	for _, language := range languages {
		value := strings.Trim(strings.TrimSpace(language.Key), "/")
		value = strings.TrimPrefix(value, "languages/")
		if value == openLibraryChineseLanguage || value == openLibraryChineseZho {
			return value
		}
	}
	return ""
}

func buildOpenLibraryDumpEdition(key string, payload openLibraryDumpEditionPayload, language string) openLibraryDumpEdition {
	editionID := normalizeOpenLibraryKey(key, "books", "M")
	candidate := openLibraryDumpEdition{
		EditionID: editionID,
		Title:     strings.TrimSpace(payload.Title),
		Publisher: firstNonBlank(payload.Publishers...),
		ISBN10:    firstNonBlank(payload.ISBN10...),
		ISBN13:    firstNonBlank(payload.ISBN13...),
		Language:  language,
		PageCount: payload.NumberOfPages,
		Binding:   strings.TrimSpace(payload.PhysicalFormat),
	}
	if year := parseOpenLibraryYear(payload.PublishDate); year > 0 {
		candidate.PublishedYear = year
	}
	if len(payload.Covers) > 0 && payload.Covers[0] > 0 {
		candidate.CoverURL = "https://covers.openlibrary.org/b/id/" + strconv.Itoa(payload.Covers[0]) + "-M.jpg"
	}
	return candidate
}

func betterOpenLibraryEdition(candidate, current openLibraryDumpEdition) bool {
	candidateCompleteness := openLibraryEditionCompleteness(candidate)
	currentCompleteness := openLibraryEditionCompleteness(current)
	if candidateCompleteness != currentCompleteness {
		return candidateCompleteness > currentCompleteness
	}
	if candidate.PublishedYear != current.PublishedYear {
		return candidate.PublishedYear > current.PublishedYear
	}
	if candidate.ISBN13 != current.ISBN13 {
		return candidate.ISBN13 != ""
	}
	return candidate.EditionID < current.EditionID
}

func openLibraryEditionCompleteness(edition openLibraryDumpEdition) int {
	score := 0
	for _, value := range []string{edition.ISBN13, edition.ISBN10, edition.Publisher, edition.CoverURL, edition.Binding} {
		if strings.TrimSpace(value) != "" {
			score++
		}
	}
	if edition.PublishedYear > 0 {
		score++
	}
	if edition.PageCount > 0 {
		score++
	}
	return score
}

func normalizeOpenLibraryKey(raw, collection, suffix string) string {
	value := strings.TrimSpace(raw)
	value = strings.TrimPrefix(value, "/")
	prefix := collection + "/"
	if strings.HasPrefix(value, prefix) {
		value = strings.TrimPrefix(value, prefix)
	}
	if len(value) < 4 || !strings.HasPrefix(value, "OL") || !strings.HasSuffix(value, suffix) {
		return ""
	}
	for _, character := range value[2 : len(value)-1] {
		if character < '0' || character > '9' {
			return ""
		}
	}
	return "/" + collection + "/" + value
}

func parseOpenLibraryYear(raw string) int {
	match := openLibraryYearPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if len(match) != 2 {
		return 0
	}
	year, err := strconv.Atoi(match[1])
	if err != nil || year < 1 || year > time.Now().Year()+1 {
		return 0
	}
	return year
}

func openLibraryTextValue(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	var object struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(raw, &object) == nil {
		return strings.TrimSpace(object.Value)
	}
	return ""
}
