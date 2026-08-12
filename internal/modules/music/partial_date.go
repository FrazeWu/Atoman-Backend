package music

import (
	"fmt"
	"time"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/partialdate"
)

func parsePartialDate(raw string, fieldName string) (*time.Time, string, error) {
	parsed, precision, err := partialdate.Parse(raw)
	if err != nil {
		return nil, "", invalidPartialDate(fieldName)
	}
	return parsed, precision, nil
}

func invalidPartialDate(fieldName string) error {
	return apperr.BadRequest(
		"validation.invalid_request",
		fmt.Sprintf("%s must be YYYY-MM-DD, YYYY/MM/--, YYYY/--/-- or ----/--/--", fieldName),
	)
}

func parseOptionalReleaseDate(raw string) (*time.Time, string, error) {
	return parsePartialDate(raw, "release_date")
}

func parseOptionalDate(raw string, fieldName string) (*time.Time, string, error) {
	return parsePartialDate(raw, fieldName)
}
