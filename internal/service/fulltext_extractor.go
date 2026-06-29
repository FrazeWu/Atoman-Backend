package service

import (
	"bytes"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const fullTextMinimumCharacters = 120

type FullTextResult struct {
	HTML      string
	WordCount int
}

func ExtractAndSanitizeFullText(sourceURL string, body io.Reader) (FullTextResult, string, error) {
	doc, err := html.Parse(body)
	if err != nil {
		return FullTextResult{}, FullTextErrorRequestFailed, err
	}

	candidate := pickFullTextContentNode(doc)
	if candidate == nil {
		return FullTextResult{}, FullTextErrorSanitizeEmpty, errors.New("full text candidate not found")
	}
	if candidate.DataAtom == atom.Body {
		if fallback := findBestFullTextChild(candidate); fallback != nil {
			candidate = fallback
		}
	}

	normalizeFullTextNode(sourceURL, candidate)
	cleanHTML, text, err := sanitizeFullTextHTML(candidate)
	if err != nil {
		return FullTextResult{}, FullTextErrorSanitizeEmpty, err
	}
	if cleanHTML == "" {
		return FullTextResult{}, FullTextErrorSanitizeEmpty, errors.New("sanitized html empty")
	}
	if looksLikeLoginWallText(text) {
		return FullTextResult{}, FullTextErrorLoginWallDetected, errors.New("login or app wall detected")
	}
	if utf8.RuneCountInString(text) < fullTextMinimumCharacters {
		return FullTextResult{}, FullTextErrorExtractTooShort, errors.New("content too short")
	}

	return FullTextResult{
		HTML:      cleanHTML,
		WordCount: utf8.RuneCountInString(text),
	}, "", nil
}

func looksLikeLoginWallText(text string) bool {
	normalized := strings.ToLower(compactFullTextText(text))
	if normalized == "" {
		return false
	}

	score := 0
	for _, phrase := range []string{
		"登录后继续阅读",
		"登录后查看",
		"立即登录",
		"注册账号",
		"验证码登录",
		"微信扫码登录",
		"打开 app",
		"打开app",
		"下载客户端",
		"阅读全文",
		"已复制链接",
		"continue reading",
		"sign in",
		"log in",
	} {
		if strings.Contains(normalized, phrase) {
			score++
		}
	}
	if score >= 2 {
		return true
	}

	loginCount := strings.Count(normalized, "登录") + strings.Count(normalized, "login")
	appCount := strings.Count(normalized, "app") + strings.Count(normalized, "客户端")
	return loginCount >= 3 && appCount >= 1
}

func pickFullTextContentNode(doc *html.Node) *html.Node {
	preferredMatchers := []func(*html.Node) bool{
		func(n *html.Node) bool { return n.DataAtom == atom.Article },
		func(n *html.Node) bool { return n.DataAtom == atom.Main },
		func(n *html.Node) bool { return attrEquals(n, "role", "main") },
		func(n *html.Node) bool { return hasClass(n, "post-content") },
		func(n *html.Node) bool { return hasClass(n, "entry-content") },
		func(n *html.Node) bool { return hasClass(n, "article-content") },
		func(n *html.Node) bool { return hasClass(n, "content") },
	}

	for _, matcher := range preferredMatchers {
		if node := findFirstNode(doc, matcher); isUsableFullTextNode(node) {
			return node
		}
	}

	var best *html.Node
	bestScore := -1
	walkNodes(doc, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}
		switch node.DataAtom {
		case atom.Body, atom.Article, atom.Main, atom.Section, atom.Div:
		default:
			return
		}
		if isBlockedFullTextNode(node) {
			return
		}
		score := scoreFullTextNode(node)
		if score > bestScore {
			best = node
			bestScore = score
		}
	})

	if best != nil {
		return best
	}

	return findFirstNode(doc, func(n *html.Node) bool { return n.DataAtom == atom.Body })
}

func isUsableFullTextNode(node *html.Node) bool {
	if node == nil || isBlockedFullTextNode(node) {
		return false
	}
	return utf8.RuneCountInString(compactFullTextText(nodeText(node))) >= 60
}

