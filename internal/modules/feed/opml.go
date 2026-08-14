package feed

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type OPML struct {
	XMLName xml.Name `xml:"opml"`
	Version string   `xml:"version,attr,omitempty"`
	Head    OPMLHead `xml:"head"`
	Body    OPMLBody `xml:"body"`
}

func walkOPMLOutlines(outlines []OPMLOutline, visit func(OPMLOutline)) {
	for _, outline := range outlines {
		visit(outline)
		walkOPMLOutlines(outline.Outlines, visit)
	}
}

type OPMLHead struct {
	Title string `xml:"title,omitempty"`
}

type OPMLBody struct {
	Outlines []OPMLOutline `xml:"outline"`
}

type OPMLOutline struct {
	Text     string        `xml:"text,attr,omitempty"`
	Title    string        `xml:"title,attr,omitempty"`
	Type     string        `xml:"type,attr,omitempty"`
	XMLURL   string        `xml:"xmlUrl,attr,omitempty"`
	Outlines []OPMLOutline `xml:"outline,omitempty"`
}

type importedFeedSourceResult struct {
	Source   *model.FeedSource
	Imported bool
}

func importFeedSourceFromURL(db *gorm.DB, title, xmlURL string) (importedFeedSourceResult, error) {
	trimmedURL := strings.TrimSpace(xmlURL)
	u, err := url.ParseRequestURI(trimmedURL)
	if err != nil || u == nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") {
		return importedFeedSourceResult{}, fmt.Errorf("feed url must be an absolute http/https URL")
	}
	sourceHash := buildFeedSourceHash("external_rss", nil, trimmedURL)
	canonicalURL := normalizeCanonicalFeedURL(trimmedURL)

	var existing model.FeedSource
	if err := db.Where("hash = ?", sourceHash).First(&existing).Error; err == nil {
		return importedFeedSourceResult{Source: &existing, Imported: false}, nil
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return importedFeedSourceResult{}, err
	}

	if canonicalURL != "" {
		if err := db.Where("canonical_url = ?", canonicalURL).First(&existing).Error; err == nil {
			feedSource, err := findOrCreateFeedSource(db, "external_rss", nil, trimmedURL, title, "")
			if err != nil {
				return importedFeedSourceResult{}, err
			}
			return importedFeedSourceResult{Source: feedSource, Imported: false}, nil
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return importedFeedSourceResult{}, err
		}
	}

	legacyURLs := []string{}
	for _, candidate := range []string{trimmedURL, canonicalURL, canonicalURL + "/"} {
		if candidate == "" {
			continue
		}
		duplicate := false
		for _, existingURL := range legacyURLs {
			if existingURL == candidate {
				duplicate = true
				break
			}
		}
		if !duplicate {
			legacyURLs = append(legacyURLs, candidate)
		}
	}
	if len(legacyURLs) > 0 {
		if err := db.Where("source_type = ? AND (canonical_url = '' OR canonical_url IS NULL) AND rss_url IN ?", "external_rss", legacyURLs).
			Order("created_at ASC").
			First(&existing).Error; err == nil {
			feedSource, err := findOrCreateFeedSource(db, "external_rss", nil, trimmedURL, title, "")
			if err != nil {
				return importedFeedSourceResult{}, err
			}
			return importedFeedSourceResult{Source: feedSource, Imported: false}, nil
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return importedFeedSourceResult{}, err
		}
	}

	feedSource, err := findOrCreateFeedSource(db, "external_rss", nil, trimmedURL, title, "")
	if err != nil {
		return importedFeedSourceResult{}, err
	}

	return importedFeedSourceResult{Source: feedSource, Imported: true}, nil
}

type userOPMLImportResult struct {
	Imported bool
}

func importFeedFromURL(db *gorm.DB, userID uuid.UUID, title, xmlURL string, groupID *uuid.UUID) (userOPMLImportResult, error) {
	if groupID == nil {
		defaultGroup, err := getOrCreateDefaultSubscriptionGroup(db, userID)
		if err != nil {
			return userOPMLImportResult{}, err
		}
		groupID = &defaultGroup.ID
	}

	result, err := importFeedSourceFromURL(db, title, xmlURL)
	if err != nil {
		return userOPMLImportResult{}, err
	}
	feedSource := result.Source

	var existingSub model.Subscription
	if err := db.Where("user_id = ? AND feed_source_id = ?", userID, feedSource.ID).First(&existingSub).Error; err == nil {
		return userOPMLImportResult{Imported: false}, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return userOPMLImportResult{}, err
	}

	subscription := model.Subscription{
		UserID:              userID,
		FeedSourceID:        feedSource.ID,
		Title:               title,
		SubscriptionGroupID: groupID,
		Position:            nextSubscriptionPosition(db, userID, groupID),
	}

	if err := db.Create(&subscription).Error; err != nil {
		return userOPMLImportResult{}, err
	}
	applySubscriptionRulesForSubscription(db, subscription)

	syncFeedSource(db, *feedSource)
	return userOPMLImportResult{Imported: true}, nil
}

