package service

import (
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestCreateNotificationKeepsDifferentNonLikeTypesSeparate(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Notification{})

	service := NewNotificationService(db)

	recipient := model.User{Username: "recipient_" + uuid.NewString()[:8], Email: uuid.NewString() + "@example.com", Password: "secret", IsActive: true}
	if err := db.Create(&recipient).Error; err != nil {
		t.Fatalf("create recipient: %v", err)
	}

	firstActor := model.User{Username: "actor1_" + uuid.NewString()[:8], Email: uuid.NewString() + "@example.com", Password: "secret", IsActive: true}
	if err := db.Create(&firstActor).Error; err != nil {
		t.Fatalf("create first actor: %v", err)
	}

	secondActor := model.User{Username: "actor2_" + uuid.NewString()[:8], Email: uuid.NewString() + "@example.com", Password: "secret", IsActive: true}
	if err := db.Create(&secondActor).Error; err != nil {
		t.Fatalf("create second actor: %v", err)
	}

	sourceType := "forum_reply"
	sourceID := uuid.New()

	first, err := service.CreateNotification(recipient.UUID, &firstActor.UUID, "forum_reply", sourceType, sourceID, model.NotificationMeta{"topic_id": uuid.NewString()})
	if err != nil {
		t.Fatalf("create first notification: %v", err)
	}
	if first == nil {
		t.Fatal("expected first notification to be created")
	}

	second, err := service.CreateNotification(recipient.UUID, &secondActor.UUID, "forum_mention", sourceType, sourceID, model.NotificationMeta{"topic_id": uuid.NewString()})
	if err != nil {
		t.Fatalf("create second notification: %v", err)
	}
	if second == nil {
		t.Fatal("expected second notification to be created")
	}

	var notifications []model.Notification
	if err := db.Where("recipient_id = ? AND source_type = ? AND source_id = ?", recipient.UUID, sourceType, sourceID).Order("created_at ASC").Find(&notifications).Error; err != nil {
		t.Fatalf("load notifications: %v", err)
	}

	if len(notifications) != 2 {
		t.Fatalf("expected 2 notifications for same recipient/source, got %d", len(notifications))
	}

	byType := make(map[string]model.Notification, len(notifications))
	for _, notification := range notifications {
		byType[notification.Type] = notification
	}
	if _, ok := byType["forum_reply"]; !ok {
		t.Fatalf("expected forum_reply notification to be preserved, got %#v", byType)
	}
	if _, ok := byType["forum_mention"]; !ok {
		t.Fatalf("expected forum_mention notification to be preserved, got %#v", byType)
	}
}

func TestNotifyForumLikeConcurrentUpdatesKeepActorCountAndRecentActors(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Notification{})

	service := NewNotificationService(db)

	recipient := model.User{Username: "recipient_" + uuid.NewString()[:8], Email: uuid.NewString() + "@example.com", Password: "secret", IsActive: true}
	if err := db.Create(&recipient).Error; err != nil {
		t.Fatalf("create recipient: %v", err)
	}

	sourceID := uuid.New()
	topicID := uuid.New()

	firstActor := model.User{Username: "actor_0", Email: uuid.NewString() + "@example.com", Password: "secret", IsActive: true}
	if err := db.Create(&firstActor).Error; err != nil {
		t.Fatalf("create first actor: %v", err)
	}
	if err := service.NotifyForumLike(recipient.UUID, firstActor.UUID, firstActor.Username, "forum_topic", sourceID, topicID, "topic", nil); err != nil {
		t.Fatalf("create initial like notification: %v", err)
	}

	var barrierEnabled int32
	var queryCount int32
	queriesReady := make(chan struct{})
	releaseQueries := make(chan struct{})
	if err := db.Callback().Query().After("gorm:query").Register("test:hold_like_notification_reads", func(tx *gorm.DB) {
		if atomic.LoadInt32(&barrierEnabled) == 0 || tx.Statement.Table != "notifications" {
			return
		}
		if atomic.AddInt32(&queryCount, 1) == 2 {
			close(queriesReady)
		}
		<-releaseQueries
	}); err != nil {
		t.Fatalf("register query barrier: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove("test:hold_like_notification_reads")
	})

	const concurrentActors = 2
	errs := make(chan error, concurrentActors)
	var wg sync.WaitGroup
	atomic.StoreInt32(&barrierEnabled, 1)
	for i := 1; i <= concurrentActors; i++ {
		i := i
		actor := model.User{Username: fmt.Sprintf("actor_%d", i), Email: uuid.NewString() + "@example.com", Password: "secret", IsActive: true}
		if err := db.Create(&actor).Error; err != nil {
			t.Fatalf("create actor %d: %v", i, err)
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- service.NotifyForumLike(recipient.UUID, actor.UUID, actor.Username, "forum_topic", sourceID, topicID, "topic", nil)
		}()
	}
	<-queriesReady
	close(releaseQueries)
	wg.Wait()
	atomic.StoreInt32(&barrierEnabled, 0)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("notify forum like: %v", err)
		}
	}

	var notifications []model.Notification
	if err := db.Where("recipient_id = ? AND source_type = ? AND source_id = ? AND type = ?", recipient.UUID, "forum_topic", sourceID, "forum_like").Find(&notifications).Error; err != nil {
		t.Fatalf("load notifications: %v", err)
	}
	if len(notifications) != 1 {
		t.Fatalf("expected one aggregated notification, got %d", len(notifications))
	}

	count, ok := notificationActorCount(notifications[0].Meta["actor_count"])
	if !ok {
		t.Fatalf("actor_count has unexpected type %T", notifications[0].Meta["actor_count"])
	}
	if count != concurrentActors+1 {
		t.Fatalf("expected actor_count %d, got %d with meta %#v", concurrentActors+1, count, notifications[0].Meta)
	}

	recentActors := extractRecentActors(notifications[0].Meta["recent_actors"])
	for _, username := range []string{"actor_1", "actor_2"} {
		if !slices.Contains(recentActors, username) {
			t.Fatalf("expected recent_actors to include %s, got %#v", username, recentActors)
		}
	}
}

func notificationActorCount(value interface{}) (int, bool) {
	switch v := value.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	default:
		return 0, false
	}
}
