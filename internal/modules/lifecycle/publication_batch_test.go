package lifecycle

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestPublicationBatchFanoutAcrossHundredsOfRecipients(t *testing.T) {
	fixture := newLifecycleFixture(t)
	source := model.FeedSource{
		SourceType: "internal_channel", SourceID: &fixture.channel.ID,
		Hash: uuid.NewString(), Title: fixture.channel.Name,
	}
	if err := fixture.db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	const subscribers = 423
	users := make([]model.User, subscribers)
	for i := range users {
		users[i] = model.User{
			Username: fmt.Sprintf("fanout-test-%d", i),
			Email:    fmt.Sprintf("fanout-test-%d@sample.test", i),
			Password: "hash", IsActive: true,
		}
	}
	if err := fixture.db.CreateInBatches(&users, 60).Error; err != nil {
		t.Fatal(err)
	}
	subscriptions := make([]model.Subscription, subscribers)
	preferences := make([]model.ContentNotificationPreference, subscribers)
	for i, user := range users {
		subscriptions[i] = model.Subscription{UserID: user.UUID, FeedSourceID: source.ID}
		preferences[i] = model.ContentNotificationPreference{
			UserID: user.UUID, SourceType: "internal_channel", SourceID: fixture.channel.ID, Mode: "all",
		}
	}
	if err := fixture.db.CreateInBatches(&subscriptions, 60).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.CreateInBatches(&preferences, 60).Error; err != nil {
		t.Fatal(err)
	}

	// A follower with the user-scope preference must be notified even if
	// they have never subscribed to a feed source for this channel.
	if err := fixture.db.Create(&model.Follow{FollowerID: fixture.viewer.ID, FollowingID: fixture.owner.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.SaveNotificationPreference(fixture.viewer, NotificationPreferenceInput{
		SourceType: "internal_user", SourceID: fixture.owner.ID, Mode: "all",
	}); err != nil {
		t.Fatal(err)
	}

	var preferenceQueries atomic.Int32
	callback := "audit:publication_query_count:" + uuid.NewString()
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == (model.ContentNotificationPreference{}).TableName() {
			preferenceQueries.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Query().Remove(callback) })
	if err := fixture.service.EnqueuePublication("blog", fixture.post.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.DispatchPendingPublications(10); err != nil {
		t.Fatal(err)
	}
	if n := preferenceQueries.Load(); n > 5 || n == 0 {
		t.Fatalf("423 recipients should take a bounded number of preference queries, got %d", n)
	}
	var total int64
	if err := fixture.db.Model(&model.Notification{}).
		Where("type = ? AND source_type = ? AND source_id = ?", "content_published", "content_publication", fixture.post.ID).
		Count(&total).Error; err != nil {
		t.Fatal(err)
	}
	if total != subscribers+1 {
		t.Fatalf("want %d distinct notifications, got %d", subscribers+1, total)
	}
	if err := fixture.service.DispatchPendingPublications(10); err != nil {
		t.Fatal(err)
	}
	var after int64
	if err := fixture.db.Model(&model.Notification{}).
		Where("type = ? AND source_type = ? AND source_id = ?", "content_published", "content_publication", fixture.post.ID).
		Count(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after != total {
		t.Fatalf("dispatch retry duplicated notifications: before=%d after=%d", total, after)
	}
}

func TestPublicationLeaseRenewalRefusesReclaimedEvent(t *testing.T) {
	fixture := newLifecycleFixture(t)
	expired := time.Now().UTC().Add(-publicationProcessingTimeout - time.Minute)
	event := model.ContentPublicationEvent{
		ChannelID: fixture.channel.ID, OwnerID: fixture.owner.ID, ContentType: "blog", ContentID: fixture.post.ID,
		Status: "processing", LeaseVersion: 7,
	}
	if err := fixture.db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&event).Update("updated_at", expired).Error; err != nil {
		t.Fatal(err)
	}
	owned, err := fixture.service.renewPublicationLease(event)
	if err != nil || !owned {
		t.Fatalf("active lease did not renew: owned=%v err=%v", owned, err)
	}
	var refreshed model.ContentPublicationEvent
	if err := fixture.db.First(&refreshed, "id = ?", event.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refreshed.UpdatedAt.Before(time.Now().UTC().Add(-time.Minute)) {
		t.Fatalf("event lease was not renewed: %s", refreshed.UpdatedAt)
	}
	stale := event
	if err := fixture.db.Model(&model.ContentPublicationEvent{}).Where("id = ?", event.ID).
		Update("lease_version", 8).Error; err != nil {
		t.Fatal(err)
	}
	owned, err = fixture.service.renewPublicationLease(stale)
	if err != nil || owned {
		t.Fatalf("old lease version must not renew: owned=%v err=%v", owned, err)
	}
}
