package service

import (
	"encoding/json"
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// FeedImageMetadata contains image candidates discovered from an article page.
// ImageURL is suitable for an article cover; IconURL is the last-resort site mark.
type FeedImageMetadata struct {
	ImageURL string
	IconURL  string
}

func ExtractFeedImageMetadata(sourceURL string, body io.Reader) (FeedImageMetadata, error) {
	doc, err := html.Parse(body)
	if err != nil {
		return FeedImageMetadata{}, err
	}

	baseURL, err := url.Parse(sourceURL)
	if err != nil {
		baseURL = nil
	}
	var metadata FeedImageMetadata
	var ogImageURL string
	var twitterImageURL string
	var genericImageURL string
	var jsonLDImages []string

	walkNodes(doc, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}

		switch node.DataAtom {
		case atom.Meta:
			property := strings.ToLower(strings.TrimSpace(attrValue(node, "property")))
			name := strings.ToLower(strings.TrimSpace(attrValue(node, "name")))
			key := firstNonEmpty(property, name)
			if key == "og:image" || key == "og:image:url" || key == "og:image:secure_url" {
				ogImageURL = firstNonEmpty(ogImageURL, resolveFeedImageURL(attrValue(node, "content"), baseURL))
			} else if key == "twitter:image" || key == "twitter:image:src" {
				twitterImageURL = firstNonEmpty(twitterImageURL, resolveFeedImageURL(attrValue(node, "content"), baseURL))
			} else if key == "image" {
				genericImageURL = firstNonEmpty(genericImageURL, resolveFeedImageURL(attrValue(node, "content"), baseURL))
			}
		case atom.Link:
			rel := strings.Fields(strings.ToLower(attrValue(node, "rel")))
			if containsAny(rel, "image_src") {
				metadata.ImageURL = firstNonEmpty(metadata.ImageURL, resolveFeedImageURL(attrValue(node, "href"), baseURL))
			} else if containsAny(rel, "icon", "shortcut", "apple-touch-icon") {
				metadata.IconURL = firstNonEmpty(metadata.IconURL, resolveFeedImageURL(attrValue(node, "href"), baseURL))
			}
		case atom.Script:
			if strings.Contains(strings.ToLower(attrValue(node, "type")), "ld+json") {
				jsonLDImages = append(jsonLDImages, extractJSONLDImageURLs(node)...)
			}
		}
	})

	metadata.ImageURL = firstNonEmpty(ogImageURL, twitterImageURL, genericImageURL, metadata.ImageURL)
	for _, candidate := range jsonLDImages {
		metadata.ImageURL = firstNonEmpty(metadata.ImageURL, resolveFeedImageURL(candidate, baseURL))
	}
	if metadata.ImageURL == "" {
		metadata.ImageURL = firstFeedContentImageURL(doc, baseURL)
	}
	return metadata, nil
}

func firstFeedContentImageURL(doc *html.Node, baseURL *url.URL) string {
	var result string
	walkNodes(doc, func(node *html.Node) {
		if result != "" || node.Type != html.ElementNode || node.DataAtom != atom.Img {
			return
		}
		candidate := firstNonEmpty(
			attrValue(node, "src"),
			attrValue(node, "data-src"),
			attrValue(node, "data-original"),
			firstSrcSetURL(attrValue(node, "srcset")),
			firstSrcSetURL(attrValue(node, "data-srcset")),
		)
		if isFeedImageNodeDecorative(node, candidate) {
			return
		}
		result = resolveFeedImageURL(candidate, baseURL)
	})
	return result
}

func isFeedImageNodeDecorative(node *html.Node, source string) bool {
	if strings.TrimSpace(source) == "" {
		return true
	}
	lowerSource := strings.ToLower(source)
	alt := strings.ToLower(strings.TrimSpace(attrValue(node, "alt")))
	title := strings.ToLower(strings.TrimSpace(attrValue(node, "title")))
	className := strings.ToLower(strings.TrimSpace(attrValue(node, "class")))
	for _, token := range []string{"favicon", "avatar", "emoji", "icon", "logo", "pixel", "spacer"} {
		if strings.Contains(lowerSource, token) || strings.Contains(alt, token) || strings.Contains(title, token) || strings.Contains(className, token) {
			return true
		}
	}
	if strings.Contains(alt, "头像") || strings.Contains(alt, "图标") || strings.Contains(title, "头像") || strings.Contains(title, "图标") {
		return true
	}
	if width, ok := parsePositiveIntAttr(node, "width"); ok && width <= 64 {
		return true
	}
	if height, ok := parsePositiveIntAttr(node, "height"); ok && height <= 64 {
		return true
	}
	return false
}

func firstSrcSetURL(raw string) string {
	for _, candidate := range strings.Split(raw, ",") {
		fields := strings.Fields(candidate)
		if len(fields) > 0 && strings.TrimSpace(fields[0]) != "" {
			return strings.TrimSpace(fields[0])
		}
	}
	return ""
}

func resolveFeedImageURL(raw string, baseURL *url.URL) string {
	value := strings.TrimSpace(raw)
	if value == "" || strings.HasPrefix(strings.ToLower(value), "data:") {
		return ""
	}
	parsed, err := url.Parse(value)
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

func containsAny(values []string, candidates ...string) bool {
	for _, value := range values {
		for _, candidate := range candidates {
			if value == candidate {
				return true
			}
		}
	}
	return false
}

func extractJSONLDImageURLs(node *html.Node) []string {
	text := strings.TrimSpace(nodeText(node))
	if text == "" {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return nil
	}
	return collectJSONLDImageURLs(value)
}

func collectJSONLDImageURLs(value any) []string {
	return collectJSONLDValue(value, false)
}

func collectJSONLDValue(value any, imageContext bool) []string {
	switch current := value.(type) {
	case []any:
		var result []string
		for _, entry := range current {
			result = append(result, collectJSONLDValue(entry, imageContext)...)
		}
		return result
	case map[string]any:
		var result []string
		for _, key := range []string{"image", "thumbnailUrl"} {
			if candidate, ok := current[key]; ok {
				result = append(result, collectJSONLDValue(candidate, true)...)
			}
		}
		if imageContext {
			for _, key := range []string{"contentUrl", "url"} {
				if candidate, ok := current[key].(string); ok {
					result = append(result, candidate)
				}
			}
		}
		return result
	case string:
		if imageContext {
			return []string{current}
		}
		return nil
	default:
		return nil
	}
}
