package mail

import (
	"bytes"
	xhtml "golang.org/x/net/html"
	"html"
	"net/url"
	"strings"
)

// Deliberately small formatting subset. No scripts, styles, forms, remote
// images, event handlers, embeds or automatic network requests survive.
func sanitizeMailHTML(raw string) string {
	if raw == "" {
		return ""
	}
	doc, e := xhtml.Parse(strings.NewReader(raw))
	if e != nil {
		return ""
	}
	var b bytes.Buffer
	allowed := map[string]bool{"p": true, "br": true, "b": true, "strong": true, "i": true, "em": true, "u": true, "s": true, "blockquote": true, "pre": true, "code": true, "ul": true, "ol": true, "li": true, "a": true, "div": true, "span": true, "table": true, "tbody": true, "tr": true, "td": true, "th": true, "h1": true, "h2": true, "h3": true, "hr": true}
	blocked := map[string]bool{"script": true, "style": true, "iframe": true, "object": true, "embed": true, "svg": true, "math": true, "form": true, "input": true, "button": true, "textarea": true, "head": true, "template": true}
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.TextNode {
			b.WriteString(html.EscapeString(n.Data))
			return
		}
		if n.Type == xhtml.ElementNode && blocked[n.Data] {
			return
		}
		tag := n.Type == xhtml.ElementNode && allowed[n.Data]
		if tag {
			b.WriteByte('<')
			b.WriteString(n.Data)
			if n.Data == "a" {
				for _, a := range n.Attr {
					if a.Key == "href" {
						v, e := url.Parse(strings.TrimSpace(a.Val))
						if e == nil && (v.Scheme == "https" || v.Scheme == "http" || v.Scheme == "mailto") {
							b.WriteString(" href=\"")
							b.WriteString(html.EscapeString(v.String()))
							b.WriteString("\" rel=\"noopener noreferrer\" target=\"_blank\"")
						}
					}
				}
			}
			b.WriteByte('>')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if tag && n.Data != "br" && n.Data != "hr" {
			b.WriteString("</" + n.Data + ">")
		}
	}
	walk(doc)
	return b.String()
}
func mailHTMLText(raw string) string {
	doc, e := xhtml.Parse(strings.NewReader(raw))
	if e != nil {
		return ""
	}
	var b strings.Builder
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == xhtml.ElementNode && (n.Data == "p" || n.Data == "br" || n.Data == "div" || n.Data == "li") {
			b.WriteByte('\n')
		}
	}
	walk(doc)
	return strings.TrimSpace(b.String())
}
