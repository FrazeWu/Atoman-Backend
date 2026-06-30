package service

import (
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"atoman/internal/model"
)

// Simplified standard RSS Structures for parsing external feeds.
type ExtRSS struct {
	Channel ExtRSSChannel `xml:"channel"`
}

type ExtRSSChannel struct {
	Title          string               `xml:"title"`
	Items          []ExtRSSItem         `xml:"item"`
	ITunesImage    ExtRSSITunesImageRef `xml:"http://www.itunes.com/dtds/podcast-1.0.dtd image"`
	Image          ExtRSSImageBlock     `xml:"image"`
	MediaThumbnail ExtRSSMediaImageRef  `xml:"http://search.yahoo.com/mrss/ thumbnail"`
	MediaContent   ExtRSSMediaImageRef  `xml:"http://search.yahoo.com/mrss/ content"`
}

type ExtRSSImageBlock struct {
	URL string `xml:"url"`
}

type ExtRSSITunesImageRef struct {
	Href string `xml:"href,attr"`
}

type ExtRSSMediaImageRef struct {
	URL string `xml:"url,attr"`
}

type ExtRSSEnclosure struct {
	URL    string `xml:"url,attr"`
	Type   string `xml:"type,attr"`
	Length string `xml:"length,attr"`
}

type ExtRSSITunesDuration struct {
	Value string `xml:",chardata"`
}

type ExtRSSItem struct {
	Title          string               `xml:"title"`
	Link           string               `xml:"link"`
	Description    string               `xml:"description"`
	PubDate        string               `xml:"pubDate"`
	DCDate         string               `xml:"http://purl.org/dc/elements/1.1/ date"`
	Content        string               `xml:"http://purl.org/rss/1.0/modules/content/ encoded"`
	Creator        string               `xml:"http://purl.org/dc/elements/1.1/ creator"`
	Author         string               `xml:"author"`
	GUID           string               `xml:"guid"`
	Enclosure      ExtRSSEnclosure      `xml:"enclosure"`
	ITunesDur      string               `xml:"http://www.itunes.com/dtds/podcast-1.0.dtd duration"`
	ITunesImage    ExtRSSITunesImageRef `xml:"http://www.itunes.com/dtds/podcast-1.0.dtd image"`
	MediaThumbnail ExtRSSMediaImageRef  `xml:"http://search.yahoo.com/mrss/ thumbnail"`
	MediaContent   ExtRSSMediaImageRef  `xml:"http://search.yahoo.com/mrss/ content"`
}

// Atom Structures
type ExtAtom struct {
	XMLName xml.Name       `xml:"feed"`
	Title   string         `xml:"title"`
	Logo    string         `xml:"logo"`
	Icon    string         `xml:"icon"`
	Entries []ExtAtomEntry `xml:"entry"`
}

type ExtAtomEntry struct {
	Title     string        `xml:"title"`
	Links     []ExtAtomLink `xml:"link"`
	Summary   string        `xml:"summary"`
	Content   string        `xml:"content"`
	Published string        `xml:"published"`
	Updated   string        `xml:"updated"`
	Modified  string        `xml:"modified"`
	Issued    string        `xml:"issued"`
	ID        string        `xml:"id"`
	Author    ExtAtomAuthor `xml:"author"`
}

type ExtAtomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

type normalizedFeedItem struct {
	Title             string
	Link              string
	Identifier        string
	Author            string
	PublishedAt       time.Time
	ContentHTML       string
	SummaryText       string
	ImageURL          string
	EnclosureURL      string
	EnclosureType     string
	Duration          string
	LooksLikeFullText bool
}

var rssFetchHTTPClient = &http.Client{Timeout: 10 * time.Second}

type rssCronConfig struct {
	Enabled      bool
	StartupDelay time.Duration
	Interval     time.Duration
}

type ExtAtomAuthor struct {
	Name  string `xml:"name"`
	Email string `xml:"email"`
	URI   string `xml:"uri"`
}

var firstImageSrcRe = regexp.MustCompile(`(?is)<img[^>]+src=["']([^"' >]+)["']`)
var feedSummaryHTMLTagRe = regexp.MustCompile(`(?is)<[^>]+>`)
var feedSummaryWhitespaceRe = regexp.MustCompile(`\s+`)
var feedSummaryPunctuationSpaceRe = regexp.MustCompile(`\s+([.,;:!?])`)
var rssLogURLRe = regexp.MustCompile(`https?://[^\s"'<>]+`)

func sanitizeRSSLogURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "[invalid-url]"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func sanitizeRSSLogError(err error) error {
	if err == nil {
		return nil
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		copyErr := *urlErr
		copyErr.URL = sanitizeRSSLogURL(copyErr.URL)
		if copyErr.Err != nil {
			copyErr.Err = errors.New(sanitizeRSSLogText(copyErr.Err.Error()))
		}
		return &copyErr
	}
	return errors.New(sanitizeRSSLogText(err.Error()))
}

func sanitizeRSSLogText(text string) string {
	return rssLogURLRe.ReplaceAllStringFunc(text, sanitizeRSSLogURL)
}

func selectFeedContent(item ExtRSSItem) string {
	if content := strings.TrimSpace(item.Content); content != "" {
		return content
	}
	return strings.TrimSpace(item.Description)
}

func truncateFeedSummary(summary string) string {
	runes := []rune(summary)
	if len(runes) > 1000 {
		return string(runes[:1000])
	}
	return summary
}

func buildFeedItemSummary(content string) string {
	clean := html.UnescapeString(strings.TrimSpace(content))
	clean = feedSummaryHTMLTagRe.ReplaceAllString(clean, " ")
	clean = feedSummaryWhitespaceRe.ReplaceAllString(clean, " ")
	clean = feedSummaryPunctuationSpaceRe.ReplaceAllString(clean, "$1")
	clean = strings.TrimSpace(clean)
	return truncateFeedSummary(clean)
}

func buildSummaryFromNormalizedContent(contentHTML string, fallbackSummary string) string {
	contentHTML = strings.TrimSpace(contentHTML)
	if contentHTML != "" {
		return buildFeedItemSummary(contentHTML)
	}
	return buildFeedItemSummary(strings.TrimSpace(fallbackSummary))
}

func inferFeedContentQuality(content string) bool {
	text := buildFeedItemSummary(content)
	if utf8.RuneCountInString(text) < 280 {
		return false
	}
	return strings.Count(text, ".") >= 2 || strings.Count(text, "。") >= 2
}

func parsePreferredRSSDate(item ExtRSSItem, fallbackTime time.Time) time.Time {
	for _, raw := range []string{
		strings.TrimSpace(item.PubDate),
		strings.TrimSpace(item.DCDate),
	} {
		if parsed := parseRSSDate(raw); !parsed.IsZero() {
			return parsed
		}
	}
	return fallbackTime
}

func parsePreferredAtomDate(entry ExtAtomEntry, fallbackTime time.Time) time.Time {
	for _, raw := range []string{
		strings.TrimSpace(entry.Published),
		strings.TrimSpace(entry.Updated),
		strings.TrimSpace(entry.Modified),
		strings.TrimSpace(entry.Issued),
	} {
		if parsed := parseRSSDate(raw); !parsed.IsZero() {
			return parsed
		}
	}
	return fallbackTime
}

