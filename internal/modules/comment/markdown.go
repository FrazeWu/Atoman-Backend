package comment

import (
	"bytes"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

var (
	errUnsupportedMarkdown = errors.New("comment contains unsupported markdown")
	tableDelimiterPattern  = regexp.MustCompile(`(?m)^\s*\|?\s*:?-+:?\s*(?:\|\s*:?-+:?\s*)+\|?\s*$`)
)

func NormalizeContent(raw string) string {
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	return strings.TrimSpace(normalized)
}

func RenderCommentMarkdown(raw string) (string, error) {
	content := NormalizeContent(raw)
	if containsStrikethroughSyntax(content) || tableDelimiterPattern.MatchString(content) {
		return "", errUnsupportedMarkdown
	}

	markdown := goldmark.New(goldmark.WithRendererOptions(html.WithHardWraps()))
	source := []byte(content)
	document := markdown.Parser().Parse(text.NewReader(source))
	if err := validateMarkdownNodes(document); err != nil {
		return "", err
	}

	var output bytes.Buffer
	if err := markdown.Renderer().Render(&output, source, document); err != nil {
		return "", err
	}
	return output.String(), nil
}

func containsStrikethroughSyntax(content string) bool {
	codeDelimiterLength := 0
	for i := 0; i < len(content); {
		if content[i] == '\\' && codeDelimiterLength == 0 {
			i += 2
			continue
		}
		if content[i] == '`' {
			runLength := 1
			for i+runLength < len(content) && content[i+runLength] == '`' {
				runLength++
			}
			if codeDelimiterLength == 0 {
				codeDelimiterLength = runLength
			} else if codeDelimiterLength == runLength {
				codeDelimiterLength = 0
			}
			i += runLength
			continue
		}
		if codeDelimiterLength == 0 && i+1 < len(content) && content[i:i+2] == "~~" {
			return true
		}
		i++
	}
	return false
}

func validateMarkdownNodes(document ast.Node) error {
	return ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		switch node.Kind() {
		case ast.KindDocument,
			ast.KindParagraph,
			ast.KindText,
			ast.KindString,
			ast.KindEmphasis,
			ast.KindCodeSpan,
			ast.KindBlockquote:
			return ast.WalkContinue, nil
		case ast.KindLink:
			link := node.(*ast.Link)
			if !isAllowedLink(link.Destination) {
				return ast.WalkStop, errUnsupportedMarkdown
			}
			return ast.WalkContinue, nil
		default:
			return ast.WalkStop, errUnsupportedMarkdown
		}
	})
}

func isAllowedLink(destination []byte) bool {
	parsed, err := url.Parse(string(destination))
	if err != nil || parsed.Host == "" {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}