func isBlockedFullTextNode(node *html.Node) bool {
	if node == nil {
		return true
	}
	switch node.DataAtom {
	case atom.Nav, atom.Aside, atom.Footer, atom.Header, atom.Form, atom.Script, atom.Style, atom.Noscript:
		return true
	}

	if hasAnyFingerprintToken(node, "nav", "menu", "sidebar", "comment", "comments", "footer", "header", "share", "social", "ad", "ads", "promo", "breadcrumb", "pagination") {
		return true
	}
	return hasAnyFingerprintPhrase(node, "related-stories", "related-posts", "related-articles")
}

func scoreFullTextNode(node *html.Node) int {
	text := compactFullTextText(nodeText(node))
	textLen := utf8.RuneCountInString(text)
	if textLen == 0 {
		return -1
	}

	linkLen := utf8.RuneCountInString(compactFullTextText(nodeTextByAtom(node, atom.A)))
	paragraphs := countElements(node, atom.P)
	lists := countElements(node, atom.Ul) + countElements(node, atom.Ol)
	headings := countElements(node, atom.H1) + countElements(node, atom.H2) + countElements(node, atom.H3)
	images := countElements(node, atom.Img)
	penalty := 0
	if linkLen > textLen/2 {
		penalty += linkLen
	}

	return textLen + paragraphs*80 + lists*30 + headings*40 + images*10 - penalty
}

func findBestFullTextChild(node *html.Node) *html.Node {
	var best *html.Node
	bestScore := -1
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode {
			continue
		}
		if isBlockedFullTextNode(child) {
			continue
		}
		score := scoreFullTextNode(child)
		if score > bestScore {
			best = child
			bestScore = score
		}
	}
	if best != nil {
		return best
	}
	return nil
}

func normalizeFullTextNode(sourceURL string, node *html.Node) {
	baseURL, err := url.Parse(sourceURL)
	if err != nil {
		baseURL = nil
	}

	removeDisallowedSubtrees(node)
	walkNodes(node, func(current *html.Node) {
		if current.Type != html.ElementNode {
			return
		}
		cleanNodeAttributes(current)
		switch current.DataAtom {
		case atom.A:
			normalizeAnchor(baseURL, current)
		case atom.Img:
			normalizeImage(baseURL, current)
		}
	})
}

func normalizeAnchor(baseURL *url.URL, node *html.Node) {
	href := strings.TrimSpace(attrValue(node, "href"))
	if href == "" {
		removeAttr(node, "href")
		return
	}
	if isSafeRelativeURL(href) {
		return
	}
	parsed, err := url.Parse(href)
	if err != nil {
		removeAttr(node, "href")
		return
	}
	if parsed.Scheme == "" && baseURL != nil {
		setAttr(node, "href", baseURL.ResolveReference(parsed).String())
		return
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		removeAttr(node, "href")
	}
}

func normalizeImage(baseURL *url.URL, node *html.Node) {
	src := strings.TrimSpace(attrValue(node, "src"))
	if src == "" {
		detachNode(node)
		return
	}
	parsed, err := url.Parse(src)
	if err != nil {
		detachNode(node)
		return
	}
	if parsed.Scheme == "" {
		if baseURL == nil {
			detachNode(node)
			return
		}
		setAttr(node, "src", baseURL.ResolveReference(parsed).String())
		return
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		detachNode(node)
		return
	}

	if isLikelyDecorativeImage(node) {
		detachNode(node)
	}
}

func isLikelyDecorativeImage(node *html.Node) bool {
	src := strings.ToLower(strings.TrimSpace(attrValue(node, "src")))
	alt := strings.ToLower(strings.TrimSpace(attrValue(node, "alt")))
	title := strings.ToLower(strings.TrimSpace(attrValue(node, "title")))
	className := strings.ToLower(strings.TrimSpace(attrValue(node, "class")))

	if src == "" {
		return true
	}
	if strings.Contains(src, "favicon") || strings.Contains(src, "avatar") || strings.Contains(src, "emoji") || strings.Contains(src, "icon") {
		return true
	}
	if strings.Contains(alt, "头像") || strings.Contains(alt, "emoji") || strings.Contains(alt, "icon") {
		return true
	}
	if strings.Contains(title, "头像") || strings.Contains(title, "emoji") || strings.Contains(title, "icon") {
		return true
	}
	if strings.Contains(className, "avatar") || strings.Contains(className, "emoji") || strings.Contains(className, "icon") || strings.Contains(className, "logo") {
		return true
	}
	if strings.HasPrefix(src, "data:image/") && len(src) < 4096 {
		return true
	}

	width, hasWidth := parsePositiveIntAttr(node, "width")
	height, hasHeight := parsePositiveIntAttr(node, "height")
	if hasWidth && width <= 64 {
		return true
	}
	if hasHeight && height <= 64 {
		return true
	}
	return false
}