func selectAtomAuthor(author ExtAtomAuthor, sourceTitle string) string {
	for _, candidate := range []string{
		strings.TrimSpace(author.Name),
		strings.TrimSpace(author.Email),
		strings.TrimSpace(author.URI),
		strings.TrimSpace(sourceTitle),
	} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func extractFirstImageURLFromHTML(contentHTML string) string {
	matches := firstImageSrcRe.FindStringSubmatch(contentHTML)
	if len(matches) < 2 {
		return ""
	}
	return strings.TrimSpace(matches[1])
}

func selectItemImageURL(itemImageURL string, mediaImageURL string, channelImageURL string, contentHTML string) string {
	for _, candidate := range []string{
		strings.TrimSpace(itemImageURL),
		strings.TrimSpace(mediaImageURL),
		strings.TrimSpace(channelImageURL),
		extractFirstImageURLFromHTML(contentHTML),
	} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func normalizeRSSItem(item ExtRSSItem, sourceTitle string, channelImageURL string, fallbackTime time.Time) normalizedFeedItem {
	identifier := strings.TrimSpace(item.GUID)
	if identifier == "" {
		identifier = strings.TrimSpace(item.Link)
	}

	publishedAt := parsePreferredRSSDate(item, fallbackTime)

	author := strings.TrimSpace(item.Author)
	if author == "" {
		author = strings.TrimSpace(item.Creator)
	}
	if author == "" {
		author = strings.TrimSpace(sourceTitle)
	}

	contentHTML := selectFeedContent(item)
	summaryText := strings.TrimSpace(item.Description)
	imageURL := selectItemImageURL(item.ITunesImage.Href, firstNonEmpty(item.MediaContent.URL, item.MediaThumbnail.URL), channelImageURL, contentHTML)

	return normalizedFeedItem{
		Title:             strings.TrimSpace(item.Title),
		Link:              strings.TrimSpace(item.Link),
		Identifier:        identifier,
		Author:            author,
		PublishedAt:       publishedAt,
		ContentHTML:       contentHTML,
		SummaryText:       summaryText,
		ImageURL:          imageURL,
		EnclosureURL:      strings.TrimSpace(item.Enclosure.URL),
		EnclosureType:     strings.TrimSpace(item.Enclosure.Type),
		Duration:          strings.TrimSpace(item.ITunesDur),
		LooksLikeFullText: inferFeedContentQuality(contentHTML),
	}
}

func normalizeAtomEntry(entry ExtAtomEntry, sourceTitle string, feedImageURL string, fallbackTime time.Time) normalizedFeedItem {
	link := ""
	for _, candidate := range entry.Links {
		if candidate.Rel == "alternate" || candidate.Rel == "" {
			link = strings.TrimSpace(candidate.Href)
			if link != "" {
				break
			}
		}
	}
	if link == "" && len(entry.Links) > 0 {
		link = strings.TrimSpace(entry.Links[0].Href)
	}

	publishedAt := parsePreferredAtomDate(entry, fallbackTime)

	contentHTML := strings.TrimSpace(entry.Content)
	summaryText := strings.TrimSpace(entry.Summary)
	author := selectAtomAuthor(entry.Author, sourceTitle)

	identifier := strings.TrimSpace(entry.ID)
	if identifier == "" {
		identifier = link
	}

	return normalizedFeedItem{
		Title:             strings.TrimSpace(entry.Title),
		Link:              link,
		Identifier:        identifier,
		Author:            author,
		PublishedAt:       publishedAt,
		ContentHTML:       contentHTML,
		SummaryText:       summaryText,
		ImageURL:          selectItemImageURL("", "", feedImageURL, firstNonEmpty(contentHTML, summaryText)),
		LooksLikeFullText: inferFeedContentQuality(contentHTML),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func parseEnvBool(name string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		log.Printf("WARN: invalid %s=%q; using default %t", name, raw, fallback)
		return fallback
	}
	return value
}

func parseEnvDuration(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		log.Printf("WARN: invalid %s=%q; using default %s", name, raw, fallback)
		return fallback
	}
	return value
}

func loadRSSCronConfig() rssCronConfig {
	return rssCronConfig{
		Enabled:      parseEnvBool("RSS_CRON_ENABLED", true),
		StartupDelay: parseEnvDuration("RSS_CRON_STARTUP_DELAY", 60*time.Second),
		Interval:     parseEnvDuration("RSS_CRON_INTERVAL", 15*time.Minute),
	}
}

func buildModelFeedItem(src model.FeedSource, normalized normalizedFeedItem, fetchedAt time.Time) model.FeedItem {
	newFeedItem := model.FeedItem{
		FeedSourceID:  src.ID,
		GUID:          normalized.Identifier,
		Title:         normalized.Title,
		Link:          normalized.Link,
		Summary:       buildSummaryFromNormalizedContent(normalized.ContentHTML, normalized.SummaryText),
		Author:        normalized.Author,
		PublishedAt:   normalized.PublishedAt,
		FetchedAt:     fetchedAt,
		EnclosureURL:  normalized.EnclosureURL,
		EnclosureType: normalized.EnclosureType,
		Duration:      normalized.Duration,
		ImageURL:      normalized.ImageURL,
	}
	newFeedItem.FullTextStatus = defaultFullTextStatusForSource(src, newFeedItem, normalized.LooksLikeFullText)
	return newFeedItem
}

func persistNormalizedFeedItem(db *gorm.DB, src model.FeedSource, normalized normalizedFeedItem, fetchedAt time.Time) error {
	if normalized.Identifier == "" || normalized.Link == "" {
		return nil
	}

	newFeedItem := buildModelFeedItem(src, normalized, fetchedAt)
	result := db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "feed_source_id"},
			{Name: "guid"},
		},
		DoNothing: true,
	}).Create(&newFeedItem)
	if result.Error == nil {
		return nil
	}
	if isFeedItemDuplicateKeyError(result.Error) {
		return nil
	}
	return result.Error
}

func isFeedItemDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		constraint := strings.ToLower(pgErr.ConstraintName)
		detail := strings.ToLower(pgErr.Detail)
		return constraint == "idx_feed_items_source_guid" ||
			constraint == "idx_feed_items_source_link" ||
			(strings.Contains(detail, "feed_source_id") &&
				(strings.Contains(detail, "guid") || strings.Contains(detail, "link")))
	}

	var pqErr *pq.Error
	if errors.As(err, &pqErr) && string(pqErr.Code) == "23505" {
		constraint := strings.ToLower(pqErr.Constraint)
		detail := strings.ToLower(pqErr.Detail)
		return constraint == "idx_feed_items_source_guid" ||
			constraint == "idx_feed_items_source_link" ||
			(strings.Contains(detail, "feed_source_id") &&
				(strings.Contains(detail, "guid") || strings.Contains(detail, "link")))
	}

	message := strings.ToLower(err.Error())
	return strings.Contains(message, "idx_feed_items_source_guid") ||
		strings.Contains(message, "idx_feed_items_source_link") ||
		(strings.Contains(message, "unique constraint failed") && strings.Contains(message, "feed_items.feed_source_id")) ||
		(strings.Contains(message, "duplicate key") && strings.Contains(message, "feed_items"))
}

func persistParsedFeedItems(db *gorm.DB, src model.FeedSource, items []ExtRSSItem, sourceTitle string, sourceCoverURL string, fetchedAt time.Time) error {
	for _, raw := range items {
		normalized := normalizeRSSItem(raw, sourceTitle, sourceCoverURL, fetchedAt)
		if err := persistNormalizedFeedItem(db, src, normalized, fetchedAt); err != nil {
			return err
		}
	}
	return nil
}

func applyFetchedSourceUpdates(db *gorm.DB, src *model.FeedSource, sourceTitle string, sourceCoverURL string, fetchedAt time.Time) error {
	if src.Title == "" && sourceTitle != "" {
		src.Title = sourceTitle
	}
	if sourceCoverURL != "" {
		src.CoverURL = sourceCoverURL
	}
	src.LastFetchedAt = &fetchedAt
	return db.Save(src).Error
}

// StartRSSCron starts a background worker that fetches all unique RSS URLs periodically
func StartRSSCron(db *gorm.DB) {
	cfg := loadRSSCronConfig()
	if !cfg.Enabled {
		log.Println("RSS cron worker disabled by RSS_CRON_ENABLED=false")
		return
	}

	go func() {
		time.Sleep(cfg.StartupDelay)
		log.Println("Starting initial RSS sync...")
		syncAllRSSFeeds(db)

		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()

		for range ticker.C {
			log.Println("Running scheduled RSS sync...")
			syncAllRSSFeeds(db)
		}
	}()
}

func syncAllRSSFeeds(db *gorm.DB) {
	total := 0
	success := 0
	failed := 0
	skipped := 0
	defer func() {
		log.Printf("RSS sync completed: total=%d success=%d failed=%d skipped=%d", total, success, failed, skipped)
	}()

	// 1. Get all unique active RSS URLs to minimize HTTP calls
	var uniqueURLs []string
	if err := db.Model(&model.FeedSource{}).
		Where("source_type = ?", "external_rss").
		Distinct("rss_url").
		Pluck("rss_url", &uniqueURLs).Error; err != nil {
		log.Printf("RSS sync failed to fetch unique urls: %v", err)
		return
	}

	for _, url := range uniqueURLs {
		if url == "" {
			skipped++
			continue
		}
		total++
		// 跳过相对路径或非 http(s) URL（内部 RSS 端点误存为 external_rss 时的兜底保护）
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			log.Printf("RSS sync skipping non-absolute URL: %s", sanitizeRSSLogURL(url))
			skipped++
			continue
		}

		// 2. Fetch and parse the feed
		items, sourceTitle, sourceCoverURL, err := FetchAndParseRSS(url)
		if err != nil {
			log.Printf("Failed to fetch RSS %s: %v", sanitizeRSSLogURL(url), sanitizeRSSLogError(err))
			failed++
			continue
		}

		// 3. Find all FeedSources (users subscribed) to this URL
		var sources []model.FeedSource
		if err := db.Where("source_type = ? AND rss_url = ?", "external_rss", url).Find(&sources).Error; err != nil {
			log.Printf("failed to fetch feed sources for %s: %v", sanitizeRSSLogURL(url), err)
			failed++
			continue
		}

		now := time.Now()
		urlFailed := false

		for _, src := range sources {
			if err := persistParsedFeedItems(db, src, items, sourceTitle, sourceCoverURL, now); err != nil {
				log.Printf("failed to persist feed items for %s: %v", sanitizeRSSLogURL(src.RssURL), err)
				urlFailed = true
				continue
			}
			if err := applyFetchedSourceUpdates(db, &src, sourceTitle, sourceCoverURL, now); err != nil {
				log.Printf("failed to update source metadata for %s: %v", sanitizeRSSLogURL(src.RssURL), err)
				urlFailed = true
			}
		}

		if urlFailed {
			failed++
			continue
		}
		success++
	}
}

