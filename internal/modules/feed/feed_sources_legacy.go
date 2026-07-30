package feed

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"atoman/internal/model"
	"atoman/internal/service"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var syncFeedSource = func(db *gorm.DB, source model.FeedSource) {
	go service.SyncSingleRSS(db, source)
}

func normalizeCanonicalFeedURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimRight(trimmed, "/")
	return trimmed
}

func buildFeedSourceHash(targetType string, targetID *uuid.UUID, rssURL string) string {
	var raw string
	if targetType == "external_rss" {
		raw = normalizeCanonicalFeedURL(rssURL)
	} else {
		raw = fmt.Sprintf("%s:%s", targetType, targetID.String())
	}

	h := sha256.New()
	h.Write([]byte(raw))
	return hex.EncodeToString(h.Sum(nil))
}

func BuildFeedSourceHash(targetType string, targetID *uuid.UUID, rssURL string) string {
	return buildFeedSourceHash(targetType, targetID, rssURL)
}

func populateFeedSourceTitle(db *gorm.DB, source *model.FeedSource, fallbackTitle string) {
	if strings.TrimSpace(source.Title) != "" {
		return
	}

	switch source.SourceType {
	case "internal_user":
		if source.SourceID == nil {
			break
		}
		var user model.User
		if err := db.Where("uuid = ?", source.SourceID).First(&user).Error; err == nil {
			source.Title = user.Username
		}
	case "internal_channel":
		if source.SourceID == nil {
			break
		}
		var channel model.Channel
		if err := db.First(&channel, source.SourceID).Error; err == nil {
			source.Title = channel.Name
		}
	case "internal_collection":
		if source.SourceID == nil {
			break
		}
		var collection model.Collection
		if err := db.First(&collection, source.SourceID).Error; err == nil {
			source.Title = collection.Name
		}
	case "external_rss":
		if source.RssURL == "" {
			break
		}
		if _, sourceTitle, _, err := service.FetchAndParseRSS(source.RssURL); err == nil && strings.TrimSpace(sourceTitle) != "" {
			source.Title = sourceTitle
		}
	}

	if strings.TrimSpace(source.Title) == "" {
		source.Title = strings.TrimSpace(fallbackTitle)
	}
}

