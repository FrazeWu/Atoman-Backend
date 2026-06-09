package service

import (
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"atoman/internal/model"
)

// Simplified standard RSS Structures for parsing external feeds.
type ExtRSS struct {
	Channel ExtRSSChannel `xml:"channel"`
}

type ExtRSSChannel struct {
	Title       string               `xml:"title"`
	Items       []ExtRSSItem         `xml:"item"`
	ITunesImage ExtRSSITunesImageRef `xml:"http://www.itunes.com/dtds/podcast-1.0.dtd image"`
	Image       ExtRSSImageBlock     `xml:"image"`
}

type ExtRSSImageBlock struct {
	URL string `xml:"url"`
}

type ExtRSSITunesImageRef struct {
	Href string `xml:"href,attr"`
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
	Title       string               `xml:"title"`
	Link        string               `xml:"link"`
	Description string               `xml:"description"`
	PubDate     string               `xml:"pubDate"`
	Content     string               `xml:"encoded"`
	Creator     string               `xml:"creator"`
	Author      string               `xml:"author"`
	GUID        string               `xml:"guid"`
	Enclosure   ExtRSSEnclosure      `xml:"enclosure"`
	ITunesDur   string               `xml:"duration"`
	ITunesImage ExtRSSITunesImageRef `xml:"http://www.itunes.com/dtds/podcast-1.0.dtd image"`
}

// Atom Structures
type ExtAtom struct {
	XMLName xml.Name       `xml:"feed"`
	Title   string         `xml:"title"`
	Entries []ExtAtomEntry `xml:"entry"`
}

type ExtAtomEntry struct {
	Title     string        `xml:"title"`
	Links     []ExtAtomLink `xml:"link"`
	Summary   string        `xml:"summary"`
	Content   string        `xml:"content"`
	Published string        `xml:"published"`
	Updated   string        `xml:"updated"`
	ID        string        `xml:"id"`
	Author    ExtAtomAuthor `xml:"author"`
}

type ExtAtomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

var rssFetchHTTPClient = &http.Client{Timeout: 10 * time.Second}

type ExtAtomAuthor struct {
	Name string `xml:"name"`
}

// StartRSSCron starts a background worker that fetches all unique RSS URLs periodically
func StartRSSCron(db *gorm.DB) {
	go func() {
		// Wait a few seconds before starting the first sync to not block server startup
		time.Sleep(5 * time.Second)
		// Run immediately first
		log.Println("Starting initial RSS sync...")
		syncAllRSSFeeds(db)

		// Then run every 15 minutes
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()

		for {
			<-ticker.C
			log.Println("Running scheduled RSS sync...")
			syncAllRSSFeeds(db)
		}
	}()
}

func syncAllRSSFeeds(db *gorm.DB) {
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
			continue
		}
		// 跳过相对路径或非 http(s) URL（内部 RSS 端点误存为 external_rss 时的兜底保护）
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			log.Printf("RSS sync skipping non-absolute URL: %s", url)
			continue
		}

		// 2. Fetch and parse the feed
		items, sourceTitle, sourceCoverURL, err := FetchAndParseRSS(url)
		if err != nil {
			log.Printf("Failed to fetch RSS %s: %v", url, err)
			continue
		}

		// 3. Find all FeedSources (users subscribed) to this URL
		var sources []model.FeedSource
		if err := db.Where("source_type = ? AND rss_url = ?", "external_rss", url).Find(&sources).Error; err != nil {
			continue
		}

		now := time.Now()

		for _, src := range sources {
			// Update the feed title if it was empty
			if src.Title == "" && sourceTitle != "" {
				src.Title = sourceTitle
			}
			if sourceCoverURL != "" {
				src.CoverURL = sourceCoverURL
			}
			src.LastFetchedAt = &now
			db.Save(&src)

			// Store new items
			for _, item := range items {
				// Use GUID or Link as unique identifier
				identifier := item.GUID
				if identifier == "" {
					identifier = item.Link
				}
				if identifier == "" {
					continue // Skip invalid items
				}

				// Check if item already exists for this exact FeedSource
				var count int64
				db.Model(&model.FeedItem{}).Where("feed_source_id = ? AND guid = ?", src.ID, identifier).Count(&count)
				if count > 0 {
					continue // Already processed
				}

				// Parse dates
				pubDate := parseRSSDate(item.PubDate)
				if pubDate.IsZero() {
					pubDate = now
				}

				author := item.Author
				if author == "" {
					author = item.Creator
				}
				if author == "" {
					author = sourceTitle
				}

				summary := item.Description
				runes := []rune(summary)
				if len(runes) > 1000 {
					summary = string(runes[:1000])
				}

				// Insert item
				newFeedItem := model.FeedItem{
					FeedSourceID:  src.ID,
					GUID:          identifier,
					Title:         item.Title,
					Link:          item.Link,
					Summary:       summary,
					Author:        author,
					PublishedAt:   pubDate,
					FetchedAt:     now,
					EnclosureURL:  item.Enclosure.URL,
					EnclosureType: item.Enclosure.Type,
					Duration:      item.ITunesDur,
					ImageURL:      strings.TrimSpace(item.ITunesImage.Href),
				}
				newFeedItem.FullTextStatus = defaultFullTextStatusForSource(src, newFeedItem)
				db.Create(&newFeedItem)
			}
		}
	}
	log.Println("RSS sync completed")
}