func parsePositiveIntAttr(node *html.Node, key string) (int, bool) {
	raw := strings.TrimSpace(attrValue(node, key))
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

func sanitizeFullTextHTML(node *html.Node) (string, string, error) {
	var buf bytes.Buffer
	var textParts []string
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		cleanedNodes := cloneSanitizedNode(child)
		if len(cleanedNodes) == 0 {
			continue
		}
		for _, cleaned := range cleanedNodes {
			text := compactFullTextText(nodeText(cleaned))
			if text != "" {
				textParts = append(textParts, text)
			}
			if err := html.Render(&buf, cleaned); err != nil {
				return "", "", err
			}
		}
	}

	return strings.TrimSpace(buf.String()), compactFullTextText(strings.Join(textParts, " ")), nil
}

func compactFullTextText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func isSafeRelativeURL(raw string) bool {
	return strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../") || strings.HasPrefix(raw, "#")
}

func findFirstNode(root *html.Node, matcher func(*html.Node) bool) *html.Node {
	var found *html.Node
	walkNodes(root, func(node *html.Node) {
		if found == nil && matcher(node) {
			found = node
		}
	})
	return found
}

func walkNodes(node *html.Node, visit func(*html.Node)) {
	if node == nil {
		return
	}
	visit(node)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walkNodes(child, visit)
	}
}

func nodeText(node *html.Node) string {
	if node == nil {
		return ""
	}
	var parts []string
	walkNodes(node, func(current *html.Node) {
		if current.Type == html.TextNode {
			parts = append(parts, current.Data)
		}
	})
	return strings.Join(parts, " ")
}

func nodeTextByAtom(node *html.Node, target atom.Atom) string {
	var parts []string
	walkNodes(node, func(current *html.Node) {
		if current.Type == html.ElementNode && current.DataAtom == target {
			parts = append(parts, nodeText(current))
		}
	})
	return strings.Join(parts, " ")
}

func countElements(node *html.Node, target atom.Atom) int {
	count := 0
	walkNodes(node, func(current *html.Node) {
		if current.Type == html.ElementNode && current.DataAtom == target {
			count++
		}
	})
	return count
}

func hasClass(node *html.Node, className string) bool {
	classes := strings.Fields(attrValue(node, "class"))
	for _, class := range classes {
		if class == className {
			return true
		}
	}
	return false
}

func attrEquals(node *html.Node, key, want string) bool {
	return strings.EqualFold(strings.TrimSpace(attrValue(node, key)), want)
}

func attrValue(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

func hasAttr(node *html.Node, key string) bool {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return true
		}
	}
	return false
}

func setAttr(node *html.Node, key, value string) {
	for i := range node.Attr {
		if strings.EqualFold(node.Attr[i].Key, key) {
			node.Attr[i].Val = value
			return
		}
	}
	node.Attr = append(node.Attr, html.Attribute{Key: key, Val: value})
}

func removeAttr(node *html.Node, key string) {
	filtered := node.Attr[:0]
	for _, attr := range node.Attr {
		if !strings.EqualFold(attr.Key, key) {
			filtered = append(filtered, attr)
		}
	}
	node.Attr = filtered
}

func cleanNodeAttributes(node *html.Node) {
	filtered := node.Attr[:0]
	for _, attr := range node.Attr {
		key := strings.ToLower(attr.Key)
		if strings.HasPrefix(key, "on") || key == "style" || key == "srcset" {
			continue
		}
		filtered = append(filtered, attr)
	}
	node.Attr = filtered
}

func isHiddenOrDecorativeFullTextNode(node *html.Node) bool {
	if node == nil || node.Type != html.ElementNode {
		return false
	}
	switch node.DataAtom {
	case atom.Svg, atom.Canvas, atom.Button:
		return true
	}
	if hasAttr(node, "hidden") || attrEquals(node, "aria-hidden", "true") {
		return true
	}
	if attrEquals(node, "role", "button") {
		return true
	}
	if hasAnyFingerprintToken(node, "icon", "iconfont", "glyph", "symbol", "avatar", "logo") {
		return true
	}
	if hasAnyFingerprintPhrase(node, "sr-only", "screen-reader-text", "visually-hidden", "iconfont") {
		return true
	}
	return false
}

