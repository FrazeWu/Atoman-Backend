package music

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strconv"
	"strings"

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
	contentLines := readableLyricLines(content)
	translationLines := readableLyricLines(translation)
	lines := make([]ParsedLyricLine, 0, len(contentLines))

	for index, text := range contentLines {
		line := ParsedLyricLine{
			LineKey:   fmt.Sprintf("plain:%d:%s", index, lyricTextFingerprint(text)),
			LineIndex: index,
			Text:      text,
		}
		if index < len(translationLines) {
			line.Translation = translationLines[index]
		}
		lines = append(lines, line)
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
	for _, rawLine := range splitLyricLines(content) {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}

		matches := lrcLinePattern.FindStringSubmatch(line)
		if matches == nil {
			return nil, lyricValidationError("each LRC line must contain a valid timestamp")
		}
		minutes, _ := strconv.Atoi(matches[1])
		seconds, _ := strconv.Atoi(matches[2])
		if seconds >= 60 {
			return nil, lyricValidationError("LRC seconds must be less than 60")
		}
		milliseconds := fractionMilliseconds(matches[3])
		timeMS := (minutes*60+seconds)*1000 + milliseconds
		text := strings.TrimSpace(matches[4])
		lines = append(lines, ParsedLyricLine{
			LineKey: fmt.Sprintf("lrc:%d:%s", timeMS, lyricTextFingerprint(text)),
			TimeMS:  &timeMS,
			Text:    text,
		})
	}
	return lines, nil
}

func readableLyricLines(content string) []string {
	lines := make([]string, 0)
	for _, rawLine := range splitLyricLines(content) {
		line := strings.TrimSpace(rawLine)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func splitLyricLines(content string) []string {
	return strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
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
	normalized := strings.Join(strings.Fields(text), " ")
	return fmt.Sprintf("%x", sha256.Sum256([]byte(normalized)))
}

func ValidateAnnotationAnchor(text string, startOffset, endOffset int, selectedText string) error {
	runes := []rune(text)
	if startOffset < 0 || startOffset >= endOffset || endOffset > len(runes) {
		return lyricValidationError("annotation offsets are invalid")
	}
	if string(runes[startOffset:endOffset]) != selectedText {
		return lyricValidationError("selected_text does not match the lyric text")
	}
	return nil
}

func lyricValidationError(message string) error {
	return apperr.BadRequest("validation.invalid_request", message)
}
