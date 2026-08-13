package musiclyrics

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type LRCLine struct {
	LineKey     string
	LineIndex   int
	TimeMS      *int
	Text        string
	Translation string
}

var lrcLinePattern = regexp.MustCompile(`^\[(\d+):(\d{2})(?:\.(\d{1,3}))?\](.*)$`)
var lrcMetadataPattern = regexp.MustCompile(`^\[[A-Za-z][A-Za-z0-9_-]*:.*\]$`)

func ParseLRC(content, translation string) ([]LRCLine, error) {
	contentLines, err := parseLRCLines(content, false)
	if err != nil {
		return nil, err
	}
	translationLines, err := parseLRCLines(translation, true)
	if err != nil {
		return nil, err
	}
	translationsByTime := make(map[int][]string, len(translationLines))
	for _, line := range translationLines {
		if line.TimeMS == nil {
			continue
		}
		timeMS := *line.TimeMS
		translationsByTime[timeMS] = append(translationsByTime[timeMS], line.Text)
	}
	translationOccurrences := make(map[int]int, len(translationsByTime))
	for index := range contentLines {
		if contentLines[index].TimeMS == nil {
			continue
		}
		timeMS := *contentLines[index].TimeMS
		occurrence := translationOccurrences[timeMS]
		translations := translationsByTime[timeMS]
		if occurrence < len(translations) {
			contentLines[index].Translation = translations[occurrence]
			translationOccurrences[timeMS] = occurrence + 1
		}
	}
	if len(contentLines) == 0 {
		return nil, fmt.Errorf("no timed LRC lines")
	}
	return contentLines, nil
}

func parseLRCLines(content string, keepEmpty bool) ([]LRCLine, error) {
	lines := make([]LRCLine, 0)
	keyOccurrences := make(map[string]int)
	for _, rawLine := range SplitLines(content) {
		line := strings.TrimSpace(strings.TrimPrefix(rawLine, "\uFEFF"))
		if line == "" || lrcMetadataPattern.MatchString(line) {
			continue
		}
		matches := lrcLinePattern.FindStringSubmatch(line)
		if matches == nil {
			return nil, fmt.Errorf("each LRC line must contain a valid timestamp")
		}
		minutes, err := strconv.Atoi(matches[1])
		if err != nil {
			return nil, fmt.Errorf("LRC minutes are invalid")
		}
		seconds, err := strconv.Atoi(matches[2])
		if err != nil || seconds >= 60 {
			return nil, fmt.Errorf("LRC seconds are invalid")
		}
		fraction := matches[3]
		switch len(fraction) {
		case 0:
			fraction = "0"
		case 1:
			fraction += "00"
		case 2:
			fraction += "0"
		default:
			fraction = fraction[:3]
		}
		fractionMS, err := strconv.Atoi(fraction)
		if err != nil {
			return nil, fmt.Errorf("LRC fraction is invalid")
		}
		timeMS := minutes*60000 + seconds*1000 + fractionMS
		text := strings.TrimSpace(matches[4])
		if text == "" && !keepEmpty {
			continue
		}
		baseKey := fmt.Sprintf("lrc:%d:%s", timeMS, TextFingerprint(text))
		occurrence := keyOccurrences[baseKey]
		keyOccurrences[baseKey] = occurrence + 1
		lines = append(lines, LRCLine{
			LineKey: fmt.Sprintf("%s:%d", baseKey, occurrence), LineIndex: len(lines),
			TimeMS: &timeMS, Text: text,
		})
	}
	return lines, nil
}

type PlainLine struct {
	LineKey     string
	LineIndex   int
	Text        string
	Translation string
}

func ParsePlain(content, translation string) []PlainLine {
	contentLines := PlainLines(content)
	translationLines := PlainLines(translation)
	lines := make([]PlainLine, 0, len(contentLines))
	occurrences := make(map[string]int)
	for index, text := range contentLines {
		fingerprint := TextFingerprint(text)
		occurrence := occurrences[fingerprint]
		occurrences[fingerprint] = occurrence + 1
		line := PlainLine{
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

func PlainLines(content string) []string {
	if content == "" {
		return []string{}
	}
	lines := SplitLines(content)
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for index := range lines {
		lines[index] = strings.TrimSpace(lines[index])
	}
	return lines
}

func SplitLines(content string) []string {
	normalized := strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(content)
	return strings.Split(normalized, "\n")
}

func TextFingerprint(text string) string {
	normalized := strings.Join(strings.Fields(text), " ")
	return fmt.Sprintf("%x", sha256.Sum256([]byte(normalized)))
}
