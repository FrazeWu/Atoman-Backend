package timeline

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

var dateLayouts = []string{
	"2006-01-02T15:04",
	"2006-01-02T15:04:05",
	time.RFC3339,
	"2006-01-02 15:04",
	"2006-01-02",
}

// ParseDateTime supports the precision accepted by Timeline editors, including BCE years.
func ParseDateTime(value string) (time.Time, error) {
	if strings.HasPrefix(value, "-") {
		rest := strings.TrimPrefix(value, "-")
		separator := strings.Index(rest, "-")
		if separator > 0 {
			year, err := strconv.Atoi(rest[:separator])
			if err == nil {
				positive := fmt.Sprintf("%04d%s", year, rest[separator:])
				for _, layout := range dateLayouts {
					if parsed, parseErr := time.Parse(layout, positive); parseErr == nil {
						return time.Date(-year, parsed.Month(), parsed.Day(), parsed.Hour(), parsed.Minute(), parsed.Second(), parsed.Nanosecond(), parsed.Location()), nil
					}
				}
			}
		}
	}
	for _, layout := range dateLayouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, &time.ParseError{Value: value}
}
