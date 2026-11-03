package books

import (
	"context"
	"sort"
	"strings"
	"unicode"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
)

// Duplicate checks are suggestions, never automatic merges. They do not
// serialize with creation; an idempotency key handles network retries.
type CheckBookWorkDuplicatesInput struct {
	Title         string      `json:"title"`
	OriginalTitle string      `json:"original_title"`
	Authors       []string    `json:"authors"`
	AuthorIDs     []uuid.UUID `json:"author_ids"`
}

type BookWorkDuplicateMatch struct {
	Work   BookPublicWorkDTO `json:"work"`
	Reason string            `json:"reason"`
	Score  int               `json:"score"`
}

func normalizeCatalogComparison(value string) string {
	return strings.ToLower(strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(value)))
}

func (s *Service) CheckBookWorkDuplicates(ctx context.Context, input CheckBookWorkDuplicatesInput) ([]BookWorkDuplicateMatch, error) {
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return nil, apperr.BadRequest("books.title_required", "Book title is required")
	}
	var all []BookPublicWorkDTO
	seen := map[string]bool{}
	for _, query := range []string{title, strings.TrimSpace(input.OriginalTitle)} {
		if query == "" {
			continue
		}
		items, _, err := s.SearchBookWorks(ctx, query, 40, 0)
		if err != nil {
			return nil, err
		}
		for _, work := range items {
			if !seen[work.ID] {
				all = append(all, work)
				seen[work.ID] = true
			}
		}
	}
	authorIDs := map[string]bool{}
	for _, id := range input.AuthorIDs {
		authorIDs[id.String()] = true
	}
	authorNames := map[string]bool{}
	for _, name := range input.Authors {
		authorNames[normalizeBookPersonName(name)] = true
	}
	results := make([]BookWorkDuplicateMatch, 0)
	for _, work := range all {
		score := 0
		reason := ""
		normalizedTitle := normalizeCatalogComparison(title)
		normalizedOriginal := normalizeCatalogComparison(input.OriginalTitle)
		if normalizedTitle == normalizeCatalogComparison(work.Title) || (normalizedOriginal != "" && normalizedOriginal == normalizeCatalogComparison(work.OriginalTitle)) {
			score, reason = 80, "same_title"
		} else if normalizedTitle == normalizeCatalogComparison(work.OriginalTitle) || (normalizedOriginal != "" && normalizedOriginal == normalizeCatalogComparison(work.Title)) {
			score, reason = 75, "cross_language_title"
		} else if strings.Contains(normalizeCatalogComparison(work.Title), normalizedTitle) {
			score, reason = 55, "similar_title"
		}
		if score == 0 {
			continue
		}
		for _, person := range work.Authors {
			if authorIDs[person.ID] || authorNames[normalizeBookPersonName(person.Name)] {
				score += 15
				reason += "_author"
				break
			}
		}
		results = append(results, BookWorkDuplicateMatch{Work: work, Reason: reason, Score: score})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Work.ID < results[j].Work.ID
	})
	if len(results) > 12 {
		results = results[:12]
	}
	return results, nil
}

type BookEditionDuplicateMatch struct {
	Edition BookPublicEditionDTO `json:"edition"`
	Reason  string               `json:"reason"`
	Score   int                  `json:"score"`
}

func (s *Service) CheckBookEditionDuplicates(ctx context.Context, workID uuid.UUID, input CreateBookEditionInput) ([]BookEditionDuplicateMatch, error) {
	if err := validateCatalogEditionCreate(&input); err != nil {
		return nil, err
	}
	query := s.db.WithContext(ctx).Model(&model.BookEdition{}).
		Where("lifecycle_status = ?", model.BookLifecycleStatusActive)
	if input.ISBN10 != "" && input.ISBN13 != "" {
		query = query.Where("work_id = ? OR isbn10 = ? OR isbn13 = ?", workID, input.ISBN10, input.ISBN13)
	} else if input.ISBN10 != "" {
		query = query.Where("work_id = ? OR isbn10 = ?", workID, input.ISBN10)
	} else if input.ISBN13 != "" {
		query = query.Where("work_id = ? OR isbn13 = ?", workID, input.ISBN13)
	} else {
		query = query.Where("work_id = ?", workID)
	}
	var editions []model.BookEdition
	if err := query.Order("id ASC").Limit(120).Find(&editions).Error; err != nil {
		return nil, err
	}
	results := make([]BookEditionDuplicateMatch, 0)
	for _, edition := range editions {
		score := 0
		reason := ""
		switch {
		case input.ISBN10 != "" && input.ISBN10 == normalizeISBN(edition.ISBN10),
			input.ISBN13 != "" && input.ISBN13 == normalizeISBN(edition.ISBN13):
			score, reason = 100, "same_isbn"
		case edition.WorkID == workID && strings.EqualFold(input.Language, strings.TrimSpace(edition.Language)):
			samePublisher := strings.EqualFold(strings.TrimSpace(input.Publisher), strings.TrimSpace(edition.Publisher)) && strings.TrimSpace(input.Publisher) != ""
			sameStatement := normalizeCatalogComparison(input.EditionStatement) != "" && normalizeCatalogComparison(input.EditionStatement) == normalizeCatalogComparison(edition.EditionStatement)
			sameTitle := normalizeCatalogComparison(input.Title) != "" && normalizeCatalogComparison(input.Title) == normalizeCatalogComparison(edition.Title)
			sameDate := input.PublishedDate != "" && edition.PublishedDate != nil && edition.PublishedDate.Format("2006-01-02") == input.PublishedDate
			if samePublisher && (sameStatement || sameDate) {
				score, reason = 90, "same_publication"
			} else if sameTitle && samePublisher {
				score, reason = 75, "same_title_publisher"
			}
		}
		if score > 0 {
			results = append(results, BookEditionDuplicateMatch{Edition: buildBookPublicEditionDTO(edition, nil, nil), Reason: reason, Score: score})
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Edition.ID < results[j].Edition.ID
	})
	if len(results) > 12 {
		results = results[:12]
	}
	return results, nil
}
