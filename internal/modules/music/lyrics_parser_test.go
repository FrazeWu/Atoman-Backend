package music

import (
	"errors"
	"strings"
	"testing"

	"atoman/internal/platform/apperr"
)

func TestParseLyricLinesPlainPreservesOrderWithoutTime(t *testing.T) {
	lines, err := ParseLyricLines("First line\nSecond line", "", "plain")
	if err != nil {
		t.Fatalf("parse plain lyrics: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if lines[0].LineIndex != 0 || lines[0].Text != "First line" || lines[0].TimeMS != nil {
		t.Fatalf("unexpected first line: %#v", lines[0])
	}
	if lines[1].LineIndex != 1 || lines[1].Text != "Second line" || lines[1].TimeMS != nil {
		t.Fatalf("unexpected second line: %#v", lines[1])
	}
	if lines[0].LineKey == "" || lines[0].LineKey == lines[1].LineKey {
		t.Fatalf("expected distinct stable line keys, got %#v", lines)
	}
}

func TestParseLyricLinesPlainMergesTranslationByLine(t *testing.T) {
	lines, err := ParseLyricLines("Hello\nWorld", "你好\n世界", "plain")
	if err != nil {
		t.Fatalf("parse plain lyrics: %v", err)
	}
	if lines[0].Translation != "你好" || lines[1].Translation != "世界" {
		t.Fatalf("unexpected translations: %#v", lines)
	}
}

func TestParseLyricLinesPlainKeepsEmptyTranslationPosition(t *testing.T) {
	lines, err := ParseLyricLines("Hello\nWorld", "你好\n\n世界", "plain")
	if err != nil {
		t.Fatalf("parse plain lyrics: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %#v", lines)
	}
	if lines[0].Translation != "你好" || lines[1].Translation != "" {
		t.Fatalf("expected translations to stay aligned by physical line, got %#v", lines)
	}
}

func TestParseLyricLinesPlainPreservesInternalEmptyContentLine(t *testing.T) {
	lines, err := ParseLyricLines("Hello\n\nWorld\n", "你好\n\n世界\n", "plain")
	if err != nil {
		t.Fatalf("parse plain lyrics: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("expected trailing empty item removed and internal empty line kept, got %#v", lines)
	}
	if lines[1].LineIndex != 1 || lines[1].Text != "" || lines[1].Translation != "" {
		t.Fatalf("unexpected internal empty line: %#v", lines[1])
	}
	if lines[2].LineIndex != 2 || lines[2].Text != "World" || lines[2].Translation != "世界" {
		t.Fatalf("unexpected line after internal empty line: %#v", lines[2])
	}
}

func TestParseLyricLinesProducesStableLineKeys(t *testing.T) {
	tests := []struct {
		name, content, translation, format string
	}{
		{name: "plain", content: "Hello\n\nWorld", translation: "你好\n\n世界", format: "plain"},
		{name: "lrc", content: "[00:01.20]Hello\n[00:02.00]World", translation: "[00:02.00]世界", format: "lrc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first, err := ParseLyricLines(tt.content, tt.translation, tt.format)
			if err != nil {
				t.Fatalf("first parse: %v", err)
			}
			second, err := ParseLyricLines(tt.content, tt.translation, tt.format)
			if err != nil {
				t.Fatalf("second parse: %v", err)
			}
			if len(first) != len(second) {
				t.Fatalf("line counts differ: %d != %d", len(first), len(second))
			}
			for i := range first {
				if first[i].LineKey != second[i].LineKey {
					t.Fatalf("line %d key is unstable: %q != %q", i, first[i].LineKey, second[i].LineKey)
				}
			}
		})
	}
}

func TestParseLyricLinesPlainKeysSurviveDifferentLineInsertion(t *testing.T) {
	original, err := ParseLyricLines("A\nB", "", "plain")
	if err != nil {
		t.Fatalf("parse original lyrics: %v", err)
	}
	inserted, err := ParseLyricLines("X\nA\nB", "", "plain")
	if err != nil {
		t.Fatalf("parse lyrics with inserted line: %v", err)
	}
	if original[0].LineKey != inserted[1].LineKey || original[1].LineKey != inserted[2].LineKey {
		t.Fatalf("expected A/B keys to survive insertion: original=%#v inserted=%#v", original, inserted)
	}
}

func TestParseLyricLinesPlainRepeatedTextGetsDistinctStableKeys(t *testing.T) {
	first, err := ParseLyricLines("A\nA", "", "plain")
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}
	second, err := ParseLyricLines("A\nA", "", "plain")
	if err != nil {
		t.Fatalf("second parse: %v", err)
	}
	if first[0].LineKey == first[1].LineKey {
		t.Fatalf("expected repeated text to have distinct keys, got %#v", first)
	}
	if first[0].LineKey != second[0].LineKey || first[1].LineKey != second[1].LineKey {
		t.Fatalf("expected repeated text keys to be stable: first=%#v second=%#v", first, second)
	}
}

func TestParseLyricLinesLRCParsesMillisecondsAndMergesTranslationByTime(t *testing.T) {
	lines, err := ParseLyricLines("[00:01.20]Hello", "[00:01.20]你好", "lrc")
	if err != nil {
		t.Fatalf("parse LRC lyrics: %v", err)
	}
	if len(lines) != 1 || lines[0].TimeMS == nil || *lines[0].TimeMS != 1200 {
		t.Fatalf("expected timestamp 1200ms, got %#v", lines)
	}
	if lines[0].Text != "Hello" || lines[0].Translation != "你好" {
		t.Fatalf("unexpected LRC line: %#v", lines[0])
	}
}

