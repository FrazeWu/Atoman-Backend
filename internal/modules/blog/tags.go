package blog

import (
	"strings"
	"unicode/utf8"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const maxBlogTags = 5

func normalizeBlogTags(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	tags := make([]string, 0, len(values))
	for _, value := range values {
		tag := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
		if tag == "" {
			continue
		}
		if utf8.RuneCountInString(tag) > 48 {
			return nil, apperr.BadRequest("blog.invalid_tags", "each tag must be at most 48 characters")
		}
		if _, exists := seen[tag]; exists {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	if len(tags) > maxBlogTags {
		return nil, apperr.BadRequest("blog.invalid_tags", "a post can have at most 5 tags")
	}
	return tags, nil
}

func syncBlogTags(db *gorm.DB, contentID uuid.UUID, values []string) error {
	tags, err := normalizeBlogTags(values)
	if err != nil {
		return err
	}
	if err := db.Where("content_id = ?", contentID).Delete(&model.ContentBlogTag{}).Error; err != nil {
		return err
	}
	if len(tags) == 0 {
		return nil
	}
	rows := make([]model.ContentBlogTag, 0, len(tags))
	for _, tag := range tags {
		rows = append(rows, model.ContentBlogTag{ContentID: contentID, Name: tag})
	}
	return db.Create(&rows).Error
}

func hydrateBlogTags(db *gorm.DB, contents []BlogContent) error {
	ids := make([]uuid.UUID, 0, len(contents))
	for _, content := range contents {
		ids = append(ids, content.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	var rows []model.ContentBlogTag
	if err := db.Where("content_id IN ?", ids).Order("name ASC").Find(&rows).Error; err != nil {
		return err
	}
	byContent := make(map[uuid.UUID][]string, len(contents))
	for _, row := range rows {
		byContent[row.ContentID] = append(byContent[row.ContentID], row.Name)
	}
	for index := range contents {
		contents[index].Tags = byContent[contents[index].ID]
	}
	return nil
}
