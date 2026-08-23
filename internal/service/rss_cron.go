package service

import (
	"context"
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
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"atoman/internal/feedlanguage"
	"atoman/internal/model"
)

// Simplified standard RSS Structures for parsing external feeds.
type ExtRSS struct {
	Channel ExtRSSChannel `xml:"channel"`
}

type ExtRSSChannel struct {
	Title          string               `xml:"title"`
	Language       string               `xml:"language"`
	XMLLanguage    string               `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
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
	Language       string               `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
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
	XMLName  xml.Name       `xml:"feed"`
	Language string         `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
	Title    string         `xml:"title"`
	Logo     string         `xml:"logo"`
	Icon     string         `xml:"icon"`
	Entries  []ExtAtomEntry `xml:"entry"`
}

type ExtAtomEntry struct {
	Title     string        `xml:"title"`
	Language  string        `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
	Links     []ExtAtomLink `xml:"link"`
	Summary   string        `xml:"-"`
	Content   string        `xml:"-"`
	Published string        `xml:"published"`
	Updated   string        `xml:"updated"`
	Modified  string        `xml:"modified"`
	Issued    string        `xml:"issued"`
	ID        string        `xml:"id"`
	Author    ExtAtomAuthor `xml:"author"`
}

type atomHTMLValue struct {
	Type      string `xml:"type,attr"`
	InnerHTML string `xml:",innerxml"`
}

func (entry *ExtAtomEntry) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	var decoded struct {
		Title     string        `xml:"title"`
		Language  string        `xml:"http://www.w3.org/XML/1998/namespace lang,attr"`
		Links     []ExtAtomLink `xml:"link"`
		Summary   atomHTMLValue `xml:"summary"`
		Content   atomHTMLValue `xml:"content"`
		Published string        `xml:"published"`
		Updated   string        `xml:"updated"`
		Modified  string        `xml:"modified"`
		Issued    string        `xml:"issued"`
		ID        string        `xml:"id"`
		Author    ExtAtomAuthor `xml:"author"`
	}
	if err := decoder.DecodeElement(&decoded, &start); err != nil {
		return err
	}
	entry.Title = decoded.Title
	entry.Language = decoded.Language
	entry.Links = decoded.Links
	entry.Summary = decodeAtomHTMLValue(decoded.Summary)
	entry.Content = decodeAtomHTMLValue(decoded.Content)
	entry.Published = decoded.Published
	entry.Updated = decoded.Updated
	entry.Modified = decoded.Modified
	entry.Issued = decoded.Issued
	entry.ID = decoded.ID
	entry.Author = decoded.Author
	return nil
}

func decodeAtomHTMLValue(value atomHTMLValue) string {
	inner := strings.TrimSpace(value.InnerHTML)
	if inner == "" {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(value.Type), "xhtml") {
		return inner
	}
	return html.UnescapeString(inner)
}

type ExtAtomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

type normalizedFeedItem struct {
	LanguageCode  string
	Title         string
	Link          string
	Identifier    string
	Author        string
	PublishedAt   time.Time
	ContentHTML   string
	SummaryText   string
	ImageURL      string
	EnclosureURL  string
	EnclosureType string
	Duration      string
}

var rssFetchHTTPClient = &http.Client{
	Timeout:   10 * time.Second,
	Transport: newFullTextSafeHTTPTransport(),
}

const (
	rssFetchProviderDirect   = "direct"
	rssFetchStatusIdle       = "idle"
	rssFetchStatusFetching   = "fetching"
	rssFetchStatusHealthy    = "healthy"
	rssFetchStatusWarning    = "warning"
	rssFetchStatusBlocked    = "blocked"
	rssFetchMaxResponseBytes = 10 * 1024 * 1024
)

type RSSFetchConditions struct {
	ETag         string
	LastModified string
}

type RSSFetchResult struct {
	Items          []ExtRSSItem
	SourceTitle    string
	SourceCoverURL string
	NotModified    bool
	HTTPStatus     int
	ETag           string
	LastModified   string
	Provider       string
	Duration       time.Duration
}

type rssFetchError struct {
	Code       string
	HTTPStatus int
	Err        error
}

func (e *rssFetchError) Error() string {
	if e.Err == nil {
		return e.Code
	}
	return e.Err.Error()
}

func (e *rssFetchError) Unwrap() error { return e.Err }