func defaultFullTextStatusForSource(src model.FeedSource, item model.FeedItem) string {
	if !IsFeedItemEligibleForFullText(src, item) {
		return FullTextStatusDisabled
	}
	return FullTextStatusPending
}

func SyncSingleRSS(db *gorm.DB, src model.FeedSource) {
	if src.SourceType != "external_rss" || src.RssURL == "" {
		return
	}
	if !strings.HasPrefix(src.RssURL, "http://") && !strings.HasPrefix(src.RssURL, "https://") {
		log.Printf("SyncSingleRSS skipping non-absolute URL: %s", src.RssURL)
		return
	}

	items, sourceTitle, sourceCoverURL, err := FetchAndParseRSS(src.RssURL)
	if err != nil {
		log.Printf("Immediate RSS sync failed for %s: %v", src.RssURL, err)
		return
	}

	now := time.Now()

	// Update title if missing
	if src.Title == "" && sourceTitle != "" {
		src.Title = sourceTitle
		db.Model(&src).Update("title", sourceTitle)
	}
	if sourceCoverURL != "" {
		src.CoverURL = sourceCoverURL
		db.Model(&src).Update("cover_url", sourceCoverURL)
	}

	for _, item := range items {
		identifier := item.GUID
		if identifier == "" {
			identifier = item.Link
		}
		if identifier == "" {
			continue
		}

		var count int64
		db.Model(&model.FeedItem{}).Where("feed_source_id = ? AND guid = ?", src.ID, identifier).Count(&count)
		if count > 0 {
			continue
		}

		pubDate := parseRSSDate(item.PubDate)
		if pubDate.IsZero() {
			pubDate = now
		}

		author := item.Author
		if author == "" {
			author = item.Creator
		}
		if author == "" {
			author = sourceTitle
		}

		summary := item.Description
		runes := []rune(summary)
		if len(runes) > 1000 {
			summary = string(runes[:1000])
		}

		newFeedItem := model.FeedItem{
			FeedSourceID:  src.ID,
			GUID:          identifier,
			Title:         item.Title,
			Link:          item.Link,
			Summary:       summary,
			Author:        author,
			PublishedAt:   pubDate,
			FetchedAt:     now,
			EnclosureURL:  item.Enclosure.URL,
			EnclosureType: item.Enclosure.Type,
			Duration:      item.ITunesDur,
			ImageURL:      strings.TrimSpace(item.ITunesImage.Href),
		}
		newFeedItem.FullTextStatus = defaultFullTextStatusForSource(src, newFeedItem)
		db.Create(&newFeedItem)
	}
	db.Model(&src).Update("last_fetched_at", now)
}

func FetchAndParseRSS(feedURL string) ([]ExtRSSItem, string, string, error) {
	client := rssFetchHTTPClient
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
		coverURL := strings.TrimSpace(parsedRSS.Channel.ITunesImage.Href)
		if coverURL == "" {
			coverURL = strings.TrimSpace(parsedRSS.Channel.Image.URL)
		}
		return parsedRSS.Channel.Items, parsedRSS.Channel.Title, coverURL, nil
	}

	// Try Atom
	var parsedAtom ExtAtom
	if err := xml.Unmarshal([]byte(bodyStr), &parsedAtom); err == nil {
		items := make([]ExtRSSItem, len(parsedAtom.Entries))
		for i, entry := range parsedAtom.Entries {
			link := ""
			for _, l := range entry.Links {
				if l.Rel == "alternate" || l.Rel == "" {
					link = l.Href
					break
				}
			}
			if link == "" && len(entry.Links) > 0 {
				link = entry.Links[0].Href
			}

			date := entry.Published
			if date == "" {
				date = entry.Updated
			}

			desc := entry.Summary
			if desc == "" {
				desc = entry.Content
			}

			items[i] = ExtRSSItem{
				Title:       entry.Title,
				Link:        link,
				Description: desc,
				PubDate:     date,
				GUID:        entry.ID,
				Author:      entry.Author.Name,
			}
		}
		return items, parsedAtom.Title, "", nil
	}

	return nil, "", "", fmt.Errorf("failed to parse feed as RSS or Atom")
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
