package service

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	forumTrustL1AgeDays       = 3
	forumTrustL1Contributions = 3
	forumTrustL1Likers        = 1
	forumTrustL2AgeDays       = 14
	forumTrustL2Contributions = 20
	forumTrustL2Likers        = 10
	forumTrustL3AgeDays       = 60
	forumTrustL3Contributions = 100
	forumTrustL3Likers        = 50

	forumTopicTitleMaxRunes   = 200
	forumTopicContentMaxRunes = 50000
	forumReplyContentMaxRunes = 20000
	forumDuplicateWindow      = 10 * time.Minute
)

var forumTopicDailyLimits = [...]int{3, 10, 30, 100}
var forumReplyHourlyLimits = [...]int{20, 60, 180, 600}

type ForumTrustService struct {
	db *gorm.DB
}

func NewForumTrustService(db *gorm.DB) *ForumTrustService {
	return &ForumTrustService{db: db}
}

func (s *ForumTrustService) WithDB(db *gorm.DB) *ForumTrustService {
	return &ForumTrustService{db: db}
}

func (s *ForumTrustService) Get(userID uuid.UUID) (model.ForumUserTrust, error) {
	var trust model.ForumUserTrust
	err := s.db.First(&trust, "user_id = ?", userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.Evaluate(userID)
	}
	return trust, err
}

func (s *ForumTrustService) Evaluate(userID uuid.UUID) (model.ForumUserTrust, error) {
	var user model.User
	if err := s.db.Select("uuid", "created_at").First(&user, "uuid = ?", userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.ForumUserTrust{}, apperr.NotFound("forum.trust_user_not_found", "User not found")
		}
		return model.ForumUserTrust{}, err
	}

	var topicCount, replyCount int64
	if err := s.db.Model(&model.ForumTopic{}).Where("user_id = ?", userID).Count(&topicCount).Error; err != nil {
		return model.ForumUserTrust{}, err
	}
	if err := s.forumCommentQuery().Where("author_id = ?", userID).
		Count(&replyCount).Error; err != nil {
		return model.ForumUserTrust{}, err
	}
	likerCount, err := s.countDistinctLikers(userID)
	if err != nil {
		return model.ForumUserTrust{}, err
	}

	now := time.Now().UTC()
	ageDays := int(now.Sub(user.CreatedAt).Hours() / 24)
	contributions := topicCount + replyCount
	level := evaluatedForumTrustLevel(ageDays, contributions, likerCount)
	trust := model.ForumUserTrust{UserID: userID, Level: level, EvaluatedAt: now}
	if err := s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"level":        gorm.Expr("CASE WHEN forum_user_trust.level > excluded.level THEN forum_user_trust.level ELSE excluded.level END"),
			"evaluated_at": now,
		}),
	}).Create(&trust).Error; err != nil {
		return model.ForumUserTrust{}, err
	}
	if err := s.db.First(&trust, "user_id = ?", userID).Error; err != nil {
		return model.ForumUserTrust{}, err
	}
	return trust, nil
}

func evaluatedForumTrustLevel(ageDays int, contributions int64, distinctLikers int64) int {
	switch {
	case ageDays >= forumTrustL3AgeDays && contributions >= forumTrustL3Contributions && distinctLikers >= forumTrustL3Likers:
		return 3
	case ageDays >= forumTrustL2AgeDays && contributions >= forumTrustL2Contributions && distinctLikers >= forumTrustL2Likers:
		return 2
	case ageDays >= forumTrustL1AgeDays && contributions >= forumTrustL1Contributions && distinctLikers >= forumTrustL1Likers:
		return 1
	default:
		return 0
	}
}