func findOrCreateFeedSource(db *gorm.DB, targetType string, targetID *uuid.UUID, rssURL, fallbackTitle, providerOverride string) (*model.FeedSource, error) {
	sourceHash := buildFeedSourceHash(targetType, targetID, rssURL)
	trimmedURL := strings.TrimSpace(rssURL)
	canonicalURL := ""
	provider := "internal"
	if targetType == "external_rss" {
		canonicalURL = normalizeCanonicalFeedURL(rssURL)
		provider = "rss"
		if strings.TrimSpace(providerOverride) != "" {
			provider = strings.TrimSpace(providerOverride)
		}
		if canonicalURL != "" {
			var existing model.FeedSource
			canonicalLookup := db.Where("canonical_url = ?", canonicalURL).Limit(1).Find(&existing)
			if canonicalLookup.Error != nil {
				return nil, canonicalLookup.Error
			}
			if canonicalLookup.RowsAffected > 0 {
				updates := map[string]any{}
				if strings.TrimSpace(existing.Provider) == "" || (provider == "rsshub" && strings.TrimSpace(existing.Provider) == "rss") {
					updates["provider"] = provider
					existing.Provider = provider
				}
				if strings.TrimSpace(existing.CanonicalURL) == "" {
					updates["canonical_url"] = canonicalURL
					existing.CanonicalURL = canonicalURL
				}
				if strings.TrimSpace(existing.RssURL) == "" && trimmedURL != "" {
					updates["rss_url"] = trimmedURL
					existing.RssURL = trimmedURL
				}
				if strings.TrimSpace(existing.Hash) == "" {
					updates["hash"] = sourceHash
					existing.Hash = sourceHash
				}
				if strings.TrimSpace(existing.Title) == "" {
					populateFeedSourceTitle(db, &existing, fallbackTitle)
					if strings.TrimSpace(existing.Title) != "" {
						updates["title"] = existing.Title
					}
				}
				if len(updates) > 0 {
					if err := db.Model(&existing).Updates(updates).Error; err != nil {
						return nil, err
					}
					if err := db.Where("id = ?", existing.ID).First(&existing).Error; err != nil {
						return nil, err
					}
				}
				return &existing, nil
			}

			legacyURLs := []string{trimmedURL, canonicalURL}
			if canonicalURL+"/" != "" {
				legacyURLs = append(legacyURLs, canonicalURL+"/")
			}
			legacyLookup := db.Where("source_type = ? AND (canonical_url = '' OR canonical_url IS NULL) AND rss_url IN ?", targetType, legacyURLs).
				Order("created_at ASC").
				Limit(1).
				Find(&existing)
			if legacyLookup.Error != nil {
				return nil, legacyLookup.Error
			}
			if legacyLookup.RowsAffected > 0 {
				updates := map[string]any{
					"provider":      provider,
					"canonical_url": canonicalURL,
					"hash":          sourceHash,
				}
				if trimmedURL != "" {
					updates["rss_url"] = trimmedURL
					existing.RssURL = trimmedURL
				}
				existing.Provider = provider
				existing.CanonicalURL = canonicalURL
				existing.Hash = sourceHash
				if strings.TrimSpace(existing.Title) == "" {
					populateFeedSourceTitle(db, &existing, fallbackTitle)
					if strings.TrimSpace(existing.Title) != "" {
						updates["title"] = existing.Title
					}
				}
				if err := db.Model(&existing).Updates(updates).Error; err != nil {
					return nil, err
				}
				if err := db.Where("id = ?", existing.ID).First(&existing).Error; err != nil {
					return nil, err
				}
				return &existing, nil
			}
		}
	}

	var source model.FeedSource
	hashLookup := db.Where("hash = ?", sourceHash).Limit(1).Find(&source)
	if hashLookup.Error != nil {
		return nil, hashLookup.Error
	}
	if hashLookup.RowsAffected > 0 {
		updates := map[string]any{}
		if strings.TrimSpace(source.SourceType) == "" {
			updates["source_type"] = targetType
		}
		if targetType == "external_rss" {
			if strings.TrimSpace(source.Provider) == "" {
				updates["provider"] = provider
			}
			if strings.TrimSpace(source.CanonicalURL) == "" && canonicalURL != "" {
				updates["canonical_url"] = canonicalURL
			}
			if strings.TrimSpace(source.RssURL) == "" && trimmedURL != "" {
				updates["rss_url"] = trimmedURL
			}
		} else if source.SourceID == nil && targetID != nil {
			updates["source_id"] = *targetID
		}
		if strings.TrimSpace(source.Hash) == "" {
			updates["hash"] = sourceHash
		}
		if strings.TrimSpace(source.Title) == "" {
			populateFeedSourceTitle(db, &source, fallbackTitle)
			if strings.TrimSpace(source.Title) != "" {
				updates["title"] = source.Title
			}
		}
		if len(updates) > 0 {
			if err := db.Model(&source).Updates(updates).Error; err != nil {
				return nil, err
			}
			if err := db.Where("id = ?", source.ID).First(&source).Error; err != nil {
				return nil, err
			}
		}
		return &source, nil
	}

	source = model.FeedSource{
		SourceType:      targetType,
		SourceID:        targetID,
		Provider:        provider,
		Category:        "blog",
		RssURL:          trimmedURL,
		CanonicalURL:    canonicalURL,
		SiteURL:         "",
		Hash:            sourceHash,
		HealthStatus:    "healthy",
		FullTextEnabled: service.DefaultFullTextEnabled(targetType),
	}
	populateFeedSourceTitle(db, &source, fallbackTitle)
	if err := db.Create(&source).Error; err != nil {
		var existing model.FeedSource
		if loadErr := db.Where("hash = ?", sourceHash).First(&existing).Error; loadErr == nil {
			return &existing, nil
		}
		if canonicalURL != "" {
			if loadErr := db.Where("canonical_url = ?", canonicalURL).First(&existing).Error; loadErr == nil {
				return &existing, nil
			}
		}
		return nil, err
	}
	return &source, nil
}

func FindOrCreateFeedSource(db *gorm.DB, targetType string, targetID *uuid.UUID, rssURL, fallbackTitle, providerOverride string) (*model.FeedSource, error) {
	return findOrCreateFeedSource(db, targetType, targetID, rssURL, fallbackTitle, providerOverride)
}

// internalRSSPattern 匹配 /api/feed/rss/:username、/api/v1/feed/rss/:username 及对应绝对 URL
var internalRSSPattern = regexp.MustCompile(`(?:^|/)api(?:/v1)?/feed/rss/([^/?#]+)$`)

// resolveInternalRSSURL 检测 URL 是否为站内 RSS 地址，如果是则返回对应用户的 UUID。
func resolveInternalRSSURL(db *gorm.DB, rawURL string) (uuid.UUID, error) {
	m := internalRSSPattern.FindStringSubmatch(rawURL)
	if len(m) < 2 {
		return uuid.UUID{}, fmt.Errorf("not an internal RSS URL")
	}
	username := m[1]
	var user model.User
	if err := db.Where("username = ?", username).First(&user).Error; err != nil {
		return uuid.UUID{}, err
	}
	return user.UUID, nil
}

func ResolveInternalRSSURL(db *gorm.DB, rawURL string) (uuid.UUID, error) {
	return resolveInternalRSSURL(db, rawURL)
}
