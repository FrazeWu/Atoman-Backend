package blog

import (
	"regexp"
	"strings"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var markdownAssetURLPattern = regexp.MustCompile(`!\[[^\]]*\]\(\s*<?([^\s)>]+)`)

func (s *Service) syncBlogContentMediaAssets(tx *gorm.DB, contentID, userID uuid.UUID, coverURL, markdown string) error {
	if err := tx.Where("content_id = ?", contentID).Delete(&model.ContentMediaAsset{}).Error; err != nil {
		return err
	}
	urls := referencedBlogAssetURLs(coverURL, markdown)
	if len(urls) == 0 {
		return nil
	}
	var assets []model.MediaAsset
	if err := tx.Where("user_id = ? AND purpose = ? AND url IN ?", userID, "blog.image", urls).Find(&assets).Error; err != nil {
		return err
	}
	links := make([]model.ContentMediaAsset, 0, len(assets))
	for _, asset := range assets {
		links = append(links, model.ContentMediaAsset{ContentID: contentID, MediaAssetID: asset.ID})
	}
	if len(links) == 0 {
		return nil
	}
	return tx.Create(&links).Error
}

func referencedBlogAssetURLs(coverURL, markdown string) []string {
	seen := map[string]struct{}{}
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw != "" {
			seen[raw] = struct{}{}
		}
	}
	add(coverURL)
	for _, match := range markdownAssetURLPattern.FindAllStringSubmatch(markdown, -1) {
		if len(match) > 1 {
			add(match[1])
		}
	}
	urls := make([]string, 0, len(seen))
	for url := range seen {
		urls = append(urls, url)
	}
	return urls
}