func (s *ForumTrustService) countDistinctLikers(userID uuid.UUID) (int64, error) {
	var count int64
	err := s.db.Raw(`
		SELECT COUNT(DISTINCT liker_id) FROM (
			SELECT forum_likes.user_id AS liker_id
			FROM forum_likes
			JOIN forum_topics ON forum_likes.target_type = 'topic' AND forum_likes.target_id = forum_topics.id
			WHERE forum_likes.deleted_at IS NULL AND forum_topics.deleted_at IS NULL
				AND forum_likes.user_id <> ? AND forum_topics.user_id = ?
			UNION
			SELECT comment_likes.user_id AS liker_id
			FROM comment_likes
			JOIN comment_entries ON comment_likes.comment_id = comment_entries.id
			JOIN discussion_targets ON comment_entries.target_id = discussion_targets.id AND discussion_targets.kind = 'forum_topic'
			JOIN forum_topics ON discussion_targets.resource_id = forum_topics.id
			WHERE comment_likes.deleted_at IS NULL AND comment_entries.deleted_at IS NULL
				AND discussion_targets.deleted_at IS NULL AND forum_topics.deleted_at IS NULL
				AND comment_likes.user_id <> ? AND comment_entries.author_id = ?
		) AS forum_likers
	`, userID, userID, userID, userID).Scan(&count).Error
	return count, err
}

func (s *ForumTrustService) CheckCreateTopic(user authctx.CurrentUser, title, content string) error {
	if utf8.RuneCountInString(title) > forumTopicTitleMaxRunes {
		return apperr.BadRequest("forum.topic_title_too_long", "Topic title exceeds 200 characters")
	}
	if utf8.RuneCountInString(content) > forumTopicContentMaxRunes {
		return apperr.BadRequest("forum.topic_content_too_long", "Topic content exceeds 50000 characters")
	}
	trust, err := s.Evaluate(user.ID)
	if err != nil {
		return err
	}
	if !forumTrustBypassesFrequency(user.Role) {
		if err := s.checkTopicFrequency(user.ID, trust.Level); err != nil {
			return err
		}
	}
	return s.checkDuplicateTopic(user.ID, title, content, uuid.Nil)
}

func (s *ForumTrustService) CheckCreateReply(user authctx.CurrentUser, content string) error {
	if utf8.RuneCountInString(content) > forumReplyContentMaxRunes {
		return apperr.BadRequest("forum.reply_content_too_long", "Reply content exceeds 20000 characters")
	}
	trust, err := s.Evaluate(user.ID)
	if err != nil {
		return err
	}
	if !forumTrustBypassesFrequency(user.Role) {
		if err := s.checkReplyFrequency(user.ID, trust.Level); err != nil {
			return err
		}
	}
	return s.checkDuplicateReply(user.ID, content, uuid.Nil)
}

func (s *ForumTrustService) CheckUpdateTopic(authorID, topicID uuid.UUID, title, content string) error {
	if utf8.RuneCountInString(title) > forumTopicTitleMaxRunes {
		return apperr.BadRequest("forum.topic_title_too_long", "Topic title exceeds 200 characters")
	}
	if utf8.RuneCountInString(content) > forumTopicContentMaxRunes {
		return apperr.BadRequest("forum.topic_content_too_long", "Topic content exceeds 50000 characters")
	}
	return s.checkDuplicateTopic(authorID, title, content, topicID)
}

func (s *ForumTrustService) CheckUpdateReply(authorID, replyID uuid.UUID, content string) error {
	if utf8.RuneCountInString(content) > forumReplyContentMaxRunes {
		return apperr.BadRequest("forum.reply_content_too_long", "Reply content exceeds 20000 characters")
	}
	return s.checkDuplicateReply(authorID, content, replyID)
}

func forumTrustBypassesFrequency(role string) bool {
	return authctx.RoleAtLeast(role, authctx.RoleModerator)
}

func (s *ForumTrustService) checkTopicFrequency(userID uuid.UUID, level int) error {
	now := time.Now().UTC()
	windowStart := now.Add(-24 * time.Hour)
	var count int64
	query := s.db.Unscoped().Model(&model.ForumTopic{}).Where("user_id = ? AND created_at >= ?", userID, windowStart)
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	level = normalizedTrustLevel(level)
	if count < int64(forumTopicDailyLimits[level]) {
		return nil
	}
	return s.rateLimitError(query, "forum.topic_rate_limited", "Topic creation limit reached", now)
}