var rssFetchLocks = struct {
	sync.Mutex
	byURL map[string]*sync.Mutex
}{byURL: make(map[string]*sync.Mutex)}

func normalizeRSSFetchURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func withRSSFetchLock(feedURL string, fn func() error) error {
	key := normalizeRSSFetchURL(feedURL)
	rssFetchLocks.Lock()
	lock := rssFetchLocks.byURL[key]
	if lock == nil {
		lock = &sync.Mutex{}
		rssFetchLocks.byURL[key] = lock
	}
	rssFetchLocks.Unlock()

	lock.Lock()
	defer lock.Unlock()
	return fn()
}

type rssCronConfig struct {
	Enabled      bool
	StartupDelay time.Duration
	Interval     time.Duration
	Concurrency  int
}

type ExtAtomAuthor struct {
	Name  string `xml:"name"`
	Email string `xml:"email"`
	URI   string `xml:"uri"`
}

var firstImageSrcRe = regexp.MustCompile(`(?is)<img[^>]+(?:src|data-src|data-original)=["']([^"' >]+)["']`)
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
	itemImageURL := firstNonEmpty(item.ITunesImage.Href, item.MediaContent.URL, item.MediaThumbnail.URL)
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(item.Enclosure.Type)), "image/") {
		itemImageURL = firstNonEmpty(itemImageURL, item.Enclosure.URL)
	}
	imageURL := selectItemImageURL(itemImageURL, "", channelImageURL, contentHTML)
	if linkURL, err := url.Parse(strings.TrimSpace(item.Link)); err == nil {
		imageURL = resolveFeedImageURL(imageURL, linkURL)
	}

	return normalizedFeedItem{
		LanguageCode:  firstNonEmpty(feedlanguage.NormalizeCode(item.Language), feedlanguage.Detect(strings.Join([]string{item.Title, item.Description, item.Content}, " "))),
		Title:         strings.TrimSpace(item.Title),
		Link:          strings.TrimSpace(item.Link),
		Identifier:    identifier,
		Author:        author,
		PublishedAt:   publishedAt,
		ContentHTML:   contentHTML,
		SummaryText:   summaryText,
		ImageURL:      imageURL,
		EnclosureURL:  strings.TrimSpace(item.Enclosure.URL),
		EnclosureType: strings.TrimSpace(item.Enclosure.Type),
		Duration:      strings.TrimSpace(item.ITunesDur),
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

	imageURL := selectItemImageURL("", "", feedImageURL, firstNonEmpty(contentHTML, summaryText))
	if linkURL, err := url.Parse(link); err == nil {
		imageURL = resolveFeedImageURL(imageURL, linkURL)
	}

	return normalizedFeedItem{
		LanguageCode: firstNonEmpty(feedlanguage.NormalizeCode(entry.Language), feedlanguage.Detect(strings.Join([]string{entry.Title, entry.Summary, entry.Content}, " "))),
		Title:        strings.TrimSpace(entry.Title),
		Link:         link,
		Identifier:   identifier,
		Author:       author,
		PublishedAt:  publishedAt,
		ContentHTML:  contentHTML,
		SummaryText:  summaryText,
		ImageURL:     imageURL,
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
		Concurrency:  parseEnvPositiveInt("RSS_CRON_CONCURRENCY", 4),
	}
}

func buildModelFeedItem(src model.FeedSource, normalized normalizedFeedItem, fetchedAt time.Time) model.FeedItem {
	newFeedItem := model.FeedItem{
		FeedSourceID:       src.ID,
		LanguageCode:       normalized.LanguageCode,
		GUID:               normalized.Identifier,
		Title:              normalized.Title,
		Link:               normalized.Link,
		Summary:            buildSummaryFromNormalizedContent(normalized.ContentHTML, normalized.SummaryText),
		Author:             normalized.Author,
		PublishedAt:        normalized.PublishedAt,
		FetchedAt:          fetchedAt,
		EnclosureURL:       normalized.EnclosureURL,
		EnclosureType:      normalized.EnclosureType,
		Duration:           normalized.Duration,
		ImageURL:           normalized.ImageURL,
		ReaderSource:       ReaderSourceSummary,
		ReaderQualityFlags: ReaderQualityFlagsJSON(nil),
	}

	feedCandidate, err := SanitizeFeedContent(normalized.Link, firstNonEmpty(normalized.ContentHTML, normalized.SummaryText))
	if err == nil {
		newFeedItem.FeedContentHTML = feedCandidate.HTML
		newFeedItem.ReaderHTML = feedCandidate.HTML
		newFeedItem.ReaderSource = feedCandidate.Source
		newFeedItem.ReaderQualityScore = feedCandidate.QualityScore
		newFeedItem.ReaderQualityFlags = ReaderQualityFlagsJSON(feedCandidate.QualityFlags)
		newFeedItem.ReaderVersion = ReaderVersionCurrent
		newFeedItem.ReaderContentHash = feedCandidate.ContentHash
	}
	feedIsComplete := newFeedItem.ReaderSource == ReaderSourceFeed && newFeedItem.ReaderQualityScore >= ReaderQualityReadyThreshold
	newFeedItem.FullTextStatus = defaultFullTextStatusForSource(src, newFeedItem, feedIsComplete)
	return newFeedItem
}

