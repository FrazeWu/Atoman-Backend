package service

import (
	"regexp"
	"strings"

	"gorm.io/gorm"

	"atoman/internal/model"
)

var (
	// codeBlockRe strips fenced code blocks and inline code to avoid false-positive @mentions
	codeBlockRe = regexp.MustCompile("(?s)```[\\s\\S]*?```|`[^`]+`")

	// plainMentionRe matches standalone @username patterns (2–32 chars: letters, digits, underscores, hyphens)
	plainMentionRe = regexp.MustCompile(`@([A-Za-z0-9_-]{2,32})`)

	// markdownMentionRe matches legacy markdown mentions like [@显示名](/user/username)
	markdownMentionRe = regexp.MustCompile(`\[@[^\]]*\]\(/user/([A-Za-z0-9_-]{2,32})\)`)
)

// ParseMentions extracts supported mention patterns from content, looks them up in the DB,
// and returns the matched User records. Code blocks are stripped first to avoid
// mentioning users inside code examples.
func ParseMentions(db *gorm.DB, content string) ([]model.User, error) {
	stripped := codeBlockRe.ReplaceAllString(content, "")
	usernames := extractMentionUsernames(stripped)
	if len(usernames) == 0 {
		return nil, nil
	}

	var users []model.User
	if err := db.Where("LOWER(username) IN ?", usernames).Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

func extractMentionUsernames(content string) []string {
	seen := make(map[string]struct{})
	usernames := make([]string, 0)

	for _, match := range markdownMentionRe.FindAllStringSubmatch(content, -1) {
		addMentionUsername(seen, &usernames, match[1])
	}

	plainContent := markdownMentionRe.ReplaceAllString(content, "")
	for _, match := range plainMentionRe.FindAllStringSubmatch(plainContent, -1) {
		addMentionUsername(seen, &usernames, match[1])
	}

	return usernames
}

func addMentionUsername(seen map[string]struct{}, usernames *[]string, raw string) {
	username := strings.ToLower(strings.TrimSpace(raw))
	if username == "" {
		return
	}
	if _, ok := seen[username]; ok {
		return
	}
	seen[username] = struct{}{}
	*usernames = append(*usernames, username)
}