func (s *ForumTrustService) checkReplyFrequency(userID uuid.UUID, level int) error {
	now := time.Now().UTC()
	windowStart := now.Add(-time.Hour)
	var count int64
	query := s.forumCommentQuery().Where("author_id = ? AND created_at >= ?", userID, windowStart)
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	level = normalizedTrustLevel(level)
	if count < int64(forumReplyHourlyLimits[level]) {
		return nil
	}
	return s.rateLimitError(query, "forum.reply_rate_limited", "Reply creation limit reached", now)
}

func (s *ForumTrustService) rateLimitError(query *gorm.DB, code, message string, now time.Time) error {
	var oldest struct{ CreatedAt time.Time }
	retryAfter := int64(1)
	if err := query.Select("created_at").Order("created_at ASC").Limit(1).Scan(&oldest).Error; err != nil {
		return err
	}
	if !oldest.CreatedAt.IsZero() {
		window := time.Hour
		if code == "forum.topic_rate_limited" {
			window = 24 * time.Hour
		}
		retryAfter = int64(oldest.CreatedAt.Add(window).Sub(now).Seconds()) + 1
		if retryAfter < 1 {
			retryAfter = 1
		}
	}
	return apperr.New(http.StatusTooManyRequests, code, message, map[string]any{"retry_after": retryAfter})
}

func (s *ForumTrustService) checkDuplicateTopic(userID uuid.UUID, title, content string, excludeID uuid.UUID) error {
	var topics []model.ForumTopic
	query := s.db.Unscoped().Select("title", "content").Where("user_id = ? AND created_at >= ?", userID, time.Now().UTC().Add(-forumDuplicateWindow))
	if excludeID != uuid.Nil {
		query = query.Where("id <> ?", excludeID)
	}
	if err := query.Find(&topics).Error; err != nil {
		return err
	}
	normalizedTitle := normalizeForumDuplicateText(title)
	normalizedContent := normalizeForumDuplicateText(content)
	for _, topic := range topics {
		if normalizeForumDuplicateText(topic.Title) == normalizedTitle && normalizeForumDuplicateText(topic.Content) == normalizedContent {
			return apperr.Conflict("forum.duplicate_topic", "Duplicate topic submitted within 10 minutes")
		}
	}
	return nil
}

func (s *ForumTrustService) checkDuplicateReply(userID uuid.UUID, content string, excludeID uuid.UUID) error {
	var replies []model.CommentEntry
	query := s.forumCommentQuery().Select("content").Where("author_id = ? AND created_at >= ?", userID, time.Now().UTC().Add(-forumDuplicateWindow))
	if excludeID != uuid.Nil {
		query = query.Where("id <> ?", excludeID)
	}
	if err := query.Find(&replies).Error; err != nil {
		return err
	}
	normalizedContent := normalizeForumDuplicateText(content)
	for _, reply := range replies {
		if normalizeForumDuplicateText(reply.Content) == normalizedContent {
			return apperr.Conflict("forum.duplicate_reply", "Duplicate reply submitted within 10 minutes")
		}
	}
	return nil
}

func (s *ForumTrustService) forumCommentQuery() *gorm.DB {
	targets := s.db.Model(&model.DiscussionTarget{}).
		Select("discussion_targets.id").
		Joins("JOIN forum_topics ON discussion_targets.resource_id = forum_topics.id AND forum_topics.deleted_at IS NULL").
		Where("discussion_targets.kind = ? AND discussion_targets.deleted_at IS NULL", "forum_topic")
	return s.db.Unscoped().Model(&model.CommentEntry{}).
		Where("comment_entries.deleted_at IS NULL AND comment_entries.target_id IN (?)", targets)
}

func normalizeForumDuplicateText(value string) string {
	normalized := norm.NFKC.String(value)
	normalized = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, normalized)
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(normalized))), " ")
}

func normalizedTrustLevel(level int) int {
	if level < 0 {
		return 0
	}
	if level > 3 {
		return 3
	}
	return level
}
