package music

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"atoman/internal/musiclyrics"
	"atoman/internal/platform/apperr"
)

var lrcLinePattern = regexp.MustCompile(`^\[(\d+):(\d{2})(?:\.(\d{1,3}))?\](.*)$`)

func ParseLyricLines(content, translation, format string) ([]ParsedLyricLine, error) {
	switch format {
	case "plain":
		return parsePlainLyricLines(content, translation), nil
	case "lrc":
		return parseLRCLyricLines(content, translation)
	default:
		return nil, lyricValidationError("format must be plain or lrc")
	}
}

func parsePlainLyricLines(content, translation string) []ParsedLyricLine {
	parsed := musiclyrics.ParsePlain(content, translation)
	lines := make([]ParsedLyricLine, 0, len(parsed))
	for _, line := range parsed {
		lines = append(lines, ParsedLyricLine{
			LineKey: line.LineKey, LineIndex: line.LineIndex,
			Text: line.Text, Translation: line.Translation,
		})
	}
	return lines
}

func parseLRCLyricLines(content, translation string) ([]ParsedLyricLine, error) {
	contentLines, err := parseTimedLRCLines(content)
	if err != nil {
		return nil, err
	}
	translationLines, err := parseTimedLRCLines(translation)
	if err != nil {
		return nil, err
	}

	translationsByTime := make(map[int]string, len(translationLines))
	for _, line := range translationLines {
		translationsByTime[*line.TimeMS] = line.Text
	}
	for index := range contentLines {
		contentLines[index].LineIndex = index
		contentLines[index].Translation = translationsByTime[*contentLines[index].TimeMS]
	}
	return contentLines, nil
}

func parseTimedLRCLines(content string) ([]ParsedLyricLine, error) {
	lines := make([]ParsedLyricLine, 0)
	keyOccurrences := make(map[string]int)
	for _, rawLine := range splitLyricLines(content) {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}

		matches := lrcLinePattern.FindStringSubmatch(line)
		if matches == nil {
			return nil, lyricValidationError("each LRC line must contain a valid timestamp")
		}
		minutes, err := strconv.Atoi(matches[1])
		if err != nil {
			return nil, lyricValidationError("LRC minutes are invalid")
		}
		seconds, err := strconv.Atoi(matches[2])
		if err != nil {
			return nil, lyricValidationError("LRC seconds are invalid")
		}
		if seconds >= 60 {
			return nil, lyricValidationError("LRC seconds must be less than 60")
		}
		milliseconds := fractionMilliseconds(matches[3])
		maxInt := int(^uint(0) >> 1)
		remainingMS := seconds*1000 + milliseconds
		if minutes > (maxInt-remainingMS)/60000 {
			return nil, lyricValidationError("LRC timestamp is too large")
		}
		timeMS := minutes*60000 + remainingMS
		text := strings.TrimSpace(matches[4])
		baseKey := fmt.Sprintf("lrc:%d:%s", timeMS, lyricTextFingerprint(text))
		occurrence := keyOccurrences[baseKey]
		keyOccurrences[baseKey] = occurrence + 1
		lines = append(lines, ParsedLyricLine{
			LineKey: fmt.Sprintf("%s:%d", baseKey, occurrence),
			TimeMS:  &timeMS,
			Text:    text,
		})
	}
	return lines, nil
}

func plainLyricLines(content string) []string {
	return musiclyrics.PlainLines(content)
}

func splitLyricLines(content string) []string {
	return musiclyrics.SplitLines(content)
}

func fractionMilliseconds(fraction string) int {
	switch len(fraction) {
	case 1:
		fraction += "00"
	case 2:
		fraction += "0"
	case 0:
		return 0
	}
	milliseconds, _ := strconv.Atoi(fraction)
	return milliseconds
}

func lyricTextFingerprint(text string) string {
	return musiclyrics.TextFingerprint(text)
}

func ValidateAnnotationAnchor(text string, startOffset, endOffset int, selectedText string) error {
	units := utf16.Encode([]rune(text))
	if startOffset < 0 || startOffset >= endOffset || endOffset > len(units) ||
		splitsUTF16SurrogatePair(units, startOffset) || splitsUTF16SurrogatePair(units, endOffset) {
		return lyricValidationError("annotation offsets are invalid")
	}
	selectedUnits := utf16.Encode([]rune(selectedText))
	anchorUnits := units[startOffset:endOffset]
	if len(anchorUnits) != len(selectedUnits) {
		return lyricValidationError("selected_text does not match the lyric text")
	}
	for index := range anchorUnits {
		if anchorUnits[index] != selectedUnits[index] {
			return lyricValidationError("selected_text does not match the lyric text")
		}
	}
	return nil
}

func splitsUTF16SurrogatePair(units []uint16, offset int) bool {
	return offset > 0 && offset < len(units) &&
		units[offset-1] >= 0xD800 && units[offset-1] <= 0xDBFF &&
		units[offset] >= 0xDC00 && units[offset] <= 0xDFFF
}

func lyricValidationError(message string) error {
	return apperr.BadRequest("validation.invalid_request", message)
}
