package forum

import (
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"gorm.io/gorm"
)

func (r *Repo) SearchTopics(user authctx.CurrentUser, query ListTopicsQuery) ([]model.ForumTopic, int64, error) {
	search := strings.TrimSpace(query.Search)
	if search == "" {
		return []model.ForumTopic{}, 0, nil
	}

	db := r.visibleCategories(r.db.Model(&model.ForumTopic{}), user, "forum_topics.category_id")
	switch r.db.Dialector.Name() {
	case "postgres", "pgx":
		db = applyPostgresTopicSearch(db, search)
	default:
		db = applySQLiteTopicSearch(db, search)
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var topics []model.ForumTopic
	err := db.Preload("User.ForumTrust").Preload("Category").
		Offset(offset(query.Page, query.PageSize)).
		Limit(normalizedPageSize(query.PageSize)).
		Find(&topics).Error
	attachTopicTrustLevels(topics)
	return topics, total, err
}

func applySQLiteTopicSearch(db *gorm.DB, search string) *gorm.DB {
	pattern := "%" + escapeLike(strings.ToLower(search)) + "%"
	const predicate = `(
		LOWER(forum_topics.title) LIKE ? ESCAPE '\' OR
		LOWER(forum_topics.content) LIKE ? ESCAPE '\' OR
		LOWER(forum_topics.tags) LIKE ? ESCAPE '\' OR
		EXISTS (
			SELECT 1 FROM forum_categories fc
			WHERE fc.id = forum_topics.category_id AND fc.deleted_at IS NULL
				AND LOWER(fc.name) LIKE ? ESCAPE '\'
		) OR
		EXISTS (
			SELECT 1 FROM "Users" topic_user
			WHERE topic_user.uuid = forum_topics.user_id AND topic_user.deleted_at IS NULL
				AND LOWER(COALESCE(topic_user.username, '') || ' ' || COALESCE(topic_user.display_name, '')) LIKE ? ESCAPE '\'
		) OR
		EXISTS (
			SELECT 1 FROM discussion_targets dt
			JOIN comment_entries ce ON ce.target_id = dt.id AND ce.deleted_at IS NULL AND ce.status IN ('active', 'auto_folded')
			LEFT JOIN "Users" comment_user ON comment_user.uuid = ce.author_id AND comment_user.deleted_at IS NULL
			WHERE dt.kind = 'forum_topic' AND dt.resource_id = forum_topics.id AND dt.deleted_at IS NULL
				AND (LOWER(ce.content) LIKE ? ESCAPE '\' OR LOWER(COALESCE(comment_user.username, '') || ' ' || COALESCE(comment_user.display_name, '')) LIKE ? ESCAPE '\')
		)
	)`

	return db.Where(predicate, pattern, pattern, pattern, pattern, pattern, pattern, pattern).
		Order(forumLastCommentAtExpr + " DESC, forum_topics.created_at DESC, forum_topics.id DESC")
}

func applyPostgresTopicSearch(db *gorm.DB, search string) *gorm.DB {
	pattern := "%" + escapeLike(search) + "%"
	const topicVector = `(
		setweight(to_tsvector('simple', COALESCE(forum_topics.title, '')), 'A') ||
		setweight(to_tsvector('simple', concat_ws(' ',
			COALESCE((SELECT fc.name FROM forum_categories fc WHERE fc.id = forum_topics.category_id AND fc.deleted_at IS NULL), ''),
			COALESCE((SELECT concat_ws(' ', topic_user.username, topic_user.display_name) FROM "Users" topic_user WHERE topic_user.uuid = forum_topics.user_id AND topic_user.deleted_at IS NULL), ''),
			COALESCE(forum_topics.tags, '')
		)), 'B') ||
		setweight(to_tsvector('simple', COALESCE(forum_topics.content, '')), 'C')
	)`
	const rank = `(
		ts_rank(` + topicVector + `, websearch_to_tsquery('simple', ?)) +
		CASE WHEN forum_topics.title ILIKE ? ESCAPE '\' THEN 1.0 ELSE 0 END +
		CASE WHEN COALESCE((SELECT fc.name FROM forum_categories fc WHERE fc.id = forum_topics.category_id AND fc.deleted_at IS NULL), '') ILIKE ? ESCAPE '\' THEN 0.5 ELSE 0 END +
		CASE WHEN COALESCE((SELECT concat_ws(' ', topic_user.username, topic_user.display_name) FROM "Users" topic_user WHERE topic_user.uuid = forum_topics.user_id AND topic_user.deleted_at IS NULL), '') ILIKE ? ESCAPE '\' THEN 0.5 ELSE 0 END +
		CASE WHEN forum_topics.tags ILIKE ? ESCAPE '\' THEN 0.5 ELSE 0 END +
		CASE WHEN forum_topics.content ILIKE ? ESCAPE '\' THEN 0.25 ELSE 0 END +
		COALESCE((
			SELECT MAX(
					ts_rank(
						setweight(to_tsvector('simple', concat_ws(' ', comment_user.username, comment_user.display_name)), 'B') ||
						setweight(to_tsvector('simple', COALESCE(ce.content, '')), 'C'),
						websearch_to_tsquery('simple', ?)
					) +
				CASE WHEN concat_ws(' ', comment_user.username, comment_user.display_name) ILIKE ? ESCAPE '\' THEN 0.5 ELSE 0 END +
				CASE WHEN ce.content ILIKE ? ESCAPE '\' THEN 0.25 ELSE 0 END
			)
			FROM discussion_targets dt
			JOIN comment_entries ce ON ce.target_id = dt.id AND ce.deleted_at IS NULL AND ce.status IN ('active', 'auto_folded')
			LEFT JOIN "Users" comment_user ON comment_user.uuid = ce.author_id AND comment_user.deleted_at IS NULL
			WHERE dt.kind = 'forum_topic' AND dt.resource_id = forum_topics.id AND dt.deleted_at IS NULL
		), 0)
	)`
	const predicate = `(
		to_tsvector('simple', COALESCE(forum_topics.title, '') || ' ' || COALESCE(forum_topics.content, '')) @@ websearch_to_tsquery('simple', ?) OR
		to_tsvector('simple', COALESCE(forum_topics.tags, '')) @@ websearch_to_tsquery('simple', ?) OR
		EXISTS (
			SELECT 1 FROM forum_categories fc
			WHERE fc.id = forum_topics.category_id AND fc.deleted_at IS NULL
				AND to_tsvector('simple', COALESCE(fc.name, '')) @@ websearch_to_tsquery('simple', ?)
		) OR
		EXISTS (
			SELECT 1 FROM "Users" topic_user
			WHERE topic_user.uuid = forum_topics.user_id AND topic_user.deleted_at IS NULL
				AND to_tsvector('simple', concat_ws(' ', topic_user.username, topic_user.display_name)) @@ websearch_to_tsquery('simple', ?)
		) OR
		EXISTS (
			SELECT 1 FROM discussion_targets dt
			JOIN comment_entries ce ON ce.target_id = dt.id AND ce.deleted_at IS NULL AND ce.status IN ('active', 'auto_folded')
			WHERE dt.kind = 'forum_topic' AND dt.resource_id = forum_topics.id AND dt.deleted_at IS NULL
				AND to_tsvector('simple', COALESCE(ce.content, '')) @@ websearch_to_tsquery('simple', ?)
		) OR
		EXISTS (
			SELECT 1 FROM discussion_targets dt
			JOIN comment_entries ce ON ce.target_id = dt.id AND ce.deleted_at IS NULL AND ce.status IN ('active', 'auto_folded')
			JOIN "Users" comment_user ON comment_user.uuid = ce.author_id AND comment_user.deleted_at IS NULL
			WHERE dt.kind = 'forum_topic' AND dt.resource_id = forum_topics.id AND dt.deleted_at IS NULL
				AND to_tsvector('simple', concat_ws(' ', comment_user.username, comment_user.display_name)) @@ websearch_to_tsquery('simple', ?)
		) OR
		forum_topics.title ILIKE ? ESCAPE '\' OR
		forum_topics.content ILIKE ? ESCAPE '\' OR
		forum_topics.tags ILIKE ? ESCAPE '\' OR
		EXISTS (
			SELECT 1 FROM forum_categories fc
			WHERE fc.id = forum_topics.category_id AND fc.deleted_at IS NULL
				AND fc.name ILIKE ? ESCAPE '\'
		) OR
		EXISTS (
			SELECT 1 FROM "Users" topic_user
			WHERE topic_user.uuid = forum_topics.user_id AND topic_user.deleted_at IS NULL
				AND concat_ws(' ', topic_user.username, topic_user.display_name) ILIKE ? ESCAPE '\'
		) OR
		EXISTS (
			SELECT 1 FROM discussion_targets dt
			JOIN comment_entries ce ON ce.target_id = dt.id AND ce.deleted_at IS NULL AND ce.status IN ('active', 'auto_folded')
			LEFT JOIN "Users" comment_user ON comment_user.uuid = ce.author_id AND comment_user.deleted_at IS NULL
			WHERE dt.kind = 'forum_topic' AND dt.resource_id = forum_topics.id AND dt.deleted_at IS NULL
				AND (ce.content ILIKE ? ESCAPE '\' OR concat_ws(' ', comment_user.username, comment_user.display_name) ILIKE ? ESCAPE '\')
		)
	)`

	return db.
		Select("forum_topics.*, "+rank+" AS search_rank",
			search, pattern, pattern, pattern, pattern, pattern, search, pattern, pattern,
		).
		Where(predicate,
			search, search, search, search, search, search,
			pattern, pattern, pattern, pattern, pattern, pattern, pattern,
		).
		Order("search_rank DESC, " + forumLastCommentAtExpr + " DESC, forum_topics.created_at DESC, forum_topics.id DESC")
}
