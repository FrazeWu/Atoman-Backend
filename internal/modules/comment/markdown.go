package comment

import (
	"bytes"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"golang.org/x/text/unicode/norm"
)

var (
	errUnsupportedMarkdown = errors.New("comment contains unsupported markdown")
	tableDelimiterPattern  = regexp.MustCompile(`(?m)^\s*\|?\s*:?-+:?\s*(?:\|\s*:?-+:?\s*)+\|?\s*$`)
)

func NormalizeContent(raw string) string {
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	return strings.TrimSpace(norm.NFC.String(normalized))
}

func RenderCommentMarkdown(raw string) (string, error) {
	content := NormalizeContent(raw)
	markdown := goldmark.New(goldmark.WithRendererOptions(
		html.WithHardWraps(),
		renderer.WithNodeRenderers(util.Prioritized(externalLinkRenderer{}, 500)),
	))
	source := []byte(content)
	document := markdown.Parser().Parse(text.NewReader(source))
	plainSource := sourceOutsideCodeSpans(source, document)
	if containsStrikethroughSyntax(string(plainSource)) || tableDelimiterPattern.Match(plainSource) {
		return "", errUnsupportedMarkdown
	}
	if err := validateMarkdownNodes(document); err != nil {
		return "", err
	}

	var output bytes.Buffer
	if err := markdown.Renderer().Render(&output, source, document); err != nil {
		return "", err
	}
	return output.String(), nil
}

type externalLinkRenderer struct{}

func (externalLinkRenderer) RegisterFuncs(register renderer.NodeRendererFuncRegisterer) {
	register.Register(ast.KindLink, renderExternalLink)
}

func renderExternalLink(writer util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	link := node.(*ast.Link)
	if !entering {
		_, _ = writer.WriteString("</a>")
		return ast.WalkContinue, nil
	}
	_, _ = writer.WriteString(`<a href="`)
	_, _ = writer.Write(util.EscapeHTML(util.URLEscape(link.Destination, true)))
	_, _ = writer.WriteString(`" rel="nofollow noreferrer noopener"`)
	if link.Title != nil {
		_, _ = writer.WriteString(` title="`)
		_, _ = writer.Write(util.EscapeHTML(link.Title))
		_ = writer.WriteByte('"')
	}
	_ = writer.WriteByte('>')
	return ast.WalkContinue, nil
}

func containsStrikethroughSyntax(content string) bool {
	for i := 0; i < len(content); {
		if content[i] == '\\' {
			i += 2
			continue
		}
		if i+1 < len(content) && content[i:i+2] == "~~" {
			return true
		}
		i++
	}
	return false
}

func sourceOutsideCodeSpans(source []byte, document ast.Node) []byte {
	plainSource := append([]byte(nil), source...)
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || node.Kind() != ast.KindCodeSpan {
			return ast.WalkContinue, nil
		}
		for child := node.FirstChild(); child != nil; child = child.NextSibling() {
			textNode, ok := child.(*ast.Text)
			if !ok {
				continue
			}
			for i := textNode.Segment.Start; i < textNode.Segment.Stop; i++ {
				if plainSource[i] != '\n' {
					plainSource[i] = ' '
				}
			}
		}
		return ast.WalkSkipChildren, nil
	})
	return plainSource
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