func persistNormalizedFeedItem(db *gorm.DB, src model.FeedSource, normalized normalizedFeedItem, fetchedAt time.Time) (bool, error) {
	if normalized.Identifier == "" || normalized.Link == "" {
		return false, nil
	}

	newFeedItem := buildModelFeedItem(src, normalized, fetchedAt)
	result := db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "feed_source_id"},
			{Name: "guid"},
		},
		DoNothing: true,
	}).Create(&newFeedItem)
	if result.Error == nil && result.RowsAffected > 0 {
		return true, nil
	}
	if result.Error != nil && !isFeedItemDuplicateKeyError(result.Error) {
		return false, result.Error
	}

	var existing model.FeedItem
	if err := db.Where("feed_source_id = ? AND guid = ?", src.ID, normalized.Identifier).First(&existing).Error; err != nil {
		if result.Error != nil && isFeedItemDuplicateKeyError(result.Error) {
			return false, nil
		}
		return false, err
	}
	updates := map[string]any{
		"title":             newFeedItem.Title,
		"link":              newFeedItem.Link,
		"summary":           newFeedItem.Summary,
		"author":            newFeedItem.Author,
		"published_at":      newFeedItem.PublishedAt,
		"fetched_at":        newFeedItem.FetchedAt,
		"enclosure_url":     newFeedItem.EnclosureURL,
		"enclosure_type":    newFeedItem.EnclosureType,
		"duration":          newFeedItem.Duration,
		"image_url":         newFeedItem.ImageURL,
		"feed_content_html": newFeedItem.FeedContentHTML,
	}
	if newFeedItem.LanguageCode != "" {
		updates["language_code"] = newFeedItem.LanguageCode
	}
	if newFeedItem.ReaderHTML != "" && (existing.ReaderSource != ReaderSourcePage || newFeedItem.ReaderQualityScore > existing.ReaderQualityScore) {
		updates["reader_html"] = newFeedItem.ReaderHTML
		updates["reader_source"] = newFeedItem.ReaderSource
		updates["reader_quality_score"] = newFeedItem.ReaderQualityScore
		updates["reader_quality_flags"] = newFeedItem.ReaderQualityFlags
		updates["reader_version"] = newFeedItem.ReaderVersion
		updates["reader_content_hash"] = newFeedItem.ReaderContentHash
	}
	if existing.FullTextStatus == FullTextStatusDisabled && newFeedItem.FullTextStatus == FullTextStatusPending {
		updates["full_text_status"] = FullTextStatusPending
		updates["next_full_text_attempt_at"] = nil
	}
	return false, db.Model(&model.FeedItem{}).Where("id = ?", existing.ID).Updates(updates).Error
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

func persistParsedFeedItems(db *gorm.DB, src model.FeedSource, items []ExtRSSItem, sourceTitle string, sourceCoverURL string, fetchedAt time.Time) (int64, error) {
	var inserted int64
	for _, raw := range items {
		normalized := normalizeRSSItem(raw, sourceTitle, sourceCoverURL, fetchedAt)
		created, err := persistNormalizedFeedItem(db, src, normalized, fetchedAt)
		if err != nil {
			return inserted, err
		}
		if created {
			inserted++
		}
	}
	return inserted, nil
}

func applyFetchedSourceUpdates(db *gorm.DB, src *model.FeedSource, sourceTitle string, sourceCoverURL string, fetchedAt time.Time) error {
	updates := map[string]interface{}{
		"last_fetched_at": fetchedAt,
	}
	if src.Title == "" && sourceTitle != "" {
		updates["title"] = sourceTitle
	}
	if sourceCoverURL != "" {
		updates["cover_url"] = sourceCoverURL
	}
	return db.Model(src).Updates(updates).Error
}

