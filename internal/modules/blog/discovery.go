package blog

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	blogSearchSortRelevance = "relevance"
	blogSearchSortRecent    = "recent"
	blogDigestPeriodDay     = "day"
	blogDigestPeriodWeek    = "week"
)

type BlogSearchQuery struct {
	Text         string
	AuthorID     *uuid.UUID
	ChannelID    *uuid.UUID
	CollectionID *uuid.UUID
	Sort         string
	Page         int
	PageSize     int
}

type BlogSearchResultDTO struct {
	ID           uuid.UUID                 `json:"id"`
	Title        string                    `json:"title"`
	Summary      string                    `json:"summary"`
	Snippet      string                    `json:"snippet"`
	MatchField   string                    `json:"match_field"`
	CoverURL     string                    `json:"cover_url"`
	PublishedAt  *time.Time                `json:"published_at,omitempty"`
	CreatedAt    time.Time                 `json:"created_at"`
	Channel      *RecommendationChannelDTO `json:"channel,omitempty"`
	User         *RecommendationAuthorDTO  `json:"user,omitempty"`
	CollectionID *uuid.UUID                `json:"collection_id,omitempty"`
	TargetPath   string                    `json:"target_path"`
}

type BlogDigestItemDTO struct {
	ID          uuid.UUID                 `json:"id"`
	Title       string                    `json:"title"`
	Summary     string                    `json:"summary"`
	CoverURL    string                    `json:"cover_url"`
	PublishedAt *time.Time                `json:"published_at,omitempty"`
	Channel     *RecommendationChannelDTO `json:"channel,omitempty"`
	TargetPath  string                    `json:"target_path"`
}

type BlogDigestDTO struct {
	Period      string              `json:"period"`
	PeriodStart time.Time           `json:"period_start"`
	PeriodEnd   time.Time           `json:"period_end"`
	Total       int64               `json:"total"`
	Items       []BlogDigestItemDTO `json:"items"`
}

func normalizeBlogSearchSort(raw string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", blogSearchSortRelevance:
		return blogSearchSortRelevance, nil
	case blogSearchSortRecent:
		return blogSearchSortRecent, nil
	default:
		return "", apperr.BadRequest("validation.invalid_request", "sort must be one of relevance, recent")
	}
}

func normalizeBlogDigestPeriod(raw string) (string, time.Duration, error) {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", blogDigestPeriodWeek:
		return blogDigestPeriodWeek, 7 * 24 * time.Hour, nil
	case blogDigestPeriodDay:
		return blogDigestPeriodDay, 24 * time.Hour, nil
	default:
		return "", 0, apperr.BadRequest("validation.invalid_request", "period must be one of day, week")
	}
}

func (s *Service) SearchPublishedBlogContents(input BlogSearchQuery) ([]BlogSearchResultDTO, int64, error) {
	text := strings.TrimSpace(input.Text)
	if text == "" {
		return []BlogSearchResultDTO{}, 0, nil
	}
	page, pageSize := normalizeBlogSearchPage(input.Page, input.PageSize)
	sortMode, err := normalizeBlogSearchSort(input.Sort)
	if err != nil {
		return nil, 0, err
	}
	newQuery := func() *gorm.DB {
		query := canonicalBlogPostsQuery(s.db).
			Where("posts.status = ? AND (posts.visibility = ? OR posts.visibility = ?)", "published", "", "public")
		if input.AuthorID != nil {
			query = query.Where("posts.author_id = ?", *input.AuthorID)
		}
		if input.ChannelID != nil {
			query = query.Where("posts.channel_id = ?", *input.ChannelID)
		}
		if input.CollectionID != nil {
			query = query.Where("memberships.collection_id = ?", *input.CollectionID)
		}
		return applyBlogSearchFilter(query, text)
	}

	var total int64
	if err := newQuery().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return []BlogSearchResultDTO{}, 0, nil
	}

	var rows []canonicalBlogPostRow
	query := blogSearchOrder(newQuery(), text, sortMode)
	if err := query.Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	contents, err := hydrateCanonicalBlogContents(s.db, rows)
	if err != nil {
		return nil, 0, err
	}
	items := make([]BlogSearchResultDTO, 0, len(contents))
	for _, content := range contents {
		snippet, matchField := blogSearchSnippet(content, text)
		items = append(items, BlogSearchResultDTO{
			ID: content.ID, Title: content.Title, Summary: content.Summary, Snippet: snippet, MatchField: matchField,
			CoverURL: content.CoverURL, PublishedAt: content.PublishedAt, CreatedAt: content.CreatedAt,
			Channel: recommendationChannel(content.Channel), User: recommendationAuthor(content.User),
			CollectionID: content.CollectionID, TargetPath: "/posts/post/" + content.ID.String(),
		})
	}
	return items, total, nil
}

func normalizeBlogSearchPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func applyBlogSearchFilter(query *gorm.DB, text string) *gorm.DB {
	pattern := "%" + escapeBlogSearchLike(text) + "%"
	if query.Dialector.Name() == "postgres" || query.Dialector.Name() == "pgx" {
		return query.Where(`(
			(`+blogSearchEntryVector+`) @@ websearch_to_tsquery('simple', ?) OR
			(`+blogSearchContentVector+`) @@ websearch_to_tsquery('simple', ?) OR
			LOWER(posts.title) LIKE LOWER(?) ESCAPE '\' OR
			LOWER(posts.summary) LIKE LOWER(?) ESCAPE '\' OR
			LOWER(blog_extensions.content) LIKE LOWER(?) ESCAPE '\'
		)`, text, text, pattern, pattern, pattern)
	}
	return query.Where(`(
		LOWER(posts.title) LIKE ? ESCAPE '\' OR
		LOWER(posts.summary) LIKE ? ESCAPE '\' OR
		LOWER(blog_extensions.content) LIKE ? ESCAPE '\'
	)`, strings.ToLower(pattern), strings.ToLower(pattern), strings.ToLower(pattern))
}

const blogSearchEntryVector = `
	to_tsvector('simple', COALESCE(posts.title, '') || ' ' || COALESCE(posts.summary, ''))
`

const blogSearchContentVector = `
	to_tsvector('simple', COALESCE(blog_extensions.content, ''))
`

func blogSearchOrder(query *gorm.DB, text, sortMode string) *gorm.DB {
	if sortMode == blogSearchSortRecent {
		return query.Order("COALESCE(posts.published_at, posts.created_at) DESC, posts.id DESC")
	}
	pattern := "%" + escapeBlogSearchLike(text) + "%"
	prefix := escapeBlogSearchLike(text) + "%"
	if query.Dialector.Name() == "postgres" || query.Dialector.Name() == "pgx" {
		return query.Order(clause.OrderBy{Expression: clause.Expr{
			SQL: `(
				ts_rank((` + blogSearchEntryVector + `), websearch_to_tsquery('simple', ?)) +
				ts_rank((` + blogSearchContentVector + `), websearch_to_tsquery('simple', ?)) +
				CASE WHEN LOWER(posts.title) = LOWER(?) THEN 4.0 ELSE 0 END +
				CASE WHEN LOWER(posts.title) LIKE LOWER(?) ESCAPE '\' THEN 2.0 ELSE 0 END +
				CASE WHEN LOWER(posts.summary) LIKE LOWER(?) ESCAPE '\' THEN 1.0 ELSE 0 END +
				CASE WHEN LOWER(blog_extensions.content) LIKE LOWER(?) ESCAPE '\' THEN 0.5 ELSE 0 END
			) DESC, COALESCE(posts.published_at, posts.created_at) DESC, posts.id DESC`,
			Vars: []any{text, text, text, prefix, pattern, pattern}, WithoutParentheses: true,
		}})
	}
	return query.Order(clause.OrderBy{Expression: clause.Expr{
		SQL: `CASE
			WHEN LOWER(posts.title) = LOWER(?) THEN 0
			WHEN LOWER(posts.title) LIKE LOWER(?) ESCAPE '\' THEN 1
			WHEN LOWER(posts.summary) LIKE LOWER(?) ESCAPE '\' THEN 2
			ELSE 3
		END, COALESCE(posts.published_at, posts.created_at) DESC, posts.id DESC`,
		Vars: []any{text, prefix, pattern}, WithoutParentheses: true,
	}})
}

func escapeBlogSearchLike(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "%", "\\%")
	return strings.ReplaceAll(value, "_", "\\_")
}

func blogSearchSnippet(content BlogContent, query string) (string, string) {
	needle := strings.ToLower(strings.TrimSpace(query))
	for _, candidate := range []struct {
		name  string
		value string
	}{
		{name: "title", value: content.Title},
		{name: "summary", value: content.Summary},
		{name: "content", value: content.Content},
	} {
		plain := compactBlogSearchText(candidate.value)
		if plain == "" {
			continue
		}
		if strings.Contains(strings.ToLower(plain), needle) {
			return excerptBlogSearchText(plain, needle), candidate.name
		}
	}
	if summary := compactBlogSearchText(content.Summary); summary != "" {
		return excerptBlogSearchText(summary, ""), "summary"
	}
	return excerptBlogSearchText(compactBlogSearchText(content.Content), ""), "content"
}