func TestParseLyricLinesLRCAcceptsOneTwoAndThreeFractionDigits(t *testing.T) {
	lines, err := ParseLyricLines("[00:01.2]one\n[00:02.25]two\n[00:03.375]three", "", "lrc")
	if err != nil {
		t.Fatalf("parse LRC lyrics: %v", err)
	}
	want := []int{1200, 2250, 3375}
	for i, line := range lines {
		if line.TimeMS == nil || *line.TimeMS != want[i] {
			t.Fatalf("line %d: expected %dms, got %#v", i, want[i], line.TimeMS)
		}
	}
}

func TestParseLyricLinesHandlesCRLF(t *testing.T) {
	plain, err := ParseLyricLines("Hello\r\nWorld\r\n", "你好\r\n世界\r\n", "plain")
	if err != nil {
		t.Fatalf("parse CRLF plain lyrics: %v", err)
	}
	if len(plain) != 2 || plain[1].Text != "World" || plain[1].Translation != "世界" {
		t.Fatalf("unexpected CRLF plain lines: %#v", plain)
	}

	lrc, err := ParseLyricLines("[00:01.00]Hello\r\n[00:02.00]World\r\n", "", "lrc")
	if err != nil {
		t.Fatalf("parse CRLF LRC lyrics: %v", err)
	}
	if len(lrc) != 2 || lrc[1].Text != "World" {
		t.Fatalf("unexpected CRLF LRC lines: %#v", lrc)
	}
}

func TestParseLyricLinesHandlesBareCarriageReturns(t *testing.T) {
	plain, err := ParseLyricLines("Hello\rWorld\r", "你好\r世界\r", "plain")
	if err != nil {
		t.Fatalf("parse bare CR plain lyrics: %v", err)
	}
	if len(plain) != 2 || plain[1].Text != "World" || plain[1].Translation != "世界" {
		t.Fatalf("unexpected bare CR plain lines: %#v", plain)
	}

	lrc, err := ParseLyricLines("[00:01.00]Hello\r[00:02.00]World\r", "", "lrc")
	if err != nil {
		t.Fatalf("parse bare CR LRC lyrics: %v", err)
	}
	if len(lrc) != 2 || lrc[1].Text != "World" {
		t.Fatalf("unexpected bare CR LRC lines: %#v", lrc)
	}
}

func TestParseLyricLinesEmptyContent(t *testing.T) {
	for _, format := range []string{"plain", "lrc"} {
		lines, err := ParseLyricLines("", "", format)
		if err != nil {
			t.Fatalf("format %s: parse empty lyrics: %v", format, err)
		}
		if len(lines) != 0 {
			t.Fatalf("format %s: expected no lines, got %#v", format, lines)
		}
	}
}

func TestParseLyricLinesRejectsInvalidNonEmptyLRCLine(t *testing.T) {
	_, err := ParseLyricLines("[00:01.00]Hello\nnot timed", "", "lrc")
	assertValidationError(t, err)
}

func TestParseLyricLinesRejectsInvalidLRCSeconds(t *testing.T) {
	_, err := ParseLyricLines("[00:60.00]Invalid", "", "lrc")
	assertValidationError(t, err)
}

func TestParseLyricLinesRejectsLRCMinutesOutsideIntegerRange(t *testing.T) {
	minutes := strings.Repeat("9", 100)
	_, err := ParseLyricLines("["+minutes+":01.00]Invalid", "", "lrc")
	assertValidationError(t, err)
}

func TestParseLyricLinesRejectsUnknownFormat(t *testing.T) {
	_, err := ParseLyricLines("Hello", "", "srt")
	assertValidationError(t, err)
}

func TestValidateAnnotationAnchorUsesUTF16OffsetsForChinese(t *testing.T) {
	if err := ValidateAnnotationAnchor("你好吗 world", 1, 3, "好吗"); err != nil {
		t.Fatalf("validate Unicode anchor: %v", err)
	}
}

func TestValidateAnnotationAnchorUsesUTF16OffsetsForEmoji(t *testing.T) {
	if err := ValidateAnnotationAnchor("a😀b", 1, 3, "😀"); err != nil {
		t.Fatalf("validate emoji anchor: %v", err)
	}
}

func TestValidateAnnotationAnchorRejectsSplitSurrogatePair(t *testing.T) {
	assertValidationError(t, ValidateAnnotationAnchor("a😀b", 1, 2, "😀"))
}

func TestValidateAnnotationAnchorRejectsInvalidAnchors(t *testing.T) {
	tests := []struct {
		name         string
		start, end   int
		selectedText string
	}{
		{name: "out of bounds", start: 0, end: 10, selectedText: "你好吗 world"},
		{name: "empty range", start: 1, end: 1, selectedText: ""},
		{name: "text mismatch", start: 0, end: 1, selectedText: "好"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertValidationError(t, ValidateAnnotationAnchor("你好吗 world", tt.start, tt.end, tt.selectedText))
		})
	}
}

func assertValidationError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected validation error")
	}
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected app error, got %T: %v", err, err)
	}
	if appErr.Code != "validation.invalid_request" {
		t.Fatalf("expected validation.invalid_request, got %#v", appErr)
	}
}