func removeDisallowedSubtrees(node *html.Node) {
	for child := node.FirstChild; child != nil; {
		next := child.NextSibling
		if child.Type == html.ElementNode {
			switch child.DataAtom {
			case atom.Script, atom.Style, atom.Iframe, atom.Object, atom.Embed, atom.Form, atom.Nav, atom.Aside, atom.Footer, atom.Header, atom.Noscript, atom.Svg, atom.Canvas, atom.Button:
				detachNode(child)
				child = next
				continue
			}
			if isHiddenOrDecorativeFullTextNode(child) || isObviousNonContentSubtree(child) {
				detachNode(child)
				child = next
				continue
			}
		}
		removeDisallowedSubtrees(child)
		child = next
	}
}

func isObviousNonContentSubtree(node *html.Node) bool {
	if node == nil || node.Type != html.ElementNode {
		return false
	}

	if hasAnyFingerprintPhrase(node, "comment-list", "share-buttons", "related-stories", "related-posts", "related-articles") {
		return true
	}
	if hasAnyFingerprintToken(node, "comments", "replies", "share", "social", "sponsored", "recommendations", "ad", "ads") {
		return true
	}
	return false
}

func hasFingerprintToken(fingerprint, token string) bool {
	for _, field := range strings.FieldsFunc(fingerprint, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z':
			return false
		case r >= '0' && r <= '9':
			return false
		default:
			return true
		}
	}) {
		if field == token {
			return true
		}
	}
	return false
}

func hasAnyFingerprintToken(node *html.Node, tokens ...string) bool {
	for _, fingerprint := range fingerprintCandidates(node) {
		for _, token := range tokens {
			if hasFingerprintToken(fingerprint, token) {
				return true
			}
		}
	}
	return false
}

func hasAnyFingerprintPhrase(node *html.Node, phrases ...string) bool {
	for _, fingerprint := range fingerprintCandidates(node) {
		for _, phrase := range phrases {
			if strings.Contains(fingerprint, phrase) {
				return true
			}
		}
	}
	return false
}

func fingerprintCandidates(node *html.Node) []string {
	if node == nil {
		return nil
	}
	return []string{
		strings.ToLower(node.Data),
		strings.ToLower(attrValue(node, "class")),
		strings.ToLower(attrValue(node, "id")),
		strings.ToLower(attrValue(node, "data-testid")),
		strings.ToLower(attrValue(node, "data-component")),
		strings.ToLower(attrValue(node, "aria-label")),
	}
}

func detachNode(node *html.Node) {
	if node == nil || node.Parent == nil {
		return
	}
	parent := node.Parent
	if parent.FirstChild == node {
		parent.FirstChild = node.NextSibling
	}
	if parent.LastChild == node {
		parent.LastChild = node.PrevSibling
	}
	if node.PrevSibling != nil {
		node.PrevSibling.NextSibling = node.NextSibling
	}
	if node.NextSibling != nil {
		node.NextSibling.PrevSibling = node.PrevSibling
	}
	node.Parent = nil
	node.PrevSibling = nil
	node.NextSibling = nil
}

func cloneSanitizedNode(node *html.Node) []*html.Node {
	if node == nil {
		return nil
	}
	if node.Type == html.TextNode {
		text := compactFullTextText(node.Data)
		if text == "" {
			return nil
		}
		return []*html.Node{{Type: html.TextNode, Data: text}}
	}
	if node.Type != html.ElementNode {
		return nil
	}
	if isHiddenOrDecorativeFullTextNode(node) {
		return nil
	}
	if !allowedFullTextElements[node.DataAtom] {
		return cloneSanitizedChildren(node)
	}

	clone := &html.Node{Type: html.ElementNode, DataAtom: node.DataAtom, Data: node.Data}
	for _, attr := range node.Attr {
		if isAllowedFullTextAttr(node.DataAtom, attr) {
			clone.Attr = append(clone.Attr, attr)
		}
	}
	if node.DataAtom == atom.A {
		ensureExternalLinkAttrs(clone)
	}

	children := sanitizedChildrenWithSpacing(node)
	appendSanitizedChildren(clone, children)
	if clone.DataAtom == atom.Img {
		if attrValue(clone, "src") == "" {
			return nil
		}
		return []*html.Node{clone}
	}
	if clone.FirstChild == nil && !voidFullTextElements[clone.DataAtom] {
		return nil
	}
	return []*html.Node{clone}
}