func findOrCreateOPMLGroup(db *gorm.DB, userID uuid.UUID, name string) (*model.SubscriptionGroup, error) {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" {
		return nil, nil
	}
	group := model.SubscriptionGroup{UserID: userID, Name: trimmedName}
	if err := db.Where("user_id = ? AND name = ?", userID, trimmedName).FirstOrCreate(&group).Error; err != nil {
		return nil, err
	}
	return &group, nil
}

// ImportOPML godoc
// @Summary 导入 OPML
// @Description 上传 OPML 文件并批量导入 RSS 订阅。
// @Tags feed
// @Accept mpfd
// @Produce json
// @Param file formData file true "OPML 文件"
// @Success 200 {object} OPMLImportResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/opml/import [post]
func ImportOPML(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		file, header, err := c.Request.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to get uploaded file"})
			return
		}
		defer file.Close()

		if header.Size > 10<<20 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "File size exceeds 10MB limit"})
			return
		}

		data := make([]byte, header.Size)
		if _, err := file.Read(data); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read file"})
			return
		}

		var opml OPML
		if err := xml.Unmarshal(data, &opml); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid OPML format"})
			return
		}

		imported := 0
		reused := 0
		failed := 0
		failedSources := []gin.H{}

		var importOutlines func([]OPMLOutline, *uuid.UUID, string)
		importOutlines = func(outlines []OPMLOutline, groupID *uuid.UUID, groupName string) {
			for _, outline := range outlines {
				currentGroupID := groupID
				currentGroupName := groupName
				if strings.TrimSpace(outline.XMLURL) == "" && len(outline.Outlines) > 0 {
					candidateName := strings.TrimSpace(outline.Title)
					if candidateName == "" {
						candidateName = strings.TrimSpace(outline.Text)
					}
					group, groupErr := findOrCreateOPMLGroup(db, userID, candidateName)
					if groupErr != nil {
						failed += len(outline.Outlines)
						for _, child := range outline.Outlines {
							failedSources = append(failedSources, gin.H{
								"url": child.XMLURL, "title": child.Title, "group": candidateName, "reason": groupErr.Error(),
							})
						}
						continue
					}
					if group != nil {
						currentGroupID = &group.ID
						currentGroupName = group.Name
					}
				}

				if strings.TrimSpace(outline.XMLURL) != "" {
					title := strings.TrimSpace(outline.Title)
					if title == "" {
						title = strings.TrimSpace(outline.Text)
					}
					result, importErr := importFeedFromURL(db, userID, title, outline.XMLURL, currentGroupID)
					if importErr != nil {
						failed++
						failedSources = append(failedSources, gin.H{
							"url": outline.XMLURL, "title": title, "group": currentGroupName, "reason": importErr.Error(),
						})
					} else if result.Imported {
						imported++
					} else {
						reused++
					}
				}

				importOutlines(outline.Outlines, currentGroupID, currentGroupName)
			}
		}
		importOutlines(opml.Body.Outlines, nil, "")

		c.JSON(http.StatusOK, gin.H{
			"message":        "OPML import completed",
			"imported":       imported,
			"reused":         reused,
			"failed":         failed,
			"failed_sources": failedSources,
		})
	}
}

// ImportGlobalOPML godoc
// @Summary 导入全局 OPML 订阅源
// @Description 管理员上传 OPML 文件，批量创建或复用全局 RSS 源，不创建用户订阅。
// @Tags feed
// @Accept mpfd
// @Produce json
// @Param file formData file true "OPML 文件"
// @Success 200 {object} OPMLImportResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/sources/opml/import [post]
func ImportGlobalOPML(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		file, header, err := c.Request.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to get uploaded file"})
			return
		}
		defer file.Close()

		if header.Size > 10<<20 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "File size exceeds 10MB limit"})
			return
		}

		data, err := io.ReadAll(file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read file"})
			return
		}

		var opml OPML
		if err := xml.Unmarshal(data, &opml); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid OPML format"})
			return
		}

		imported := 0
		reused := 0
		failed := 0
		failedSources := []gin.H{}

		importOutline := func(outline OPMLOutline) {
			if strings.TrimSpace(outline.XMLURL) == "" {
				return
			}
			result, err := importFeedSourceFromURL(db, strings.TrimSpace(outline.Text), strings.TrimSpace(outline.XMLURL))
			if err != nil {
				failed++
				failedSources = append(failedSources, gin.H{"url": outline.XMLURL, "reason": err.Error()})
				return
			}
			if result.Imported {
				imported++
			} else {
				reused++
			}
			if result.Source != nil {
				go syncFeedSource(db, *result.Source)
			}
		}

		walkOPMLOutlines(opml.Body.Outlines, importOutline)

		c.JSON(http.StatusOK, gin.H{
			"message":        "OPML import completed",
			"imported":       imported,
			"reused":         reused,
			"failed":         failed,
			"failed_sources": failedSources,
		})
	}
}

