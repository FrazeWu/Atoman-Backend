package feed

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const maxSubscriptionSyncWorkers = 4

var subscriptionSyncSlots = make(chan struct{}, maxSubscriptionSyncWorkers)

type SubscriptionSyncResult struct {
	SubscriptionID uuid.UUID `json:"subscription_id"`
	FeedSourceID   uuid.UUID `json:"feed_source_id"`
	FetchedItems   int       `json:"fetched_items"`
	NewItems       int64     `json:"new_items"`
	SyncedAt       time.Time `json:"synced_at"`
	Success        bool      `json:"success"`
	Error          string    `json:"error,omitempty"`
}

type SubscriptionSyncSummary struct {
	Total     int                      `json:"total"`
	Succeeded int                      `json:"succeeded"`
	Failed    int                      `json:"failed"`
	NewItems  int64                    `json:"new_items"`
	Results   []SubscriptionSyncResult `json:"results"`
}

func (s *Service) SyncSubscription(user authctx.CurrentUser, subscriptionID uuid.UUID) (SubscriptionSyncResult, error) {
	return s.SyncSubscriptionContext(context.Background(), user, subscriptionID)
}

func (s *Service) SyncSubscriptionContext(ctx context.Context, user authctx.CurrentUser, subscriptionID uuid.UUID) (SubscriptionSyncResult, error) {
	var subscription model.Subscription
	err := s.db.WithContext(ctx).Preload("FeedSource").
		Where("id = ? AND user_id = ?", subscriptionID, user.ID).
		First(&subscription).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return SubscriptionSyncResult{}, apperr.NotFound("feed.subscription_not_found", "Subscription not found")
	}
	if err != nil {
		return SubscriptionSyncResult{}, err
	}
	if subscription.IsPaused {
		return SubscriptionSyncResult{}, apperr.BadRequest("feed.subscription_paused", "Paused subscriptions cannot be refreshed")
	}
	if subscription.FeedSource == nil || subscription.FeedSource.SourceType != "external_rss" {
		return SubscriptionSyncResult{}, apperr.BadRequest("feed.subscription_not_external", "Only external RSS subscriptions can be refreshed")
	}

	result, _ := s.syncLoadedSubscriptionContext(ctx, subscription)
	if err := s.persistSubscriptionSyncState(subscription, result); err != nil {
		log.Printf("failed to persist subscription sync state for %s: %v", subscription.ID, err)
		result.Success = false
		result.Error = "failed to save refresh status"
	}
	return result, nil
}

func (s *Service) SyncAllSubscriptions(user authctx.CurrentUser) (SubscriptionSyncSummary, error) {
	return s.SyncAllSubscriptionsContext(context.Background(), user)
}

func (s *Service) SyncAllSubscriptionsContext(ctx context.Context, user authctx.CurrentUser) (SubscriptionSyncSummary, error) {
	var subscriptions []model.Subscription
	if err := s.db.WithContext(ctx).Preload("FeedSource").Where("user_id = ?", user.ID).Find(&subscriptions).Error; err != nil {
		return SubscriptionSyncSummary{}, err
	}

	external := make([]model.Subscription, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		if subscription.IsPaused {
			continue
		}
		if subscription.FeedSource != nil && subscription.FeedSource.SourceType == "external_rss" {
			external = append(external, subscription)
		}
	}

	results := make([]SubscriptionSyncResult, len(external))
	type syncOutcome struct {
		index  int
		result SubscriptionSyncResult
	}
	outcomes := make(chan syncOutcome, len(external))
	jobs := make(chan int)
	var wg sync.WaitGroup
	workerCount := min(maxSubscriptionSyncWorkers, len(external))
	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				result, _ := s.syncLoadedSubscriptionContext(ctx, external[index])
				outcomes <- syncOutcome{index: index, result: result}
			}
		}()
	}
	for index := range external {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	close(outcomes)
	for outcome := range outcomes {
		results[outcome.index] = outcome.result
	}

	summary := SubscriptionSyncSummary{Total: len(results), Results: results}
	for i := range results {
		if err := s.persistSubscriptionSyncState(external[i], results[i]); err != nil {
			log.Printf("failed to persist subscription sync state for %s: %v", external[i].ID, err)
			results[i].Success = false
			results[i].Error = "failed to save refresh status"
		}
		summary.NewItems += results[i].NewItems
		if results[i].Success {
			summary.Succeeded++
		} else {
			summary.Failed++
		}
	}
	summary.Results = results
	return summary, nil
}

func (s *Service) HasExternalFeedUpdates(user authctx.CurrentUser, query FeedQuery, since time.Time) (bool, error) {
	if user.ID == uuid.Nil {
		return false, apperr.Unauthorized("Login required")
	}
	query.SourceType = "external_rss"
	subscriptions, err := s.repo.ListSubscriptionsWithSources(user.ID, query)
	if err != nil {
		return false, err
	}
	sourceIDs := make([]uuid.UUID, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		if query.SourceID == uuid.Nil && (subscription.IsMuted || subscription.IsPaused) {
			continue
		}
		if subscription.FeedSource != nil && subscription.FeedSource.SourceType == "external_rss" {
			sourceIDs = append(sourceIDs, subscription.FeedSourceID)
		}
	}
	sourceIDs = dedupeUUIDs(sourceIDs)
	if len(sourceIDs) == 0 {
		return false, nil
	}
	var count int64
	if err := s.db.Model(&model.FeedItem{}).
		Where("feed_source_id IN ? AND fetched_at > ?", sourceIDs, since).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Service) syncLoadedSubscription(subscription model.Subscription) (SubscriptionSyncResult, error) {
	return s.syncLoadedSubscriptionContext(context.Background(), subscription)
}

func (s *Service) syncLoadedSubscriptionContext(ctx context.Context, subscription model.Subscription) (SubscriptionSyncResult, error) {
	result := SubscriptionSyncResult{
		SubscriptionID: subscription.ID,
		FeedSourceID:   subscription.FeedSourceID,
		SyncedAt:       time.Now().UTC(),
	}
	select {
	case subscriptionSyncSlots <- struct{}{}:
		defer func() { <-subscriptionSyncSlots }()
	case <-ctx.Done():
		result.Error = "refresh canceled"
		return result, ctx.Err()
	}
	rssResult, err := s.syncSource(ctx, s.db.WithContext(ctx), *subscription.FeedSource)
	result.FetchedItems = rssResult.FetchedItems
	result.NewItems = rssResult.NewItems
	if !rssResult.SyncedAt.IsZero() {
		result.SyncedAt = rssResult.SyncedAt
	}
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	result.Success = true
	return result, nil
}

func (s *Service) persistSubscriptionSyncState(subscription model.Subscription, result SubscriptionSyncResult) error {
	updates := map[string]interface{}{
		"last_checked": result.SyncedAt,
	}
	if result.Success {
		updates["health_status"] = "healthy"
		updates["error_message"] = ""
	} else {
		updates["health_status"] = "error"
		updates["error_message"] = result.Error
	}
	return s.db.Model(&subscription).Updates(updates).Error
}
