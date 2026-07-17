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
	contentLines := plainLyricLines(content)
	translationLines := plainLyricLines(translation)
	lines := make([]ParsedLyricLine, 0, len(contentLines))
	fingerprintOccurrences := make(map[string]int)

	for index, text := range contentLines {
		fingerprint := lyricTextFingerprint(text)
		occurrence := fingerprintOccurrences[fingerprint]
		fingerprintOccurrences[fingerprint] = occurrence + 1
		line := ParsedLyricLine{
			LineKey:   fmt.Sprintf("plain:%s:%d", fingerprint, occurrence),
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
		lines = append(lines, ParsedLyricLine{
			LineKey: fmt.Sprintf("lrc:%d:%s", timeMS, lyricTextFingerprint(text)),
			TimeMS:  &timeMS,
			Text:    text,
		})
	}
	return lines, nil
}

func plainLyricLines(content string) []string {
	if content == "" {
		return []string{}
	}

	lines := splitLyricLines(content)
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for index := range lines {
		lines[index] = strings.TrimSpace(lines[index])
	}
	return lines
}

func splitLyricLines(content string) []string {
	normalized := strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(content)
	return strings.Split(normalized, "\n")
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
