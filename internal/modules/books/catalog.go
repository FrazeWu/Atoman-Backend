package books

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	openLibraryDefaultBaseURL = "https://openlibrary.org"
	openLibrarySourceBaseURL  = "https://openlibrary.org"
	openLibraryMaxSearchLimit = 100
	openLibraryResponseLimit  = 16 << 20
)

// CatalogAuthor is a contributor returned by a public catalog provider.
type CatalogAuthor struct {
	ExternalID string
	Name       string
}

// CatalogBook contains public bibliographic metadata only. It never contains
// book text or a private object-storage key.
type CatalogBook struct {
	ExternalWorkID    string
	ExternalEditionID string
	Title             string
	Subtitle          string
	Description       string
	Language          string
	Publisher         string
	ISBN10            string
	ISBN13            string
	PublishedYear     int
	PageCount         int
	CoverURL          string
	WorkSourceURL     string
	EditionSourceURL  string
	Authors           []CatalogAuthor
}

// CatalogProvider supplies public bibliographic records for import.
type CatalogProvider interface {
	Search(context.Context, string, int) ([]CatalogBook, error)
}

// OpenLibraryProvider reads metadata from Open Library's public search API.
// The API base URL is injectable so callers can use a test server or a proxy.
type OpenLibraryProvider struct {
	client    *http.Client
	baseURL   *url.URL
	userAgent string
}

func NewOpenLibraryProvider(client *http.Client, baseURL, userAgent string) (*OpenLibraryProvider, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = openLibraryDefaultBaseURL
	}
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("invalid Open Library base URL")
	}
	return &OpenLibraryProvider{
		client:    client,
		baseURL:   parsed,
		userAgent: strings.TrimSpace(userAgent),
	}, nil
}

type openLibrarySearchResponse struct {
	Docs []openLibrarySearchDocument `json:"docs"`
}

type openLibrarySearchDocument struct {
	Key              string   `json:"key"`
	Title            string   `json:"title"`
	Subtitle         string   `json:"subtitle"`
	AuthorKeys       []string `json:"author_key"`
	AuthorNames      []string `json:"author_name"`
	FirstPublishYear int      `json:"first_publish_year"`
	EditionKeys      []string `json:"edition_key"`
	Publisher        []string `json:"publisher"`
	ISBN             []string `json:"isbn"`
	Language         []string `json:"language"`
	PageCountMedian  float64  `json:"number_of_pages_median"`
	CoverID          int      `json:"cover_i"`
}

func (p *OpenLibraryProvider) Search(ctx context.Context, query string, limit int) ([]CatalogBook, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("book catalog search query is required")
	}
	if limit < 1 {
		limit = 20
	}
	if limit > openLibraryMaxSearchLimit {
		limit = openLibraryMaxSearchLimit
	}

	endpoint := *p.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/search.json"
	params := endpoint.Query()
	params.Set("q", query)
	params.Set("limit", strconv.Itoa(limit))
	params.Set("fields", "key,title,subtitle,author_key,author_name,first_publish_year,edition_key,publisher,isbn,language,number_of_pages_median,cover_i")
	endpoint.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create Open Library request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if p.userAgent != "" {
		req.Header.Set("User-Agent", p.userAgent)
	}

	response, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request Open Library: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		return nil, fmt.Errorf("Open Library returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}

	var payload openLibrarySearchResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, openLibraryResponseLimit)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode Open Library response: %w", err)
	}

	books := make([]CatalogBook, 0, len(payload.Docs))
	for _, document := range payload.Docs {
		if len(books) >= limit {
			break
		}
		book, ok := mapOpenLibraryDocument(document)
		if ok {
			books = append(books, book)
		}
	}
	return books, nil
}

func mapOpenLibraryDocument(document openLibrarySearchDocument) (CatalogBook, bool) {
	workID := openLibraryID(document.Key, "/works/", "W")
	if workID == "" || strings.TrimSpace(document.Title) == "" {
		return CatalogBook{}, false
	}

	book := CatalogBook{
		ExternalWorkID: workID,
		Title:          strings.TrimSpace(document.Title),
		Subtitle:       firstNonBlank(document.Subtitle),
		Language:       firstNonBlank(document.Language...),
		Publisher:      firstNonBlank(document.Publisher...),
		PublishedYear:  document.FirstPublishYear,
		PageCount:      positiveRoundedInt(document.PageCountMedian),
		WorkSourceURL:  openLibrarySourceBaseURL + "/works/" + workID,
	}
	if editionID := firstOpenLibraryID(document.EditionKeys, "M"); editionID != "" {
		book.ExternalEditionID = editionID
		book.EditionSourceURL = openLibrarySourceBaseURL + "/books/" + editionID
	}
	if document.CoverID > 0 {
		book.CoverURL = "https://covers.openlibrary.org/b/id/" + strconv.Itoa(document.CoverID) + "-M.jpg"
	}
	book.ISBN10, book.ISBN13 = selectISBNs(document.ISBN)

	maxAuthors := len(document.AuthorNames)
	if len(document.AuthorKeys) < maxAuthors {
		maxAuthors = len(document.AuthorKeys)
	}
	book.Authors = make([]CatalogAuthor, 0, maxAuthors)
	for index := 0; index < maxAuthors; index++ {
		authorID := openLibraryID(document.AuthorKeys[index], "", "A")
		authorName := strings.TrimSpace(document.AuthorNames[index])
		if authorID == "" || authorName == "" {
			continue
		}
		book.Authors = append(book.Authors, CatalogAuthor{ExternalID: authorID, Name: authorName})
	}
	return book, true
}

func openLibraryID(raw, prefix, suffix string) string {
	value := strings.TrimSpace(raw)
	if prefix != "" && !strings.HasPrefix(value, prefix) {
		return ""
	}
	if prefix == "" && strings.HasPrefix(value, "/") {
		return ""
	}
	id := strings.TrimPrefix(value, prefix)
	if len(id) < 4 || !strings.HasPrefix(id, "OL") || !strings.HasSuffix(id, suffix) {
		return ""
	}
	for _, character := range id[2 : len(id)-1] {
		if character < '0' || character > '9' {
			return ""
		}
	}
	return id
}

func firstOpenLibraryID(values []string, suffix string) string {
	for _, value := range values {
		if id := openLibraryID(value, "", suffix); id != "" {
			return id
		}
	}
	return ""
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func positiveRoundedInt(value float64) int {
	if value <= 0 {
		return 0
	}
	if value > 1000000 {
		return 1000000
	}
	return int(value + 0.5)
}

func selectISBNs(values []string) (string, string) {
	var isbn10, isbn13 string
	for _, value := range values {
		compact := normalizeISBN(value)
		if compact == "" {
			continue
		}
		if len(compact) == 10 && isbn10 == "" {
			isbn10 = compact
		}
		if len(compact) == 13 && isbn13 == "" {
			isbn13 = compact
		}
	}
	return isbn10, isbn13
}

func normalizeISBN(value string) string {
	var builder strings.Builder
	for _, character := range strings.TrimSpace(value) {
		switch {
		case character >= '0' && character <= '9':
			builder.WriteRune(character)
		case character == 'X' || character == 'x':
			if builder.Len() != 9 {
				return ""
			}
			builder.WriteRune('X')
		case character == '-' || character == ' ':
		default:
			return ""
		}
	}
	return builder.String()
}
