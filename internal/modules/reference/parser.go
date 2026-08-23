package reference

import (
	"fmt"
	"regexp"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

var canonicalUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

func Parse(content string) ([]ParsedReference, error) {
	return parse(content, true)
}

func ParseLenient(content string) []ParsedReference {
	result, _ := parse(content, false)
	return result
}

func parse(content string, strict bool) ([]ParsedReference, error) {
	source := []byte(content)
	document := goldmark.DefaultParser().Parse(text.NewReader(source))
	result := make([]ParsedReference, 0)
	pendingStart, pendingStop := -1, -1
	flush := func() error {
		if pendingStart < 0 {
			return nil
		}
		parsed, err := parseTextSegment(source, pendingStart, pendingStop, strict)
		if err != nil {
			return err
		}
		result = append(result, parsed...)
		pendingStart, pendingStop = -1, -1
		return nil
	}

	err := ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || node.Kind() != ast.KindText || insideCode(node) {
			return ast.WalkContinue, nil
		}
		segment := node.(*ast.Text).Segment
		if pendingStop == segment.Start {
			pendingStop = segment.Stop
			return ast.WalkContinue, nil
		}
		if err := flush(); err != nil {
			return ast.WalkStop, err
		}
		pendingStart, pendingStop = segment.Start, segment.Stop
		return ast.WalkContinue, nil
	})
	if err != nil {
		return nil, err
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return result, nil
}

func insideCode(node ast.Node) bool {
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		switch parent.Kind() {
		case ast.KindCodeSpan, ast.KindCodeBlock, ast.KindFencedCodeBlock:
			return true
		}
	}
	return false
}

func parseTextSegment(source []byte, start, stop int, strict bool) ([]ParsedReference, error) {
	result := make([]ParsedReference, 0)
	for cursor := start; cursor < stop; {
		current, width := utf8.DecodeRune(source[cursor:stop])
		if current != '@' || !validStartBoundary(source, cursor) {
			cursor += width
			continue
		}

		nameStart := cursor + width
		nameEnd := nameStart
		for nameEnd < stop {
			current, currentWidth := utf8.DecodeRune(source[nameEnd:stop])
			if !isUsernameRune(current) {
				break
			}
			nameEnd += currentWidth
		}
		if nameEnd == nameStart {
			cursor += width
			continue
		}

		name := string(source[nameStart:nameEnd])
		if nameEnd < stop && source[nameEnd] == ':' {
			parsed, next, handled, err := parseResource(source, cursor, nameEnd, stop, name)
			if err != nil {
				if strict {
					return nil, err
				}
				cursor = nameEnd + 1
				continue
			}
			if handled {
				if parsed != nil {
					result = append(result, *parsed)
				}
				cursor = next
				continue
			}
		}

		result = append(result, ParsedReference{
			Kind:       KindUser,
			TargetType: TargetTypeUser,
			Identifier: name,
			Start:      utf8.RuneCount(source[:cursor]),
			End:        utf8.RuneCount(source[:nameEnd]),
		})
		cursor = nameEnd
	}
	return result, nil
}

func parseResource(source []byte, tokenStart, colon, stop int, targetType string) (*ParsedReference, int, bool, error) {
	rest := source[colon+1 : stop]
	match := canonicalUUIDPattern.FindIndex(rest)
	if !IsSupportedResourceType(targetType) {
		if match != nil && match[0] == 0 {
			return nil, 0, true, fmt.Errorf("%w: unsupported resource type %q", ErrInvalidSyntax, targetType)
		}
		return nil, 0, false, nil
	}
	if match == nil || match[0] != 0 {
		return nil, 0, true, fmt.Errorf("%w: %s reference requires a canonical UUID", ErrInvalidSyntax, targetType)
	}

	uuidEnd := colon + 1 + match[1]
	if stanceEnd, ok := debateDirectionEnd(source, targetType, uuidEnd, stop); ok {
		return nil, stanceEnd, true, nil
	}
	if uuidEnd < stop {
		next, _ := utf8.DecodeRune(source[uuidEnd:stop])
		if isUsernameRune(next) || next == ':' {
			return nil, 0, true, fmt.Errorf("%w: resource token has trailing characters", ErrInvalidSyntax)
		}
	}

	return &ParsedReference{
		Kind:       KindResource,
		TargetType: targetType,
		Identifier: string(source[colon+1 : uuidEnd]),
		Start:      utf8.RuneCount(source[:tokenStart]),
		End:        utf8.RuneCount(source[:uuidEnd]),
	}, uuidEnd, true, nil
}

func debateDirectionEnd(source []byte, targetType string, uuidEnd, stop int) (int, bool) {
	if targetType != "debate" {
		return 0, false
	}
	for _, suffix := range []string{":support", ":oppose"} {
		end := uuidEnd + len(suffix)
		if end <= stop && string(source[uuidEnd:end]) == suffix && validEndBoundary(source, end, stop) {
			return end, true
		}
	}
	return 0, false
}

func validStartBoundary(source []byte, offset int) bool {
	if offset == 0 {
		return true
	}
	previous, _ := utf8.DecodeLastRune(source[:offset])
	return !isUsernameRune(previous) && previous != '@'
}

func validEndBoundary(source []byte, offset, stop int) bool {
	if offset >= stop {
		return true
	}
	next, _ := utf8.DecodeRune(source[offset:stop])
	return !isUsernameRune(next) && next != ':'
}

func isUsernameRune(current rune) bool {
	return unicode.IsLetter(current) || unicode.IsNumber(current) || current == '_' || current == '-'
}
