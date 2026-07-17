package forum

import (
	"encoding/json"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repo struct{ db *gorm.DB }

const forumCommentCountExpr = `COALESCE((
	SELECT dt.comment_count FROM discussion_targets dt
	WHERE dt.kind = 'forum_topic' AND dt.resource_id = forum_topics.id AND dt.deleted_at IS NULL
	LIMIT 1
), 0)`

const forumLastCommentAtExpr = `COALESCE((
	SELECT MAX(ce.created_at)
	FROM discussion_targets dt
	JOIN comment_entries ce ON ce.target_id = dt.id
		AND ce.deleted_at IS NULL AND ce.status IN ('active', 'auto_folded')
	WHERE dt.kind = 'forum_topic' AND dt.resource_id = forum_topics.id AND dt.deleted_at IS NULL
), forum_topics.created_at)`

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

func (r *Repo) ListCategories(user authctx.CurrentUser) ([]model.ForumCategory, error) {
	var categories []model.ForumCategory
	db := r.visibleCategories(r.db.Model(&model.ForumCategory{}), user, "forum_categories.id")
	err := db.Order("name ASC").Find(&categories).Error
	return categories, err
}

func (r *Repo) GetCategory(user authctx.CurrentUser, id uuid.UUID) (model.ForumCategory, error) {
	var category model.ForumCategory
	db := r.visibleCategories(r.db.Model(&model.ForumCategory{}), user, "forum_categories.id")
	err := db.First(&category, "forum_categories.id = ?", id).Error
	return category, err
}

func (r *Repo) CreateTopic(topic *model.ForumTopic) error { return r.db.Create(topic).Error }

func (r *Repo) GetTopic(user authctx.CurrentUser, id uuid.UUID) (model.ForumTopic, error) {
	var topic model.ForumTopic
	db := r.visibleCategories(r.db.Model(&model.ForumTopic{}), user, "forum_topics.category_id")
	err := db.Preload("User.ForumTrust").Preload("Category").First(&topic, "forum_topics.id = ?", id).Error
	attachForumTrustLevel(topic.User)
	return topic, err
}

func (r *Repo) GetTopicForUpdate(id uuid.UUID) (model.ForumTopic, error) {
	var topic model.ForumTopic
	err := r.db.Clauses(clause.Locking{Strength: "UPDATE"}).Preload("User").Preload("Category").First(&topic, "id = ?", id).Error
	return topic, err
}

func (r *Repo) ListTopics(user authctx.CurrentUser, query ListTopicsQuery) ([]model.ForumTopic, int64, error) {
	db := r.visibleCategories(r.db.Model(&model.ForumTopic{}), user, "forum_topics.category_id")
	if query.CategoryID != uuid.Nil {
		db = db.Where("category_id = ?", query.CategoryID)
	}
	if search := strings.TrimSpace(query.Search); search != "" {
		db = db.Where("(title LIKE ? OR content LIKE ?)", "%"+search+"%", "%"+search+"%")
	}
	if tag := strings.TrimSpace(query.Tag); tag != "" {
		encoded, _ := json.Marshal(tag)
		db = db.Where("tags LIKE ? ESCAPE '\\'", "%"+escapeLike(string(encoded))+"%")
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var topics []model.ForumTopic
	err := db.Preload("User.ForumTrust").Preload("Category").Order(topicOrder(query.Sort)).Offset(offset(query.Page, query.PageSize)).Limit(normalizedPageSize(query.PageSize)).Find(&topics).Error
	attachTopicTrustLevels(topics)
	return topics, total, err
}

func (r *Repo) visibleCategories(db *gorm.DB, user authctx.CurrentUser, categoryColumn string) *gorm.DB {
	if authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return db
	}
	base := "NOT EXISTS (SELECT 1 FROM forum_category_permissions fcp WHERE fcp.category_id = " + categoryColumn + " AND fcp.deleted_at IS NULL)"
	if user.ID == uuid.Nil {
		return db.Where(base)
	}
	allowed := `EXISTS (
		SELECT 1 FROM forum_category_permissions fcp
		JOIN forum_group_members fgm ON fgm.group_id = fcp.group_id AND fgm.deleted_at IS NULL
		WHERE fcp.category_id = ` + categoryColumn + ` AND fcp.can_view = ? AND fcp.deleted_at IS NULL AND fgm.user_id = ?
	)`
	return db.Where("("+base+" OR "+allowed+")", true, user.ID)
}

func (r *Repo) SaveTopic(topic *model.ForumTopic) error { return r.db.Save(topic).Error }

func (r *Repo) DeleteTopic(id uuid.UUID) error {
	return r.db.Delete(&model.ForumTopic{}, "id = ?", id).Error
}

func attachForumTrustLevel(user *model.User) {
	if user != nil && user.ForumTrust != nil {
		user.ForumTrustLevel = user.ForumTrust.Level
	}
}

func attachTopicTrustLevels(topics []model.ForumTopic) {
	for index := range topics {
		attachForumTrustLevel(topics[index].User)
	}
}

func (r *Repo) UpsertDraft(draft *model.ForumDraft) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "user_id"},
			{Name: "context_key"},
		},
		DoUpdates: clause.AssignmentColumns([]string{"title", "content", "tags", "updated_at"}),
	}).Create(draft).Error
}