func applyFetchedSourceLanguage(db *gorm.DB, src *model.FeedSource, items []ExtRSSItem) error {
	languageCode := dominantFeedLanguage(items)
	if languageCode == "" {
		return nil
	}
	src.LanguageCode = languageCode
	return db.Model(src).Update("language_code", languageCode).Error
}

func dominantFeedLanguage(items []ExtRSSItem) string {
	counts := make(map[string]int)
	for _, item := range items {
		code := feedlanguage.NormalizeCode(item.Language)
		if code == "" {
			code = feedlanguage.Detect(strings.Join([]string{item.Title, item.Description, item.Content}, " "))
		}
		if code != "" {
			counts[code]++
		}
	}
	bestCode := ""
	bestCount := 0
	for code, count := range counts {
		if count > bestCount {
			bestCode = code
			bestCount = count
		}
	}
	return bestCode
}

// StartRSSCron starts a background worker that fetches due RSS URLs periodically.
func StartRSSCron(ctx context.Context, db *gorm.DB) <-chan struct{} {
	cfg := loadRSSCronConfig()
	if !cfg.Enabled {
		log.Println("RSS cron worker disabled by RSS_CRON_ENABLED=false")
		done := make(chan struct{})
		close(done)
		return done
	}

	return startPeriodicWorker(ctx, cfg.StartupDelay, cfg.Interval, func() {
		log.Println("Running scheduled RSS sync...")
		syncAllRSSFeeds(db, cfg.Concurrency)
	})
}

type rssSourceGroup struct {
	URL             string
	Sources         []model.FeedSource
	HasSubscription bool
}

func listDueRSSSourceGroups(db *gorm.DB, now time.Time) ([]rssSourceGroup, error) {
	var sources []model.FeedSource
	if err := db.Where("source_type = ? AND hidden = ?", "external_rss", false).
		Where("fetch_next_at IS NULL OR fetch_next_at <= ?", now).
		Order("fetch_next_at ASC NULLS FIRST, created_at ASC").Find(&sources).Error; err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, 0, len(sources))
	for _, source := range sources {
		ids = append(ids, source.ID)
	}
	type subscriptionCount struct {
		FeedSourceID uuid.UUID
		Count        int64
	}
	var counts []subscriptionCount
	if err := db.Model(&model.Subscription{}).
		Select("feed_source_id, COUNT(*) AS count").
		Where("feed_source_id IN ?", ids).
		Group("feed_source_id").Find(&counts).Error; err != nil {
		return nil, err
	}
	countBySource := make(map[uuid.UUID]int64, len(counts))
	for _, count := range counts {
		countBySource[count.FeedSourceID] = count.Count
	}

	groupsByURL := make(map[string]*rssSourceGroup)
	orderedURLs := make([]string, 0, len(sources))
	for _, source := range sources {
		url := normalizeRSSFetchURL(source.RssURL)
		if url == "" {
			continue
		}
		group := groupsByURL[url]
		if group == nil {
			group = &rssSourceGroup{URL: source.RssURL}
			groupsByURL[url] = group
			orderedURLs = append(orderedURLs, url)
		}
		group.Sources = append(group.Sources, source)
		if countBySource[source.ID] > 0 {
			group.HasSubscription = true
		}
	}

	groups := make([]rssSourceGroup, 0, len(orderedURLs))
	for _, url := range orderedURLs {
		groups = append(groups, *groupsByURL[url])
	}
	return groups, nil
}

func syncAllRSSFeeds(db *gorm.DB, concurrency int) {
	now := time.Now().UTC()
	groups, err := listDueRSSSourceGroups(db, now)
	if err != nil {
		log.Printf("RSS sync failed to fetch due sources: %v", err)
		return
	}

	total := len(groups)
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > total {
		concurrency = total
	}

	jobs := make(chan rssSourceGroup)
	results := make(chan bool, total)
	var workers sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		workers.Add(1)
		go func(jobs <-chan rssSourceGroup, db *gorm.DB) {
			defer workers.Done()
			for group := range jobs {
				if _, _, err := syncRSSSourceGroup(db, group); err != nil {
					log.Printf("Failed to fetch RSS %s: %v", sanitizeRSSLogURL(group.URL), sanitizeRSSLogError(err))
					results <- false
					continue
				}
				results <- true
			}
		}(jobs, db)
	}
	for _, group := range groups {
		jobs <- group
	}
	close(jobs)
	workers.Wait()
	close(results)

	success := 0
	failed := 0
	for ok := range results {
		if ok {
			success++
		} else {
			failed++
		}
	}
	log.Printf("RSS sync completed: total=%d success=%d failed=%d skipped=0", total, success, failed)
}

