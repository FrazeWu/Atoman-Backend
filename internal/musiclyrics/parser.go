package musiclyrics

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

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