func (r *Repo) ListDrafts(userID uuid.UUID) ([]model.ForumDraft, error) {
	var drafts []model.ForumDraft
	err := r.db.Where("user_id = ?", userID).Order("updated_at DESC").Find(&drafts).Error
	return drafts, err
}

func (r *Repo) GetDraft(userID uuid.UUID, contextKey string) (model.ForumDraft, error) {
	var draft model.ForumDraft
	err := r.db.Where("user_id = ? AND context_key = ?", userID, contextKey).First(&draft).Error
	return draft, err
}

func (r *Repo) DeleteDraft(userID uuid.UUID, draftID uuid.UUID) error {
	return r.db.Where("user_id = ? AND id = ?", userID, draftID).Delete(&model.ForumDraft{}).Error
}

func (r *Repo) DeleteDraftByContext(userID uuid.UUID, contextKey string) error {
	return r.db.Where("user_id = ? AND context_key = ?", userID, contextKey).Delete(&model.ForumDraft{}).Error
}

func (r *Repo) UpsertFollow(follow *model.ForumFollow) error {
	if err := r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "target_type"}, {Name: "target_key"}},
		DoNothing: true,
	}).Create(follow).Error; err != nil {
		return err
	}
	var stored model.ForumFollow
	lookup := r.db.Session(&gorm.Session{NewDB: true})
	if err := lookup.Where("user_id = ? AND target_type = ? AND target_key = ?", follow.UserID, follow.TargetType, follow.TargetKey).First(&stored).Error; err != nil {
		return err
	}
	*follow = stored
	return nil
}

func (r *Repo) ListFollows(userID uuid.UUID) ([]model.ForumFollow, error) {
	var follows []model.ForumFollow
	err := r.db.Where("user_id = ?", userID).Order("created_at ASC, id ASC").Find(&follows).Error
	return follows, err
}

func (r *Repo) DeleteFollow(userID uuid.UUID, targetType, targetKey string) error {
	return r.db.Unscoped().Where("user_id = ? AND target_type = ? AND target_key = ?", userID, targetType, targetKey).Delete(&model.ForumFollow{}).Error
}

func (r *Repo) ListFollowerIDs(targetType, targetKey string) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := r.db.Model(&model.ForumFollow{}).
		Where("target_type = ? AND target_key = ?", targetType, targetKey).
		Order("created_at ASC, id ASC").
		Pluck("user_id", &ids).Error
	return ids, err
}

func (r *Repo) FilterUsersWhoCanViewCategory(userIDs []uuid.UUID, categoryID uuid.UUID) ([]uuid.UUID, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	var allowed []uuid.UUID
	err := r.db.Model(&model.User{}).
		Where("uuid IN ? AND is_active = ?", userIDs, true).
		Where(`role IN ? OR
			NOT EXISTS (
				SELECT 1 FROM forum_category_permissions fcp
				WHERE fcp.category_id = ? AND fcp.deleted_at IS NULL
			) OR EXISTS (
				SELECT 1 FROM forum_category_permissions fcp
				JOIN forum_group_members fgm ON fgm.group_id = fcp.group_id AND fgm.deleted_at IS NULL
				WHERE fcp.category_id = ? AND fcp.can_view = ? AND fcp.deleted_at IS NULL AND fgm.user_id = uuid
			)`, []string{authctx.RoleAdmin, authctx.RoleOwner}, categoryID, categoryID, true).
		Pluck("uuid", &allowed).Error
	return allowed, err
}

func (r *Repo) TagExists(user authctx.CurrentUser, tag string) (bool, error) {
	encoded, _ := json.Marshal(tag)
	var count int64
	db := r.visibleCategories(r.db.Model(&model.ForumTopic{}), user, "forum_topics.category_id")
	err := db.Where("tags LIKE ? ESCAPE '\\'", "%"+escapeLike(string(encoded))+"%").Count(&count).Error
	return count > 0, err
}

func topicOrder(sort string) string {
	switch sort {
	case "top":
		return "like_count DESC, " + forumCommentCountExpr + " DESC, forum_topics.created_at DESC"
	case "active":
		return forumLastCommentAtExpr + " DESC"
	case "featured":
		return "featured DESC, created_at DESC"
	default:
		return "created_at DESC"
	}
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")
	return replacer.Replace(value)
}

func offset(page int, pageSize int) int {
	page = normalizedPage(page)
	return (page - 1) * normalizedPageSize(pageSize)
}

func normalizedPage(page int) int {
	if page < 1 {
		return 1
	}
	return page
}

func normalizedPageSize(pageSize int) int {
	if pageSize < 1 {
		return 20
	}
	if pageSize > 100 {
		return 100
	}
	return pageSize
}
