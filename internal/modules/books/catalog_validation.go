package books

import (
	"strings"

	"atoman/internal/platform/apperr"

	"golang.org/x/text/language"
)

// validateCatalogEditionCreate makes public editions identifiable without
// requiring an ISBN. Private ebook imports are intentionally unaffected.
func validateCatalogEditionCreate(input *CreateBookEditionInput) error {
	lang := strings.TrimSpace(input.Language)
	if lang == "" {
		return apperr.BadRequest("books.edition_language_required", "A publication edition must specify a language")
	}
	tag, err := language.Parse(lang)
	if err != nil || tag == language.Und {
		return apperr.BadRequest("books.invalid_language", "Use a language code such as zh or en")
	}
	input.Language = tag.String()
	for _, isbn := range []*string{&input.ISBN10, &input.ISBN13} {
		raw := strings.TrimSpace(*isbn)
		if raw == "" {
			*isbn = ""
			continue
		}
		clean := normalizeISBN(raw)
		if clean == "" || (isbn == &input.ISBN10 && !isValidISBN10(clean)) || (isbn == &input.ISBN13 && !isValidISBN13(clean)) {
			return apperr.BadRequest("books.invalid_isbn", "ISBN must have a valid format and checksum")
		}
		*isbn = clean
	}
	if input.ISBN10 != "" && input.ISBN13 != "" && isbn10To13(input.ISBN10) != input.ISBN13 {
		return apperr.BadRequest("books.isbn_mismatch", "ISBN-10 and ISBN-13 identify different publications")
	}
	if input.ISBN10 != "" || input.ISBN13 != "" {
		return nil
	}
	// No ISBN is perfectly valid, but a language alone, or a title alone,
	// cannot identify a publication edition.
	publisher := strings.TrimSpace(input.Publisher)
	producer := strings.TrimSpace(input.Producer)
	statement := strings.TrimSpace(input.EditionStatement)
	published := strings.TrimSpace(input.PublishedDate)
	hasTranslator := len(input.Translators) > 0 || len(input.TranslatorIDs) > 0
	if (publisher != "" && (statement != "" || published != "" || hasTranslator)) ||
		(producer != "" && (statement != "" || published != "")) ||
		(published != "" && hasTranslator && strings.TrimSpace(input.Title) != "") {
		return nil
	}
	return apperr.BadRequest("books.edition_identifier_required", "Without an ISBN, specify distinguishing publication details such as a publisher and a publication date, edition statement or translator")
}

func isValidISBN10(value string) bool {
	if len(value) != 10 {
		return false
	}
	sum := 0
	for i := 0; i < len(value); i++ {
		digit := 0
		if value[i] == 'X' && i == 9 {
			digit = 10
		} else if value[i] >= '0' && value[i] <= '9' {
			digit = int(value[i] - '0')
		} else {
			return false
		}
		sum += (10 - i) * digit
	}
	return sum%11 == 0
}

func isValidISBN13(value string) bool {
	if len(value) != 13 {
		return false
	}
	sum := 0
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
		weight := 1
		if i%2 == 1 {
			weight = 3
		}
		sum += int(value[i]-'0') * weight
	}
	return sum%10 == 0
}

func isbn10To13(value string) string {
	base := "978" + value[:9]
	sum := 0
	for i := range base {
		weight := 1
		if i%2 == 1 {
			weight = 3
		}
		sum += int(base[i]-'0') * weight
	}
	return base + string(rune('0'+byte((10-sum%10)%10)))
}