func syncRSSSourceGroup(db *gorm.DB, group rssSourceGroup) (int, int64, error) {
	var fetchedItems int
	var newItems int64
	var syncErr error
	err := withRSSFetchLock(group.URL, func() error {
		startedAt := time.Now()
		now := startedAt.UTC()
		if err := markRSSFetchStarted(db, group.Sources); err != nil {
			return err
		}

		representative := group.Sources[0]
		fetchResult, err := fetchAndParseRSS(group.URL, RSSFetchConditions{
			ETag:         representative.FetchETag,
			LastModified: representative.FetchLastModified,
		})
		fetchResult.Duration = time.Since(startedAt)
		if err != nil {
			markErr := markRSSFetchFailure(db, group.Sources, err, fetchResult, now)
			if markErr != nil {
				return errors.Join(err, markErr)
			}
			return err
		}

		if fetchResult.NotModified {
			return markRSSFetchSuccess(db, group, fetchResult, now, 0)
		}

		failSync := func(syncErr error) error {
			failure := &rssFetchError{Code: "persist_failed", Err: syncErr}
			if markErr := markRSSFetchFailure(db, group.Sources, failure, fetchResult, now); markErr != nil {
				return errors.Join(syncErr, markErr)
			}
			return syncErr
		}

		fetchedItems = len(fetchResult.Items)
		for _, source := range group.Sources {
			inserted, persistErr := persistParsedFeedItems(db, source, fetchResult.Items, fetchResult.SourceTitle, fetchResult.SourceCoverURL, now)
			newItems += inserted
			if persistErr != nil {
				return failSync(persistErr)
			}
			sourceCopy := source
			if err := applyFetchedSourceLanguage(db, &sourceCopy, fetchResult.Items); err != nil {
				return failSync(err)
			}
			if err := applyFetchedSourceUpdates(db, &sourceCopy, fetchResult.SourceTitle, fetchResult.SourceCoverURL, now); err != nil {
				return failSync(err)
			}
		}
		return markRSSFetchSuccess(db, group, fetchResult, now, fetchedItems)
	})
	syncErr = err
	return fetchedItems, newItems, syncErr
}

func rssFetchInterval(hasSubscription bool) time.Duration {
	if hasSubscription {
		return 15 * time.Minute
	}
	return 6 * time.Hour
}

func markRSSFetchStarted(db *gorm.DB, sources []model.FeedSource) error {
	ids := make([]uuid.UUID, 0, len(sources))
	for _, source := range sources {
		ids = append(ids, source.ID)
	}
	return db.Model(&model.FeedSource{}).Where("id IN ?", ids).Updates(map[string]any{
		"fetch_status": rssFetchStatusFetching,
	}).Error
}

func markRSSFetchSuccess(db *gorm.DB, group rssSourceGroup, result RSSFetchResult, now time.Time, itemCount int) error {
	nextAt := now.Add(rssFetchInterval(group.HasSubscription))
	ids := make([]uuid.UUID, 0, len(group.Sources))
	for _, source := range group.Sources {
		ids = append(ids, source.ID)
	}
	updates := map[string]any{
		"fetch_status":               rssFetchStatusHealthy,
		"fetch_provider":             result.Provider,
		"fetch_http_status":          result.HTTPStatus,
		"fetch_etag":                 result.ETag,
		"fetch_last_modified":        result.LastModified,
		"fetch_last_success_at":      now,
		"fetch_next_at":              nextAt,
		"fetch_consecutive_failures": 0,
		"fetch_last_error_code":      "",
		"fetch_last_error":           "",
		"fetch_last_duration_ms":     result.Duration.Milliseconds(),
		"fetch_last_item_count":      itemCount,
		"last_error":                 "",
		"health_status":              "healthy",
	}
	return db.Model(&model.FeedSource{}).Where("id IN ?", ids).Updates(updates).Error
}