func compactBlogSearchText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func excerptBlogSearchText(value, needle string) string {
	const radius = 96
	if value == "" {
		return ""
	}
	runes := []rune(value)
	start, end := 0, len(runes)
	if needle != "" {
		if byteIndex := strings.Index(strings.ToLower(value), needle); byteIndex >= 0 {
			matchRune := utf8.RuneCountInString(value[:byteIndex])
			start = matchRune - radius/2
			if start < 0 {
				start = 0
			}
			end = start + radius
		}
	}
	if end > len(runes) {
		end = len(runes)
	}
	if end-start > radius {
		end = start + radius
	}
	result := string(runes[start:end])
	var builder strings.Builder
	builder.Grow(len(result) + 6)
	if start > 0 {
		builder.WriteString("...")
	}
	builder.WriteString(result)
	if end < len(runes) {
		builder.WriteString("...")
	}
	return builder.String()
}

func (s *Service) HideBlogRecommendation(userID, contentID uuid.UUID) error {
	var feedback model.BlogRecommendationFeedback
	err := s.db.Where("user_id = ? AND content_id = ? AND action = ?", userID, contentID, "hide").First(&feedback).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return s.db.Create(&model.BlogRecommendationFeedback{UserID: userID, ContentID: contentID, Action: "hide"}).Error
}

func (s *Service) RestoreBlogRecommendation(userID, contentID uuid.UUID) error {
	return s.db.Where("user_id = ? AND content_id = ? AND action = ?", userID, contentID, "hide").Delete(&model.BlogRecommendationFeedback{}).Error
}

func (s *Service) BlogDigest(userID uuid.UUID, rawPeriod string) (BlogDigestDTO, error) {
	period, duration, err := normalizeBlogDigestPeriod(rawPeriod)
	if err != nil {
		return BlogDigestDTO{}, err
	}
	now := time.Now().UTC()
	result := BlogDigestDTO{Period: period, PeriodStart: now.Add(-duration), PeriodEnd: now, Items: []BlogDigestItemDTO{}}

	var channelIDs []uuid.UUID
	if err := s.db.Table("feed_sources").Select("feed_sources.source_id").
		Joins("JOIN subscriptions ON subscriptions.feed_source_id = feed_sources.id").
		Where("subscriptions.user_id = ? AND feed_sources.source_type = ?", userID, "internal_channel").
		Where("subscriptions.deleted_at IS NULL AND feed_sources.deleted_at IS NULL AND subscriptions.is_muted = false AND subscriptions.is_paused = false").
		Scan(&channelIDs).Error; err != nil {
		return BlogDigestDTO{}, err
	}
	channelIDs = dedupeUUIDs(channelIDs)
	if len(channelIDs) == 0 {
		return result, nil
	}

	newQuery := func() *gorm.DB {
		return canonicalBlogPostsQuery(s.db).
			Where("posts.status = ? AND (posts.visibility = ? OR posts.visibility = ?)", "published", "", "public").
			Where("posts.channel_id IN ?", channelIDs).
			Where("COALESCE(posts.published_at, posts.created_at) >= ?", result.PeriodStart)
	}
	if err := newQuery().Count(&result.Total).Error; err != nil {
		return BlogDigestDTO{}, err
	}
	if result.Total == 0 {
		return result, nil
	}
	var rows []canonicalBlogPostRow
	if err := newQuery().Order("COALESCE(posts.published_at, posts.created_at) DESC, posts.id DESC").Limit(5).Find(&rows).Error; err != nil {
		return BlogDigestDTO{}, err
	}
	contents, err := hydrateCanonicalBlogContents(s.db, rows)
	if err != nil {
		return BlogDigestDTO{}, err
	}
	for _, content := range contents {
		result.Items = append(result.Items, BlogDigestItemDTO{
			ID: content.ID, Title: content.Title, Summary: content.Summary, CoverURL: content.CoverURL,
			PublishedAt: content.PublishedAt, Channel: recommendationChannel(content.Channel), TargetPath: "/posts/post/" + content.ID.String(),
		})
	}
	return result, nil
}
