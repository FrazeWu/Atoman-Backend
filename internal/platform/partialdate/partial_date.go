package partialdate

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

const (
	Day   = "day"
	Month = "month"
	Year  = "year"
)

var pattern = regexp.MustCompile(`^(\d{4})(?:[-/](\d{2}|--)[-/](\d{2}|--))?$`)

func Parse(raw string) (*time.Time, string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, "", nil
	}
	parts := pattern.FindStringSubmatch(trimmed)
	if parts == nil {
		return nil, "", errors.New("invalid partial date")
	}

	year, month, day := parts[1], parts[2], parts[3]
	precision := Day
	switch {
	case month == "" || ((month == "--" || month == "00") && (day == "--" || day == "00")):
		month, day, precision = "01", "01", Year
	case month != "--" && month != "00" && (day == "--" || day == "00"):
		day, precision = "01", Month
	case month == "--" || month == "00":
		return nil, "", errors.New("invalid partial date")
	}
	parsed, err := time.Parse("2006-01-02", year+"-"+month+"-"+day)
	if err != nil {
		return nil, "", errors.New("invalid partial date")
	}
	return &parsed, precision, nil
}

func Format(value time.Time, precision string) string {
	if value.IsZero() {
		return ""
	}
	switch precision {
	case Year:
		return value.Format("2006/--/--")
	case Month:
		return value.Format("2006/01/--")
	default:
		return value.Format("2006-01-02")
	}
}