func markRSSFetchFailure(db *gorm.DB, sources []model.FeedSource, err error, result RSSFetchResult, now time.Time) error {
	code := "request_failed"
	if fetchErr := new(rssFetchError); errors.As(err, &fetchErr) {
		code = fetchErr.Code
		if result.HTTPStatus == 0 {
			result.HTTPStatus = fetchErr.HTTPStatus
		}
	}
	message := sanitizeRSSLogError(err).Error()
	ids := make([]uuid.UUID, 0, len(sources))
	attempt := 1
	for _, source := range sources {
		ids = append(ids, source.ID)
		if source.FetchConsecutiveFailures+1 > attempt {
			attempt = source.FetchConsecutiveFailures + 1
		}
	}
	nextAt := now.Add(feedFetchRetryDelay(code, attempt))
	status := rssFetchStatusWarning
	if code == "http_401" || code == "http_403" || code == "http_429" {
		status = rssFetchStatusBlocked
	}
	updates := map[string]any{
		"fetch_status":               status,
		"fetch_provider":             result.Provider,
		"fetch_http_status":          result.HTTPStatus,
		"fetch_next_at":              nextAt,
		"fetch_consecutive_failures": attempt,
		"fetch_last_error_code":      code,
		"fetch_last_error":           message,
		"fetch_last_duration_ms":     result.Duration.Milliseconds(),
		"last_error":                 message,
		"health_status":              "error",
	}
	return db.Model(&model.FeedSource{}).Where("id IN ?", ids).Updates(updates).Error
}

func feedFetchRetryDelay(code string, attempt int) time.Duration {
	if code == "http_401" || code == "http_403" || code == "http_429" {
		if attempt >= 3 {
			return 24 * time.Hour
		}
		return 6 * time.Hour
	}
	delays := []time.Duration{5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 24 * time.Hour}
	if attempt < 1 {
		attempt = 1
	}
	if attempt > len(delays) {
		attempt = len(delays)
	}
	return delays[attempt-1]
}

type RSSSyncResult struct {
	FeedSourceID uuid.UUID `json:"feed_source_id"`
	FetchedItems int       `json:"fetched_items"`
	NewItems     int64     `json:"new_items"`
	SyncedAt     time.Time `json:"synced_at"`
}

func SyncSingleRSS(db *gorm.DB, src model.FeedSource) {
	if _, err := SyncSingleRSSWithResult(db, src); err != nil {
		log.Printf("Immediate RSS sync failed for %s: %v", sanitizeRSSLogURL(src.RssURL), sanitizeRSSLogError(err))
	}
}

func SyncSingleRSSWithResult(db *gorm.DB, src model.FeedSource) (RSSSyncResult, error) {
	result := RSSSyncResult{FeedSourceID: src.ID, SyncedAt: time.Now().UTC()}
	if src.SourceType != "external_rss" || src.RssURL == "" {
		return result, errors.New("source is not an external RSS feed")
	}
	if err := ValidateFullTextTargetURL(src.RssURL); err != nil {
		return result, err
	}

	var subscriptionCount int64
	if err := db.Model(&model.Subscription{}).Where("feed_source_id = ?", src.ID).Count(&subscriptionCount).Error; err != nil {
		return result, err
	}
	fetchedItems, newItems, err := syncRSSSourceGroup(db, rssSourceGroup{
		URL:             src.RssURL,
		Sources:         []model.FeedSource{src},
		HasSubscription: subscriptionCount > 0,
	})
	result.FetchedItems = fetchedItems
	result.NewItems = newItems
	return result, err
}

func FetchAndParseRSS(feedURL string) ([]ExtRSSItem, string, string, error) {
	result, err := fetchAndParseRSS(feedURL, RSSFetchConditions{})
	if err != nil {
		return nil, "", "", err
	}
	return result.Items, result.SourceTitle, result.SourceCoverURL, nil
}