// RetryGlobalFeedSource retries one failed global OPML source without requiring the original file.
func RetryGlobalFeedSource(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input struct {
			Title string `json:"title"`
			URL   string `json:"url" binding:"required"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}

		result, err := importFeedSourceFromURL(db, strings.TrimSpace(input.Title), strings.TrimSpace(input.URL))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if result.Source != nil {
			go syncFeedSource(db, *result.Source)
		}
		c.JSON(http.StatusOK, gin.H{"imported": result.Imported, "reused": !result.Imported})
	}
}

// ExportGlobalOPML godoc
// @Summary 导出全局 OPML 订阅源
// @Description 管理员导出全站 external_rss 订阅源为 OPML 文件，不包含用户订阅关系。
// @Tags feed
// @Produce application/x-opml+xml
// @Success 200 {string} string "OPML XML"
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/sources/opml/export [get]
func ExportGlobalOPML(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var sources []model.FeedSource
		if err := db.
			Where("source_type = ? AND COALESCE(rss_url, '') <> ?", "external_rss", "").
			Order("title ASC, created_at ASC").
			Find(&sources).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch feed sources"})
			return
		}

		opml := OPML{
			Version: "2.0",
			Head: OPMLHead{
				Title: "Atoman Feed Sources",
			},
		}

		for _, source := range sources {
			title := strings.TrimSpace(source.Title)
			if title == "" {
				title = strings.TrimSpace(source.RssURL)
			}
			opml.Body.Outlines = append(opml.Body.Outlines, OPMLOutline{
				Text:   title,
				Title:  title,
				Type:   "rss",
				XMLURL: source.RssURL,
			})
		}

		output, err := xml.MarshalIndent(opml, "", "  ")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate OPML"})
			return
		}

		c.Header("Content-Type", "application/x-opml+xml")
		c.Header("Content-Disposition", "attachment; filename=\"atoman-feed-sources.opml\"")
		c.Data(http.StatusOK, "application/x-opml+xml", output)
	}
}

// ExportOPML godoc
// @Summary 导出 OPML
// @Description 导出当前用户的订阅为 OPML 文件。
// @Tags feed
// @Produce application/x-opml+xml
// @Success 200 {string} string "OPML XML"
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/opml/export [get]
func ExportOPML(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var subscriptions []model.Subscription
		if err := db.Preload("FeedSource").Preload("SubscriptionGroup").Where("user_id = ?", userID).Find(&subscriptions).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch subscriptions"})
			return
		}

		opml := OPML{
			Version: "2.0",
			Head: OPMLHead{
				Title: "Atoman RSS Exports",
			},
		}

		grouped := make(map[uuid.UUID][]model.Subscription)
		var ungrouped []model.Subscription

		for _, sub := range subscriptions {
			if sub.SubscriptionGroupID != nil {
				grouped[*sub.SubscriptionGroupID] = append(grouped[*sub.SubscriptionGroupID], sub)
			} else {
				ungrouped = append(ungrouped, sub)
			}
		}

		for groupID, subs := range grouped {
			var group model.SubscriptionGroup
			if err := db.First(&group, groupID).Error; err != nil {
				continue
			}

			outline := OPMLOutline{
				Text:     group.Name,
				Title:    group.Name,
				Type:     "rss",
				Outlines: []OPMLOutline{},
			}

			for _, sub := range subs {
				if sub.FeedSource != nil {
					outline.Outlines = append(outline.Outlines, OPMLOutline{
						Text:   sub.FeedSource.Title,
						Title:  sub.FeedSource.Title,
						Type:   "rss",
						XMLURL: sub.FeedSource.RssURL,
					})
				}
			}
			opml.Body.Outlines = append(opml.Body.Outlines, outline)
		}

		// Add ungrouped subscriptions
		for _, sub := range ungrouped {
			if sub.FeedSource != nil {
				opml.Body.Outlines = append(opml.Body.Outlines, OPMLOutline{
					Text:   sub.FeedSource.Title,
					Title:  sub.FeedSource.Title,
					Type:   "rss",
					XMLURL: sub.FeedSource.RssURL,
				})
			}
		}

		output, err := xml.MarshalIndent(opml, "", "  ")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate OPML"})
			return
		}

		c.Header("Content-Type", "application/x-opml+xml")
		c.Header("Content-Disposition", "attachment; filename=\"atoman-export.opml\"")
		c.Data(http.StatusOK, "application/x-opml+xml", output)
	}
}