func SyncSingleRSS(db *gorm.DB, src model.FeedSource) {
	if src.SourceType != "external_rss" || src.RssURL == "" {
		return
	}
	if err := ValidateFullTextTargetURL(src.RssURL); err != nil {
		log.Printf("SyncSingleRSS skipping invalid URL: %s", sanitizeRSSLogURL(src.RssURL))
		return
	}

	items, sourceTitle, sourceCoverURL, err := FetchAndParseRSS(src.RssURL)
	if err != nil {
		log.Printf("Immediate RSS sync failed for %s: %v", sanitizeRSSLogURL(src.RssURL), sanitizeRSSLogError(err))
		return
	}

	now := time.Now()

	if err := persistParsedFeedItems(db, src, items, sourceTitle, sourceCoverURL, now); err != nil {
		log.Printf("failed to persist feed items for %s: %v", sanitizeRSSLogURL(src.RssURL), err)
		return
	}
	if err := applyFetchedSourceUpdates(db, &src, sourceTitle, sourceCoverURL, now); err != nil {
		log.Printf("failed to update source metadata for %s: %v", sanitizeRSSLogURL(src.RssURL), err)
	}
}

func FetchAndParseRSS(feedURL string) ([]ExtRSSItem, string, string, error) {
	if err := ValidateFullTextTargetURL(feedURL); err != nil {
		return nil, "", "", err
	}

	client := rssClientWithRedirectValidation(rssFetchHTTPClient)
	req, err := http.NewRequest("GET", feedURL, nil)
	if err != nil {
		return nil, "", "", err
	}
	// Many servers reject Go default user-agent
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", "", err
	}

	// Remove leading spaces before XML declaration if any (simple sanitize)
	bodyStr := strings.TrimSpace(string(bodyBytes))

	// Try RSS first
	var parsedRSS ExtRSS
	if err := xml.Unmarshal([]byte(bodyStr), &parsedRSS); err == nil && parsedRSS.Channel.Title != "" {
		coverURL := firstNonEmpty(
			parsedRSS.Channel.ITunesImage.Href,
			parsedRSS.Channel.MediaContent.URL,
			parsedRSS.Channel.MediaThumbnail.URL,
			parsedRSS.Channel.Image.URL,
		)
		return parsedRSS.Channel.Items, parsedRSS.Channel.Title, coverURL, nil
	}

	// Try Atom
	var parsedAtom ExtAtom
	if err := xml.Unmarshal([]byte(bodyStr), &parsedAtom); err == nil {
		feedImageURL := firstNonEmpty(parsedAtom.Logo, parsedAtom.Icon)
		items := make([]ExtRSSItem, len(parsedAtom.Entries))
		for i, entry := range parsedAtom.Entries {
			normalized := normalizeAtomEntry(entry, strings.TrimSpace(parsedAtom.Title), feedImageURL, time.Time{})

			items[i] = ExtRSSItem{
				Title:       normalized.Title,
				Link:        normalized.Link,
				Description: normalized.ContentHTML,
				Content:     normalized.ContentHTML,
				PubDate:     normalized.PublishedAt.Format(time.RFC3339),
				GUID:        normalized.Identifier,
				Author:      normalized.Author,
				ITunesImage: ExtRSSITunesImageRef{Href: normalized.ImageURL},
			}
			if normalized.PublishedAt.IsZero() {
				items[i].PubDate = ""
			}
			if items[i].Description == "" {
				items[i].Description = normalized.SummaryText
			}
		}
		return items, strings.TrimSpace(parsedAtom.Title), feedImageURL, nil
	}

	return nil, "", "", fmt.Errorf("failed to parse feed as RSS or Atom")
}

func rssClientWithRedirectValidation(base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	wrapped := *base
	previousCheckRedirect := base.CheckRedirect
	wrapped.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := ValidateFullTextTargetURL(req.URL.String()); err != nil {
			return err
		}
		if previousCheckRedirect != nil {
			return previousCheckRedirect(req, via)
		}
		return nil
	}
	return &wrapped
}

func parseRSSDate(dateStr string) time.Time {
	if dateStr == "" {
		return time.Time{}
	}
	// Try a few common RSS formats
	formats := []string{
		time.RFC1123Z,
		time.RFC1123,
		time.RFC822Z,
		time.RFC822,
		time.RFC3339,
		"2006-01-02T15:04:05Z", // ISO8601
		"2006-01-02T15:04:05-07:00",
		"Mon, 02 Jan 2006 15:04:05 -0700",
		"2006-01-02 15:04:05",
	}

	for _, format := range formats {
		if t, err := time.Parse(format, dateStr); err == nil {
			return t
		}
	}
	return time.Time{}
}