func cloneSanitizedChildren(node *html.Node) []*html.Node {
	return sanitizedChildrenWithSpacing(node)
}

func sanitizedChildrenWithSpacing(node *html.Node) []*html.Node {
	var nodes []*html.Node
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		cloned := cloneSanitizedNode(child)
		if len(cloned) == 0 {
			continue
		}
		if needsSpaceBetweenSanitizedNodes(nodes, cloned) {
			nodes = append(nodes, &html.Node{Type: html.TextNode, Data: " "})
		}
		nodes = append(nodes, cloned...)
	}
	return nodes
}

func needsSpaceBetweenSanitizedNodes(existing, incoming []*html.Node) bool {
	if len(existing) == 0 || len(incoming) == 0 {
		return false
	}
	left := lastMeaningfulSanitizedNode(existing)
	right := firstMeaningfulSanitizedNode(incoming)
	if left == nil || right == nil {
		return false
	}
	return nodeEndsWithWordChar(left) && nodeStartsWithWordChar(right)
}

func lastMeaningfulSanitizedNode(nodes []*html.Node) *html.Node {
	for i := len(nodes) - 1; i >= 0; i-- {
		if node := nodes[i]; node != nil && compactFullTextText(nodeText(node)) != "" {
			return node
		}
	}
	return nil
}

func firstMeaningfulSanitizedNode(nodes []*html.Node) *html.Node {
	for _, node := range nodes {
		if node != nil && compactFullTextText(nodeText(node)) != "" {
			return node
		}
	}
	return nil
}

func nodeEndsWithWordChar(node *html.Node) bool {
	text := compactFullTextText(nodeText(node))
	if text == "" {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(text)
	return isInlineWordRune(r)
}

func nodeStartsWithWordChar(node *html.Node) bool {
	text := compactFullTextText(nodeText(node))
	if text == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(text)
	return isInlineWordRune(r)
}

func isInlineWordRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
}

func appendSanitizedChildren(parent *html.Node, children []*html.Node) {
	if parent == nil || len(children) == 0 {
		return
	}
	for _, child := range children {
		if child == nil {
			continue
		}
		child.Parent = parent
		child.PrevSibling = nil
		child.NextSibling = nil
		if parent.FirstChild == nil {
			parent.FirstChild = child
			parent.LastChild = child
			continue
		}
		child.PrevSibling = parent.LastChild
		parent.LastChild.NextSibling = child
		parent.LastChild = child
	}
}

func ensureExternalLinkAttrs(node *html.Node) {
	href := attrValue(node, "href")
	parsed, err := url.Parse(href)
	if err != nil || parsed.Scheme == "" {
		return
	}
	setAttr(node, "rel", "noopener noreferrer")
	removeAttr(node, "target")
}

func isAllowedFullTextAttr(tag atom.Atom, attr html.Attribute) bool {
	key := strings.ToLower(attr.Key)
	switch tag {
	case atom.A:
		return key == "href" || key == "rel" || key == "target"
	case atom.Img:
		return key == "src" || key == "alt" || key == "title" || key == "width" || key == "height"
	default:
		return false
	}
}

var allowedFullTextElements = map[atom.Atom]bool{
	atom.H1:         true,
	atom.H2:         true,
	atom.H3:         true,
	atom.H4:         true,
	atom.H5:         true,
	atom.H6:         true,
	atom.P:          true,
	atom.Br:         true,
	atom.Strong:     true,
	atom.Em:         true,
	atom.B:          true,
	atom.I:          true,
	atom.U:          true,
	atom.Blockquote: true,
	atom.Ul:         true,
	atom.Ol:         true,
	atom.Li:         true,
	atom.Pre:        true,
	atom.Code:       true,
	atom.A:          true,
	atom.Img:        true,
	atom.Figure:     true,
	atom.Figcaption: true,
	atom.Table:      true,
	atom.Thead:      true,
	atom.Tbody:      true,
	atom.Tr:         true,
	atom.Th:         true,
	atom.Td:         true,
}

var voidFullTextElements = map[atom.Atom]bool{
	atom.Br:  true,
	atom.Img: true,
}
