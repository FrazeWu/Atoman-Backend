package comment

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

type TimeAnchor struct {
	Start   int
	End     int
	Seconds int
}

var timeAnchorPattern = regexp.MustCompile(`\d+(?::\d{2}){1,2}`)

func ParseTimeAnchors(content string, durationSec int) []TimeAnchor {
	anchors := make([]TimeAnchor, 0)
	for _, match := range timeAnchorPattern.FindAllStringIndex(content, -1) {
		if !hasTimeTokenBoundaries(content, match[0], match[1]) {
			continue
		}
		seconds, valid := parseTimeToken(content[match[0]:match[1]])
		if !valid || durationSec > 0 && seconds > durationSec {
			continue
		}
		anchors = append(anchors, TimeAnchor{
			Start:   utf8.RuneCountInString(content[:match[0]]),
			End:     utf8.RuneCountInString(content[:match[1]]),
			Seconds: seconds,
		})
	}
	return anchors
}

func parseTimeToken(token string) (int, bool) {
	maxInt := int(^uint(0) >> 1)
	parts := strings.Split(token, ":")
	values := make([]int, len(parts))
	for i, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil {
			return 0, false
		}
		values[i] = value
	}

	switch len(values) {
	case 2:
		if values[1] > 59 {
			return 0, false
		}
		if values[0] > (maxInt-values[1])/60 {
			return 0, false
		}
		return values[0]*60 + values[1], true
	case 3:
		if values[1] > 59 || values[2] > 59 {
			return 0, false
		}
		tailSeconds := values[1]*60 + values[2]
		if values[0] > (maxInt-tailSeconds)/3600 {
			return 0, false
		}
		return values[0]*3600 + tailSeconds, true
	default:
		return 0, false
	}
}

func hasTimeTokenBoundaries(content string, start, end int) bool {
	if start > 0 {
		previous, _ := utf8.DecodeLastRuneInString(content[:start])
		if isTimeTokenRune(previous) {
			return false
		}
	}
	if end < len(content) {
		next, _ := utf8.DecodeRuneInString(content[end:])
		if isTimeTokenRune(next) {
			return false
		}
	}
	return true
}

func isTimeTokenRune(current rune) bool {
	return current >= '0' && current <= '9' || current == ':'
}