func fetchAndParseRSS(feedURL string, conditions RSSFetchConditions) (RSSFetchResult, error) {
	startedAt := time.Now()
	result := RSSFetchResult{Provider: rssFetchProviderDirect}
	if err := ValidateFullTextTargetURL(feedURL); err != nil {
		return result, &rssFetchError{Code: "ssrf_blocked", Err: err}
	}

	client := rssClientWithRedirectValidation(rssFetchHTTPClient)
	req, err := http.NewRequest("GET", feedURL, nil)
	if err != nil {
		return result, &rssFetchError{Code: "invalid_request", Err: err}
	}
	// Many servers reject Go default user-agent.
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml;q=0.9, */*;q=0.1")
	if strings.TrimSpace(conditions.ETag) != "" {
		req.Header.Set("If-None-Match", conditions.ETag)
	}
	if strings.TrimSpace(conditions.LastModified) != "" {
		req.Header.Set("If-Modified-Since", conditions.LastModified)
	}

	resp, err := client.Do(req)
	if err != nil {
		return result, &rssFetchError{Code: "request_failed", Err: err}
	}
	defer resp.Body.Close()

	result.HTTPStatus = resp.StatusCode
	result.ETag = strings.TrimSpace(resp.Header.Get("ETag"))
	result.LastModified = strings.TrimSpace(resp.Header.Get("Last-Modified"))
	result.Duration = time.Since(startedAt)
	if resp.StatusCode == http.StatusNotModified {
		result.NotModified = true
		return result, nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return result, &rssFetchError{
			Code:       rssFetchErrorCode(resp.StatusCode),
			HTTPStatus: resp.StatusCode,
			Err:        fmt.Errorf("feed returned HTTP %d", resp.StatusCode),
		}
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, rssFetchMaxResponseBytes+1))
	if err != nil {
		return result, &rssFetchError{Code: "response_read_failed", HTTPStatus: resp.StatusCode, Err: err}
	}
	if int64(len(bodyBytes)) > rssFetchMaxResponseBytes {
		return result, &rssFetchError{
			Code:       "response_too_large",
			HTTPStatus: resp.StatusCode,
			Err:        fmt.Errorf("feed response exceeds %d bytes", rssFetchMaxResponseBytes),
		}
	}

	items, title, coverURL, err := parseRSSDocument(strings.TrimSpace(string(bodyBytes)), feedURL)
	if err != nil {
		return result, &rssFetchError{Code: "parse_failed", HTTPStatus: resp.StatusCode, Err: err}
	}
	result.Items = items
	result.SourceTitle = title
	result.SourceCoverURL = coverURL
	result.Duration = time.Since(startedAt)
	return result, nil
}

func rssFetchErrorCode(statusCode int) string {
	switch statusCode {
	case http.StatusUnauthorized:
		return "http_401"
	case http.StatusForbidden:
		return "http_403"
	case http.StatusTooManyRequests:
		return "http_429"
	default:
		return "http_status"
	}
}

func parseRSSDocument(bodyStr, feedURL string) ([]ExtRSSItem, string, string, error) {
	// Try RSS first.
	var parsedRSS ExtRSS
	if err := xml.Unmarshal([]byte(bodyStr), &parsedRSS); err == nil && parsedRSS.Channel.Title != "" {
		feedLanguage := firstNonEmpty(parsedRSS.Channel.Language, parsedRSS.Channel.XMLLanguage)
		for index := range parsedRSS.Channel.Items {
			if strings.TrimSpace(parsedRSS.Channel.Items[index].Language) == "" {
				parsedRSS.Channel.Items[index].Language = feedLanguage
			}
		}
		coverURL := firstNonEmpty(
			parsedRSS.Channel.ITunesImage.Href,
			parsedRSS.Channel.MediaContent.URL,
			parsedRSS.Channel.MediaThumbnail.URL,
			parsedRSS.Channel.Image.URL,
		)
		if parsedURL, parseErr := url.Parse(feedURL); parseErr == nil {
			coverURL = resolveFeedImageURL(coverURL, parsedURL)
		}
		return parsedRSS.Channel.Items, parsedRSS.Channel.Title, coverURL, nil
	}

	// Try Atom.
	var parsedAtom ExtAtom
	if err := xml.Unmarshal([]byte(bodyStr), &parsedAtom); err == nil {
		feedImageURL := firstNonEmpty(parsedAtom.Logo, parsedAtom.Icon)
		if parsedURL, parseErr := url.Parse(feedURL); parseErr == nil {
			feedImageURL = resolveFeedImageURL(feedImageURL, parsedURL)
		}
		items := make([]ExtRSSItem, len(parsedAtom.Entries))
		for i, entry := range parsedAtom.Entries {
			normalized := normalizeAtomEntry(entry, strings.TrimSpace(parsedAtom.Title), feedImageURL, time.Time{})

			items[i] = ExtRSSItem{
				Title:       normalized.Title,
				Language:    firstNonEmpty(entry.Language, parsedAtom.Language),
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
