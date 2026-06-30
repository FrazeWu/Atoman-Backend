package subscription

import (
	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repo struct{ db *gorm.DB }

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

func (r *Repo) FindDefaultGroup(userID uuid.UUID, name string) ([]model.SubscriptionGroup, error) {
	var groups []model.SubscriptionGroup
	err := r.db.Where("user_id = ? AND name = ?", userID, name).Order("created_at ASC").Find(&groups).Error
	return groups, err
}

func (r *Repo) CreateGroup(group *model.SubscriptionGroup) error { return r.db.Create(group).Error }

func (r *Repo) CreateGroupIfNotExists(group *model.SubscriptionGroup) (bool, error) {
	result := r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "name"}},
		TargetWhere: clause.Where{Exprs: []clause.Expression{
			clause.Eq{Column: clause.Column{Name: "deleted_at"}, Value: nil},
		}},
		DoNothing: true,
	}).Create(group)
	return result.RowsAffected > 0, result.Error
}

func (r *Repo) ReassignSubscriptionsToGroup(userID uuid.UUID, fromIDs []uuid.UUID, toID uuid.UUID) error {
	return r.db.Model(&model.Subscription{}).
		Where("user_id = ? AND subscription_group_id IN ?", userID, fromIDs).
		Update("subscription_group_id", toID).Error
}

func (r *Repo) DeleteGroups(userID uuid.UUID, ids []uuid.UUID) error {
	return r.db.Where("user_id = ? AND id IN ?", userID, ids).Delete(&model.SubscriptionGroup{}).Error
}

func (r *Repo) FindFeedSourceByHash(hash string) (model.FeedSource, error) {
	var source model.FeedSource
	err := r.db.Where("hash = ?", hash).First(&source).Error
	return source, err
}

func (r *Repo) CreateFeedSource(source *model.FeedSource) error { return r.db.Create(source).Error }

func (r *Repo) FindSubscriptionByUserAndSource(userID uuid.UUID, feedSourceID uuid.UUID) (model.Subscription, error) {
	var subscription model.Subscription
	err := r.db.Where("user_id = ? AND feed_source_id = ?", userID, feedSourceID).First(&subscription).Error
	return subscription, err
}

func (r *Repo) CreateSubscription(subscription *model.Subscription) error {
	return r.db.Create(subscription).Error
}

func (r *Repo) CreateSubscriptionIfNotExists(subscription *model.Subscription) (bool, error) {
	result := r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "feed_source_id"}},
		TargetWhere: clause.Where{Exprs: []clause.Expression{
			clause.Eq{Column: clause.Column{Name: "deleted_at"}, Value: nil},
		}},
		DoNothing: true,
	}).Create(subscription)
	return result.RowsAffected > 0, result.Error
}
