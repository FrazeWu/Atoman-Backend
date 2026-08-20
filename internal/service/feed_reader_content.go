package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	ReaderVersionCurrent        = 2
	ReaderSourceSummary         = "summary"
	ReaderSourceFeed            = "feed"
	ReaderSourcePage            = "page"
	ReaderQualityReadyThreshold = 60
)

type ReaderCandidate struct {
	HTML           string
	Source         string
	QualityScore   int
	QualityFlags   []string
	CharacterCount int
	ContentHash    string
	Extractor      string
}

func SanitizeFeedContent(sourceURL, rawHTML string) (ReaderCandidate, error) {
	candidate, err := sanitizeReaderFragment(sourceURL, rawHTML)
	if err != nil {
		return ReaderCandidate{}, err
	}
	candidate.Source = ReaderSourceFeed
	candidate.Extractor = "feed"
	return candidate, nil
}

func sanitizeReaderFragment(sourceURL, rawHTML string) (ReaderCandidate, error) {
	if strings.TrimSpace(rawHTML) == "" {
		return ReaderCandidate{}, errors.New("reader content is empty")
	}

	context := &html.Node{Type: html.ElementNode, DataAtom: atom.Div, Data: "div"}
	nodes, err := html.ParseFragment(strings.NewReader(rawHTML), context)
	if err != nil {
		return ReaderCandidate{}, err
	}
	for _, node := range nodes {
		context.AppendChild(node)
	}

	normalizeFullTextNode(sourceURL, context)
	cleanHTML, text, err := sanitizeFullTextHTML(context)
	if err != nil {
		return ReaderCandidate{}, err
	}
	if cleanHTML == "" || text == "" {
		return ReaderCandidate{}, errors.New("sanitized reader content is empty")
	}

	score, flags := scoreReaderContent(context, text)
	return ReaderCandidate{
		HTML:           cleanHTML,
		QualityScore:   score,
		QualityFlags:   flags,
		CharacterCount: utf8.RuneCountInString(text),
		ContentHash:    hashReaderContent(cleanHTML),
	}, nil
}

func scoreReaderContent(root *html.Node, text string) (int, []string) {
	characterCount := utf8.RuneCountInString(text)
	paragraphCount := countElements(root, atom.P)
	structureCount := countElements(root, atom.H1) + countElements(root, atom.H2) + countElements(root, atom.H3) +
		countElements(root, atom.Ul) + countElements(root, atom.Ol) + countElements(root, atom.Blockquote) +
		countElements(root, atom.Pre) + countElements(root, atom.Table) + countElements(root, atom.Figure)
	linkCharacters := utf8.RuneCountInString(compactFullTextText(nodeTextByAtom(root, atom.A)))

	score := 20
	flags := make([]string, 0, 5)
	switch {
	case characterCount >= 1500:
		score += 40
	case characterCount >= 800:
		score += 35
	case characterCount >= 280:
		score += 25
	case characterCount >= fullTextMinimumCharacters:
		score += 10
	default:
		flags = append(flags, "too_short")
	}

	switch {
	case paragraphCount >= 5:
		score += 20
	case paragraphCount >= 2:
		score += 15
	case paragraphCount == 1:
		score += 5
	case characterCount >= 500:
		score -= 10
		flags = append(flags, "low_structure")
	}
	if structureCount > 0 {
		score += min(structureCount*2, 10)
	}
	if characterCount >= 500 && strings.Count(text, ".")+strings.Count(text, "。") >= 3 {
		score += 10
	}

	if characterCount > 0 {
		linkDensity := float64(linkCharacters) / float64(characterCount)
		switch {
		case linkDensity > 0.5:
			score -= 35
			flags = append(flags, "high_link_density")
		case linkDensity > 0.3:
			score -= 15
			flags = append(flags, "elevated_link_density")
		}
	}
	if characterCount > 50000 {
		score -= 30
		flags = append(flags, "extreme_length")
	} else if characterCount > 20000 {
		score -= 15
		flags = append(flags, "very_long")
	}

	normalizedText := strings.ToLower(compactFullTextText(text))
	boilerplateHits := 0
	for _, phrase := range []string{"cookie policy", "accept cookies", "related articles", "share this article", "all rights reserved", "打开app", "打开 app"} {
		if strings.Contains(normalizedText, phrase) {
			boilerplateHits++
		}
	}
	if boilerplateHits >= 2 {
		score -= 15
		flags = append(flags, "possible_boilerplate")
	}
	if looksLikeLoginWallText(normalizedText) {
		score = min(score, 20)
		flags = append(flags, "login_wall")
	}
	if characterCount >= 280 && (strings.HasSuffix(normalizedText, "...") || strings.HasSuffix(normalizedText, "……") || strings.HasSuffix(normalizedText, "阅读全文")) {
		score -= 10
		flags = append(flags, "possible_truncation")
	}

	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	if score < ReaderQualityReadyThreshold && len(flags) == 0 {
		flags = append(flags, "low_quality")
	}
	return score, flags
}

func ChooseReaderCandidate(feedCandidate, pageCandidate ReaderCandidate) ReaderCandidate {
	if feedCandidate.HTML == "" {
		return pageCandidate
	}
	if pageCandidate.HTML == "" {
		return feedCandidate
	}
	if pageCandidate.QualityScore > feedCandidate.QualityScore+5 {
		return pageCandidate
	}
	return feedCandidate
}

func ReaderQualityFlagsJSON(flags []string) json.RawMessage {
	if len(flags) == 0 {
		return json.RawMessage("[]")
	}
	encoded, _ := json.Marshal(flags)
	return encoded
}

func hashReaderContent(content string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(content)))
	return hex.EncodeToString(digest[:])
}

func resolveReaderURL(baseURL *url.URL, raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	if parsed.Scheme == "" && baseURL != nil {
		parsed = baseURL.ResolveReference(parsed)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	return parsed.String()
}
