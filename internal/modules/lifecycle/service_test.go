package lifecycle

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type lifecycleFixture struct {
	db      *gorm.DB
	service *Service
	owner   authctx.CurrentUser
	viewer  authctx.CurrentUser
	channel model.Channel
	post    model.Post
}

func startDispatchWorker(t *testing.T, dispatch func() error, release func()) <-chan error {
	t.Helper()
	result := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		result <- dispatch()
	}()
	t.Cleanup(func() {
		release()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("dispatch worker did not exit during cleanup")
		}
	})
	return result
}

func newLifecycleFixture(t *testing.T) lifecycleFixture {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Channel{}, &model.Collection{}, &model.Post{}, &model.PodcastEpisode{}, &model.Video{}, &model.VideoCollection{},
		&model.ContentEntry{}, &model.ContentBlogExtension{}, &model.ContentEpisodeExtension{}, &model.ContentVideoExtension{}, &model.ContentCollection{}, &model.ContentCollectionMembership{},
		&model.ContentLifecycleEvent{}, &model.ContentProgress{}, &model.ContentNotificationPreference{},
		&model.ContentPublicationEvent{}, &model.BlogPublishSchedule{}, &model.FeedSource{}, &model.Subscription{}, &model.Follow{}, &model.Notification{},
	)
	if err := migrations.RunNotificationDMIndexes(db); err != nil {
		t.Fatal(err)
	}
	ownerModel := model.User{Username: "lifecycle-owner", Email: "lifecycle-owner@example.com", Password: "hash", IsActive: true}
	viewerModel := model.User{Username: "lifecycle-viewer", Email: "lifecycle-viewer@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&ownerModel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&viewerModel).Error; err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{UserID: &ownerModel.UUID, Name: "Lifecycle", Slug: "lifecycle-" + uuid.NewString()[:8]}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	collection := model.Collection{ChannelID: channel.ID, ContentType: "blog", Name: "Articles"}
	if err := db.Create(&collection).Error; err != nil {
		t.Fatal(err)
	}
	canonicalCollection := model.ContentCollection{Base: collection.Base, ChannelID: channel.ID, CreatedBy: &ownerModel.UUID, Name: collection.Name}
	if err := db.Create(&canonicalCollection).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	post := model.Post{
		UserID: ownerModel.UUID, ChannelID: &channel.ID, CollectionID: &collection.ID,
		Title: "Lifecycle article", Content: "body", Status: "published", Visibility: "public", PublishedAt: &now,
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatal(err)
	}
	seedLifecycleCanonicalBlog(t, db, post, canonicalCollection.ID)
	return lifecycleFixture{
		db: db, service: NewService(db), channel: channel, post: post,
		owner:  authctx.CurrentUser{ID: ownerModel.UUID, Username: ownerModel.Username, Role: authctx.RoleUser},
		viewer: authctx.CurrentUser{ID: viewerModel.UUID, Username: viewerModel.Username, Role: authctx.RoleUser},
	}
}

func seedLifecycleCanonicalBlog(t *testing.T, db *gorm.DB, post model.Post, collectionID uuid.UUID) {
	t.Helper()
	if post.ChannelID == nil {
		t.Fatal("lifecycle blog post must have a channel")
	}
	entry := model.ContentEntry{
		Base: post.Base, AuthorID: &post.UserID, ChannelID: *post.ChannelID, Kind: "blog",
		Title: post.Title, Summary: post.Summary, CoverURL: post.CoverURL,
		Status: post.Status, Visibility: post.Visibility, PublishedAt: post.PublishedAt, ScheduledAt: post.ScheduledAt,
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("create canonical lifecycle entry: %v", err)
	}
	if err := db.Create(&model.ContentBlogExtension{ContentID: entry.ID, Content: post.Content, ViewCount: post.ViewCount}).Error; err != nil {
		t.Fatalf("create canonical lifecycle extension: %v", err)
	}
	if collectionID != uuid.Nil {
		if err := db.Create(&model.ContentCollectionMembership{ContentID: entry.ID, CollectionID: collectionID, Position: post.CollectionPosition}).Error; err != nil {
			t.Fatalf("create canonical lifecycle membership: %v", err)
		}
	}
}

func TestRecordEventDeduplicatesClientEventID(t *testing.T) {
	fixture := newLifecycleFixture(t)
	input := EventInput{
		Module: "blog", ContentID: fixture.post.ID, Event: "open", Source: "discover",
		SessionID: "session-1", ClientEventID: "event-1",
	}
	if err := fixture.service.RecordEvent(fixture.viewer, input); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.RecordEvent(fixture.viewer, input); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := fixture.db.Model(&model.ContentLifecycleEvent{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one deduplicated event, got %d", count)
	}
	var extension model.ContentBlogExtension
	if err := fixture.db.First(&extension, "content_id = ?", fixture.post.ID).Error; err != nil {
		t.Fatal(err)
	}
	if extension.ViewCount != 1 {
		t.Fatalf("expected the deduplicated open event to increment one view, got %d", extension.ViewCount)
	}
	if err := fixture.service.RecordEvent(fixture.owner, EventInput{
		Module: "blog", ContentID: fixture.post.ID, Event: "open", ClientEventID: "owner-open",
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&extension, "content_id = ?", fixture.post.ID).Error; err != nil {
		t.Fatal(err)
	}
	if extension.ViewCount != 1 {
		t.Fatalf("expected owner open not to increment views, got %d", extension.ViewCount)
	}
}

func TestRecordEventRequiresFollowerAccess(t *testing.T) {
	fixture := newLifecycleFixture(t)
	if err := fixture.db.Model(&model.ContentEntry{}).Where("id = ?", fixture.post.ID).Update("visibility", "followers").Error; err != nil {
		t.Fatal(err)
	}
	input := EventInput{Module: "blog", ContentID: fixture.post.ID, Event: "open", ClientEventID: "followers-open"}
	if err := fixture.service.RecordEvent(fixture.viewer, input); err == nil {
		t.Fatal("expected a non-follower event to be rejected")
	}
	if err := fixture.db.Create(&model.Follow{FollowerID: fixture.viewer.ID, FollowingID: fixture.owner.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.RecordEvent(fixture.viewer, input); err != nil {
		t.Fatalf("expected follower event to succeed: %v", err)
	}
}

func TestProgressIsCrossDeviceAndReturnsContinueItem(t *testing.T) {
	fixture := newLifecycleFixture(t)
	if _, err := fixture.service.SaveProgress(fixture.viewer, ProgressInput{
		Module: "blog", ContentID: fixture.post.ID, Progress: 0.62, PositionSec: 72, DurationSec: 120, Source: "search",
	}); err != nil {
		t.Fatal(err)
	}
	items, err := fixture.service.ListContinue(fixture.viewer, "blog", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ContentID != fixture.post.ID || items[0].Progress != 0.62 || items[0].Path != "/posts/post/"+fixture.post.ID.String() {
		t.Fatalf("unexpected continue items: %#v", items)
	}
	if _, err := fixture.service.SaveProgress(fixture.viewer, ProgressInput{
		Module: "blog", ContentID: fixture.post.ID, Progress: 0.98, PositionSec: 118, DurationSec: 120, Completed: true,
	}); err != nil {
		t.Fatal(err)
	}
	items, err = fixture.service.ListContinue(fixture.viewer, "blog", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("completed content must leave continue list: %#v", items)
	}
}

func TestDispatchPublicationNotifiesOptedInSubscribersOnce(t *testing.T) {
	fixture := newLifecycleFixture(t)
	source := model.FeedSource{SourceType: "internal_channel", SourceID: &fixture.channel.ID, Hash: uuid.NewString(), Title: fixture.channel.Name}
	if err := fixture.db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Create(&model.Subscription{UserID: fixture.viewer.ID, FeedSourceID: source.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.SaveNotificationPreference(fixture.viewer, NotificationPreferenceInput{
		SourceType: "internal_channel", SourceID: fixture.channel.ID, Mode: "all",
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.EnqueuePublication("blog", fixture.post.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.DispatchPendingPublications(10); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.DispatchPendingPublications(10); err != nil {
		t.Fatal(err)
	}
	var notifications []model.Notification
	if err := fixture.db.Where("recipient_id = ? AND type = ?", fixture.viewer.ID, "content_published").Find(&notifications).Error; err != nil {
		t.Fatal(err)
	}
	if len(notifications) != 1 {
		t.Fatalf("expected one publication notification, got %#v", notifications)
	}
	if notifications[0].Meta["path"] != "/posts/post/"+fixture.post.ID.String() {
		t.Fatalf("unexpected notification: %#v", notifications[0])
	}
}

func TestDispatchPublicationNotifiesVideoCollectionSubscribers(t *testing.T) {
	fixture := newLifecycleFixture(t)
	videoCollection := model.Collection{ChannelID: fixture.channel.ID, ContentType: "video", Name: "Videos"}
	if err := fixture.db.Create(&videoCollection).Error; err != nil {
		t.Fatal(err)
	}
	canonicalVideoCollection := model.ContentCollection{Base: videoCollection.Base, ChannelID: videoCollection.ChannelID, Name: videoCollection.Name}
	if err := fixture.db.Create(&canonicalVideoCollection).Error; err != nil {
		t.Fatal(err)
	}
	video := model.Video{
		UserID: fixture.owner.ID, ChannelID: &fixture.channel.ID, Title: "Lifecycle video",
		StorageType: "external", VideoURL: "https://example.com/video.mp4", Status: "published", Visibility: "public",
	}
	if err := fixture.db.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	videoEntry := model.ContentEntry{
		Base: video.Base, AuthorID: &fixture.owner.ID, ChannelID: fixture.channel.ID,
		Kind: "video", Title: video.Title, Status: video.Status, Visibility: video.Visibility,
	}
	if err := fixture.db.Create(&videoEntry).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Create(&model.ContentVideoExtension{
		ContentID: videoEntry.ID, VideoID: video.ID, CreatedAt: video.CreatedAt, UpdatedAt: video.UpdatedAt,
		StorageType: video.StorageType, VideoURL: video.VideoURL, ProcessingStatus: "none",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Create(&model.ContentCollectionMembership{ContentID: videoEntry.ID, CollectionID: canonicalVideoCollection.ID}).Error; err != nil {
		t.Fatal(err)
	}
	source := model.FeedSource{SourceType: "internal_collection", SourceID: &videoCollection.ID, Hash: uuid.NewString(), Title: videoCollection.Name}
	if err := fixture.db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Create(&model.Subscription{UserID: fixture.viewer.ID, FeedSourceID: source.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.SaveNotificationPreference(fixture.viewer, NotificationPreferenceInput{
		SourceType: "internal_collection", SourceID: videoCollection.ID, Mode: "all",
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.EnqueuePublication("video", video.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.DispatchPendingPublications(10); err != nil {
		t.Fatal(err)
	}
	var notifications []model.Notification
	if err := fixture.db.Where("recipient_id = ? AND type = ?", fixture.viewer.ID, "content_published").Find(&notifications).Error; err != nil {
		t.Fatal(err)
	}
	if len(notifications) != 1 || notifications[0].Meta["path"] != "/videos/watch/"+video.ID.String() {
		t.Fatalf("expected one video collection notification, got %#v", notifications)
	}
}

func TestDispatchPendingPublicationsConcurrentlyClaimsEventBeforeNotifying(t *testing.T) {
	fixture := newLifecycleFixture(t)
	source := model.FeedSource{SourceType: "internal_channel", SourceID: &fixture.channel.ID, Hash: uuid.NewString(), Title: fixture.channel.Name}
	if err := fixture.db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Create(&model.Subscription{UserID: fixture.viewer.ID, FeedSourceID: source.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.SaveNotificationPreference(fixture.viewer, NotificationPreferenceInput{
		SourceType: "internal_channel", SourceID: fixture.channel.ID, Mode: "all",
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.EnqueuePublication("blog", fixture.post.ID); err != nil {
		t.Fatal(err)
	}

	candidatesReady := make(chan struct{})
	firstDeliveryCompleted := make(chan struct{})
	var candidatesReadyOnce sync.Once
	var firstDeliveryOnce sync.Once
	releaseCandidateWaiters := func() {
		candidatesReadyOnce.Do(func() { close(candidatesReady) })
		firstDeliveryOnce.Do(func() { close(firstDeliveryCompleted) })
	}
	var candidateMu sync.Mutex
	candidateCount := 0
	callback := "test:publication_dispatch_barrier:" + uuid.NewString()
	if err := fixture.db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != (model.ContentPublicationEvent{}).TableName() || tx.Statement.SQL.String() == "" {
			return
		}
		candidateMu.Lock()
		if candidateCount >= 2 {
			candidateMu.Unlock()
			return
		}
		candidateCount++
		candidate := candidateCount
		if candidate == 2 {
			candidatesReadyOnce.Do(func() { close(candidatesReady) })
		}
		candidateMu.Unlock()
		if candidate == 1 {
			select {
			case <-candidatesReady:
			case <-time.After(time.Second):
				tx.AddError(errors.New("second publication dispatch worker did not load a candidate"))
			}
			return
		}
		select {
		case <-firstDeliveryCompleted:
		case <-time.After(time.Second):
			tx.AddError(errors.New("first publication dispatch worker did not deliver the candidate"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Query().Remove(callback) })
	deliveryCallback := "test:publication_dispatch_first_delivery:" + uuid.NewString()
	if err := fixture.db.Callback().Update().After("gorm:update").Register(deliveryCallback, func(tx *gorm.DB) {
		if tx.Error != nil || tx.RowsAffected != 1 || tx.Statement.Schema == nil || tx.Statement.Schema.Table != (model.ContentPublicationEvent{}).TableName() {
			return
		}
		updates, _ := tx.Statement.Dest.(map[string]any)
		if updates["status"] == "delivered" {
			firstDeliveryOnce.Do(func() { close(firstDeliveryCompleted) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Update().Remove(deliveryCallback) })
	var workers sync.WaitGroup
	errs := make(chan error, 2)
	workers.Add(2)
	for range 2 {
		go func() {
			defer workers.Done()
			errs <- fixture.service.DispatchPendingPublications(10)
		}()
	}
	workersDone := make(chan struct{})
	go func() {
		workers.Wait()
		close(workersDone)
	}()
	t.Cleanup(func() {
		releaseCandidateWaiters()
		select {
		case <-workersDone:
		case <-time.After(time.Second):
			t.Error("concurrent dispatch workers did not exit during cleanup")
		}
	})
	<-workersDone
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	var notifications []model.Notification
	if err := fixture.db.Where("recipient_id = ? AND type = ?", fixture.viewer.ID, "content_published").Find(&notifications).Error; err != nil {
		t.Fatal(err)
	}
	if len(notifications) != 1 {
		t.Fatalf("expected one publication notification from concurrent workers, got %#v", notifications)
	}
	var event model.ContentPublicationEvent
	if err := fixture.db.Where("content_type = ? AND content_id = ?", "blog", fixture.post.ID).First(&event).Error; err != nil {
		t.Fatal(err)
	}
	if event.Status != "delivered" || event.LeaseVersion != 1 {
		t.Fatalf("expected concurrent event to be delivered, got %#v", event)
	}
}

func TestDispatchPendingPublicationsScansPastContendedCandidates(t *testing.T) {
	fixture := newLifecycleFixture(t)
	createdAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	posts := []model.Post{fixture.post}
	for range 3 {
		now := time.Now().UTC()
		post := model.Post{
			UserID: fixture.owner.ID, ChannelID: &fixture.channel.ID,
			Title: "Concurrent publication", Content: "body", Status: "published", Visibility: "public", PublishedAt: &now,
		}
		if err := fixture.db.Create(&post).Error; err != nil {
			t.Fatal(err)
		}
		seedLifecycleCanonicalBlog(t, fixture.db, post, uuid.Nil)
		posts = append(posts, post)
	}
	for index, post := range posts {
		event := model.ContentPublicationEvent{
			Base: model.Base{ID: uuid.MustParse([]string{
				"00000000-0000-0000-0000-000000000004",
				"00000000-0000-0000-0000-000000000003",
				"00000000-0000-0000-0000-000000000002",
				"00000000-0000-0000-0000-000000000001",
			}[index])},
			ChannelID: fixture.channel.ID, OwnerID: fixture.owner.ID,
			ContentType: "blog", ContentID: post.ID, Status: "pending",
		}
		if err := fixture.db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Model(&event).Update("created_at", createdAt).Error; err != nil {
			t.Fatal(err)
		}
	}

	laggingWorkerReadCandidates := make(chan struct{})
	releaseLaggingWorker := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseLaggingWorker) }) }
	callback := "test:block_lagging_publication_worker:" + uuid.NewString()
	var blockOnce sync.Once
	if err := fixture.db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != (model.ContentPublicationEvent{}).TableName() || tx.Statement.SQL.String() == "" {
			return
		}
		blocked := false
		blockOnce.Do(func() {
			blocked = true
			close(laggingWorkerReadCandidates)
		})
		if !blocked {
			return
		}
		select {
		case <-releaseLaggingWorker:
		case <-time.After(time.Second):
			tx.AddError(errors.New("lagging publication worker was not released"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Query().Remove(callback) })

	laggingResult := make(chan error, 1)
	laggingDone := make(chan struct{})
	go func() {
		defer close(laggingDone)
		laggingResult <- fixture.service.DispatchPendingPublications(2)
	}()
	t.Cleanup(func() {
		release()
		select {
		case <-laggingDone:
		case <-time.After(time.Second):
			t.Error("lagging publication worker did not exit during cleanup")
		}
	})
	select {
	case <-laggingWorkerReadCandidates:
	case <-time.After(time.Second):
		t.Fatal("lagging publication worker did not load candidates")
	}

	if err := fixture.service.DispatchPendingPublications(2); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-laggingResult; err != nil {
		t.Fatal(err)
	}

	var delivered int64
	if err := fixture.db.Model(&model.ContentPublicationEvent{}).Where("status = ?", "delivered").Count(&delivered).Error; err != nil {
		t.Fatal(err)
	}
	if delivered != 4 {
		t.Fatalf("expected both workers to deliver two events after contention, got %d", delivered)
	}
}

func TestDispatchPendingPublicationsDoesNotLetExpiredLeaseOverwriteNewLease(t *testing.T) {
	fixture := newLifecycleFixture(t)
	event := model.ContentPublicationEvent{
		ChannelID: fixture.channel.ID, OwnerID: fixture.owner.ID,
		ContentType: "blog", ContentID: fixture.post.ID, Status: "pending",
	}
	if err := fixture.db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}

	firstDispatchBlocked := make(chan struct{})
	releaseFirstDispatch := make(chan struct{})
	var releaseFirstDispatchOnce sync.Once
	releaseFirstDispatchFn := func() {
		releaseFirstDispatchOnce.Do(func() { close(releaseFirstDispatch) })
	}
	var once sync.Once
	callback := "test:block_first_publication_dispatch:" + uuid.NewString()
	if err := fixture.db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != (model.Subscription{}).TableName() || tx.Statement.SQL.String() == "" {
			return
		}
		blocked := false
		once.Do(func() {
			blocked = true
			close(firstDispatchBlocked)
		})
		if !blocked {
			return
		}
		<-releaseFirstDispatch
		tx.AddError(errors.New("first worker dispatch failed"))
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Query().Remove(callback) })

	firstResult := make(chan error, 1)
	firstDone := make(chan struct{})
	t.Cleanup(func() {
		releaseFirstDispatchFn()
		select {
		case <-firstDone:
		case <-time.After(time.Second):
			t.Error("first worker did not exit during cleanup")
		}
	})
	go func() {
		defer close(firstDone)
		firstResult <- fixture.service.DispatchPendingPublications(1)
	}()
	select {
	case <-firstDispatchBlocked:
	case <-time.After(time.Second):
		t.Fatal("first worker did not claim the event")
	}
	if err := fixture.db.Model(&model.ContentPublicationEvent{}).Where("id = ?", event.ID).Update("updated_at", time.Now().UTC().Add(-publicationProcessingTimeout-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.DispatchPendingPublications(1); err != nil {
		t.Fatal(err)
	}
	releaseFirstDispatchFn()
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}

	var stored model.ContentPublicationEvent
	if err := fixture.db.First(&stored, "id = ?", event.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "delivered" {
		t.Fatalf("stale lease overwrote the recovered worker result: %#v", stored)
	}
}

func TestDispatchPendingPublicationsDoesNotNotifyAfterLeaseIsReclaimed(t *testing.T) {
	fixture := newLifecycleFixture(t)
	source := model.FeedSource{SourceType: "internal_channel", SourceID: &fixture.channel.ID, Hash: uuid.NewString(), Title: fixture.channel.Name}
	if err := fixture.db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Create(&model.Subscription{UserID: fixture.viewer.ID, FeedSourceID: source.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.SaveNotificationPreference(fixture.viewer, NotificationPreferenceInput{
		SourceType: "internal_channel", SourceID: fixture.channel.ID, Mode: "all",
	}); err != nil {
		t.Fatal(err)
	}
	event := model.ContentPublicationEvent{
		ChannelID: fixture.channel.ID, OwnerID: fixture.owner.ID,
		ContentType: "blog", ContentID: fixture.post.ID, Status: "pending",
	}
	if err := fixture.db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}

	oldWorkerReady := make(chan struct{})
	releaseOldWorker := make(chan struct{})
	var releaseOldWorkerOnce sync.Once
	releaseOldWorkerFn := func() { releaseOldWorkerOnce.Do(func() { close(releaseOldWorker) }) }
	var pauseOldWorker sync.Once
	prefCallback := "test:pause_old_worker_before_notification:" + uuid.NewString()
	if err := fixture.db.Callback().Query().After("gorm:query").Register(prefCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != (model.ContentNotificationPreference{}).TableName() || tx.Statement.SQL.String() == "" {
			return
		}
		pause := false
		pauseOldWorker.Do(func() {
			pause = true
			close(oldWorkerReady)
		})
		if pause {
			<-releaseOldWorker
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Query().Remove(prefCallback) })

	var notificationCreates atomic.Int32
	createCallback := "test:count_publication_notification_creates:" + uuid.NewString()
	if err := fixture.db.Callback().Create().Before("gorm:create").Register(createCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == (model.Notification{}).TableName() {
			notificationCreates.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Create().Remove(createCallback) })

	oldResult := startDispatchWorker(t, func() error {
		return fixture.service.DispatchPendingPublications(1)
	}, releaseOldWorkerFn)
	select {
	case <-oldWorkerReady:
	case <-time.After(time.Second):
		t.Fatal("old worker did not reach notification dispatch")
	}
	if err := fixture.db.Model(&model.ContentPublicationEvent{}).Where("id = ?", event.ID).
		Update("updated_at", time.Now().UTC().Add(-publicationProcessingTimeout-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.DispatchPendingPublications(1); err != nil {
		t.Fatal(err)
	}
	releaseOldWorkerFn()
	if err := <-oldResult; err != nil {
		t.Fatal(err)
	}

	if got := notificationCreates.Load(); got != 1 {
		t.Fatalf("stale lease attempted %d notification writes, want 1 from the recovered worker", got)
	}
	var notifications int64
	if err := fixture.db.Model(&model.Notification{}).Where("recipient_id = ? AND type = ?", fixture.viewer.ID, "content_published").Count(&notifications).Error; err != nil {
		t.Fatal(err)
	}
	if notifications != 1 {
		t.Fatalf("expected one notification from the recovered worker, got %d", notifications)
	}
}

func TestDispatchPendingPublicationsRejectsCandidateStaleAfterRetry(t *testing.T) {
	fixture := newLifecycleFixture(t)
	event := model.ContentPublicationEvent{
		ChannelID: fixture.channel.ID, OwnerID: fixture.owner.ID,
		ContentType: "blog", ContentID: fixture.post.ID, Status: "pending",
	}
	if err := fixture.db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}

	candidateLoaded := make(chan struct{})
	releaseCandidate := make(chan struct{})
	var releaseCandidateOnce sync.Once
	releaseCandidateFn := func() { releaseCandidateOnce.Do(func() { close(releaseCandidate) }) }
	var once sync.Once
	callback := "test:block_stale_publication_candidate:" + uuid.NewString()
	if err := fixture.db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != (model.ContentPublicationEvent{}).TableName() || tx.Statement.SQL.String() == "" {
			return
		}
		blocked := false
		once.Do(func() {
			blocked = true
			close(candidateLoaded)
		})
		if !blocked {
			return
		}
		select {
		case <-releaseCandidate:
		case <-time.After(time.Second):
			tx.AddError(errors.New("stale publication candidate was not released"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Query().Remove(callback) })

	result := startDispatchWorker(t, func() error {
		return fixture.service.DispatchPendingPublications(1)
	}, releaseCandidateFn)
	select {
	case <-candidateLoaded:
	case <-time.After(time.Second):
		t.Fatal("worker did not load publication candidate")
	}
	// Another worker claims the event, fails, and returns it to pending before this worker claims it.
	if err := fixture.db.Model(&model.ContentPublicationEvent{}).Where("id = ?", event.ID).Updates(map[string]any{
		"status": "processing", "lease_version": gorm.Expr("lease_version + 1"),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.ContentPublicationEvent{}).Where("id = ?", event.ID).Update("status", "pending").Error; err != nil {
		t.Fatal(err)
	}
	releaseCandidateFn()
	if err := <-result; err != nil {
		t.Fatal(err)
	}

	var stored model.ContentPublicationEvent
	if err := fixture.db.First(&stored, "id = ?", event.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "pending" || stored.LeaseVersion != 1 {
		t.Fatalf("stale candidate must not acquire a newer lease: %#v", stored)
	}
}

func TestDispatchPublicationTreatsUniqueNotificationConflictAsSuccess(t *testing.T) {
	fixture := newLifecycleFixture(t)
	source := model.FeedSource{SourceType: "internal_channel", SourceID: &fixture.channel.ID, Hash: uuid.NewString(), Title: fixture.channel.Name}
	if err := fixture.db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Create(&model.Subscription{UserID: fixture.viewer.ID, FeedSourceID: source.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.SaveNotificationPreference(fixture.viewer, NotificationPreferenceInput{SourceType: "internal_channel", SourceID: fixture.channel.ID, Mode: "all"}); err != nil {
		t.Fatal(err)
	}

	createsReady := make(chan struct{})
	var creates sync.Mutex
	createCount := 0
	callback := "test:publication_notification_create_barrier:" + uuid.NewString()
	if err := fixture.db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != (model.Notification{}).TableName() {
			return
		}
		creates.Lock()
		createCount++
		if createCount == 2 {
			close(createsReady)
		}
		creates.Unlock()
		select {
		case <-createsReady:
		case <-time.After(time.Second):
			tx.AddError(errors.New("notification creates did not synchronize"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Create().Remove(callback) })

	event := model.ContentPublicationEvent{ChannelID: fixture.channel.ID, OwnerID: fixture.owner.ID, ContentType: "blog", ContentID: fixture.post.ID}
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- fixture.service.dispatchPublication(event) }()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("unique notification conflict must be idempotent, got %v", err)
		}
	}
}

func TestDispatchPendingPublicationsReclaimsExpiredProcessingEvent(t *testing.T) {
	fixture := newLifecycleFixture(t)
	expiredAt := time.Now().UTC().Add(-10 * time.Minute)
	event := model.ContentPublicationEvent{
		ChannelID: fixture.channel.ID, OwnerID: fixture.owner.ID,
		ContentType: "blog", ContentID: fixture.post.ID, Status: "processing",
	}
	if err := fixture.db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&event).Update("updated_at", expiredAt).Error; err != nil {
		t.Fatal(err)
	}

	if err := fixture.service.DispatchPendingPublications(10); err != nil {
		t.Fatal(err)
	}

	if err := fixture.db.First(&event, "id = ?", event.ID).Error; err != nil {
		t.Fatal(err)
	}
	if event.Status != "delivered" {
		t.Fatalf("expected expired processing event to be delivered, got %#v", event)
	}
}

func TestDispatchPendingPublicationsDoesNotClaimFreshProcessingEvent(t *testing.T) {
	fixture := newLifecycleFixture(t)
	source := model.FeedSource{SourceType: "internal_channel", SourceID: &fixture.channel.ID, Hash: uuid.NewString(), Title: fixture.channel.Name}
	if err := fixture.db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Create(&model.Subscription{UserID: fixture.viewer.ID, FeedSourceID: source.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.SaveNotificationPreference(fixture.viewer, NotificationPreferenceInput{
		SourceType: "internal_channel", SourceID: fixture.channel.ID, Mode: "all",
	}); err != nil {
		t.Fatal(err)
	}
	event := model.ContentPublicationEvent{
		ChannelID: fixture.channel.ID, OwnerID: fixture.owner.ID,
		ContentType: "blog", ContentID: fixture.post.ID, Status: "processing",
	}
	if err := fixture.db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}

	if err := fixture.service.DispatchPendingPublications(10); err != nil {
		t.Fatal(err)
	}

	if err := fixture.db.First(&event, "id = ?", event.ID).Error; err != nil {
		t.Fatal(err)
	}
	if event.Status != "processing" {
		t.Fatalf("expected fresh processing event to remain processing, got %#v", event)
	}
	var notifications int64
	if err := fixture.db.Model(&model.Notification{}).
		Where("recipient_id = ? AND type = ?", fixture.viewer.ID, "content_published").Count(&notifications).Error; err != nil {
		t.Fatal(err)
	}
	if notifications != 0 {
		t.Fatalf("expected fresh processing event not to create notifications, got %d", notifications)
	}
}

func TestDispatchPendingPublicationsFailsWhenContentIsMissing(t *testing.T) {
	fixture := newLifecycleFixture(t)
	event := model.ContentPublicationEvent{
		ChannelID: fixture.channel.ID, OwnerID: fixture.owner.ID,
		ContentType: "blog", ContentID: uuid.New(), Status: "pending",
	}
	if err := fixture.db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}

	if err := fixture.service.DispatchPendingPublications(10); err != nil {
		t.Fatal(err)
	}

	var stored model.ContentPublicationEvent
	if err := fixture.db.First(&stored, "id = ?", event.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "failed" {
		t.Fatalf("expected missing content event to be failed, got %q", stored.Status)
	}
	if stored.Attempts != 1 || stored.LastError != "lifecycle.content_not_found: Content not found" {
		t.Fatalf("unexpected terminal event: %#v", stored)
	}
}

func TestDispatchPendingPublicationsReturnsPublicationStateWritebackError(t *testing.T) {
	fixture := newLifecycleFixture(t)
	event := model.ContentPublicationEvent{
		ChannelID: fixture.channel.ID, OwnerID: fixture.owner.ID,
		ContentType: "blog", ContentID: uuid.New(), Status: "pending",
	}
	if err := fixture.db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}

	want := errors.New("publication state writeback failed")
	claimUpdates := 0
	stateWritebacks := 0
	callback := "test:fail_content_publication_event_writeback"
	if err := fixture.db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == (model.ContentPublicationEvent{}).TableName() {
			updates, _ := tx.Statement.Dest.(map[string]any)
			switch updates["status"] {
			case "processing":
				claimUpdates++
			case "failed":
				stateWritebacks++
				tx.AddError(want)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Update().Remove(callback) })

	err := fixture.service.DispatchPendingPublications(10)
	if !errors.Is(err, want) {
		t.Fatalf("expected publication state writeback error, got %v", err)
	}
	if claimUpdates != 1 || stateWritebacks != 1 {
		t.Fatalf("expected one successful claim followed by one failed state writeback, got claims=%d writebacks=%d", claimUpdates, stateWritebacks)
	}
}

func TestDispatchPendingPublicationsRetriesRecoverableDispatchError(t *testing.T) {
	fixture := newLifecycleFixture(t)
	if err := fixture.service.EnqueuePublication("blog", fixture.post.ID); err != nil {
		t.Fatal(err)
	}
	want := errors.New("subscriptions unavailable")
	callback := "test:fail_publication_recipients_query"
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == (model.Subscription{}).TableName() {
			tx.AddError(want)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Query().Remove(callback) })

	if err := fixture.service.DispatchPendingPublications(10); err != nil {
		t.Fatal(err)
	}

	var event model.ContentPublicationEvent
	if err := fixture.db.Where("content_type = ? AND content_id = ?", "blog", fixture.post.ID).First(&event).Error; err != nil {
		t.Fatal(err)
	}
	if event.Status != "pending" || event.Attempts != 1 {
		t.Fatalf("expected recoverable failure to remain pending after one attempt, got %#v", event)
	}

	if err := fixture.db.Callback().Query().Remove(callback); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.DispatchPendingPublications(10); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&event, "id = ?", event.ID).Error; err != nil {
		t.Fatal(err)
	}
	if event.Status != "delivered" || event.Attempts != 1 {
		t.Fatalf("expected retried event to be delivered without another failed attempt, got %#v", event)
	}
}

func TestRetryBlogScheduleStopsAtTwentyFourHourDeadline(t *testing.T) {
	fixture := newLifecycleFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	deadline := now.Add(24 * time.Hour)
	schedule := model.BlogPublishSchedule{
		ContentID: fixture.post.ID, AuthorID: fixture.owner.ID, PublishAt: now,
		Timezone: "UTC", Status: "processing", LeaseToken: "lease", LeaseUntil: &deadline,
		Attempts: 5, NextRunAt: now,
	}
	if err := fixture.db.Create(&schedule).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.retryBlogSchedule(schedule.ID, "lease", now.Add(14*time.Hour+30*time.Minute), errors.New("publish unavailable")); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&schedule, "id = ?", schedule.ID).Error; err != nil {
		t.Fatal(err)
	}
	if schedule.Status != "pending" || !schedule.NextRunAt.Equal(deadline) {
		t.Fatalf("expected final retry at deadline, got %#v", schedule)
	}
	schedule.Status = "processing"
	schedule.LeaseToken = "deadline-lease"
	schedule.LeaseUntil = &deadline
	if err := fixture.db.Save(&schedule).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.retryBlogSchedule(schedule.ID, "deadline-lease", deadline, errors.New("publish unavailable")); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&schedule, "id = ?", schedule.ID).Error; err != nil {
		t.Fatal(err)
	}
	if schedule.Status != "failed" {
		t.Fatalf("expected failed schedule after deadline, got %#v", schedule)
	}
}

func TestGetBlogScheduleEnforcesOwnershipAndSupportsAdmin(t *testing.T) {
	fixture := newLifecycleFixture(t)
	publishAt := time.Now().UTC().Add(time.Hour)
	if _, err := fixture.service.ScheduleContent(fixture.owner, ScheduleInput{Module: "blog", ContentID: fixture.post.ID, PublishAt: publishAt, Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	status, err := fixture.service.GetBlogSchedule(fixture.owner, fixture.post.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "pending" || status.ContentID != fixture.post.ID || status.PublishAt.UTC().Unix() != publishAt.UTC().Unix() {
		t.Fatalf("unexpected owner schedule status: %#v", status)
	}
	if _, err := fixture.service.GetBlogSchedule(fixture.viewer, fixture.post.ID); err == nil {
		t.Fatal("expected non-owner schedule access to fail")
	}
	admin := fixture.viewer
	admin.Role = authctx.RoleAdmin
	if _, err := fixture.service.GetBlogSchedule(admin, fixture.post.ID); err != nil {
		t.Fatalf("expected administrator schedule access: %v", err)
	}
	if _, err := fixture.service.GetBlogSchedule(fixture.owner, uuid.New()); err == nil {
		t.Fatal("expected missing schedule content to fail")
	}
}

func TestSchedulePublishesDueContentAndEnqueuesPublication(t *testing.T) {
	fixture := newLifecycleFixture(t)
	if err := fixture.db.Model(&model.ContentEntry{}).Where("id = ?", fixture.post.ID).Updates(map[string]any{"status": "draft", "published_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(time.Hour)
	result, err := fixture.service.ScheduleContent(fixture.owner, ScheduleInput{Module: "blog", ContentID: fixture.post.ID, PublishAt: due})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "scheduled" || !result.PublishAt.Equal(due) {
		t.Fatalf("unexpected schedule result: %#v", result)
	}
	if err := fixture.service.PublishDueBlogSchedules(due.Add(-time.Second), 10); err != nil {
		t.Fatal(err)
	}
	var before model.ContentEntry
	if err := fixture.db.First(&before, "id = ?", fixture.post.ID).Error; err != nil {
		t.Fatal(err)
	}
	if before.Status != "scheduled" {
		t.Fatalf("content published early: %#v", before)
	}
	if err := fixture.service.PublishDueBlogSchedules(due.Add(time.Second), 10); err != nil {
		t.Fatal(err)
	}
	var after model.ContentEntry
	if err := fixture.db.First(&after, "id = ?", fixture.post.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status != "published" || after.PublishedAt == nil || after.ScheduledAt != nil {
		t.Fatalf("content was not published: %#v", after)
	}
	var schedule model.BlogPublishSchedule
	if err := fixture.db.Where("content_id = ?", fixture.post.ID).First(&schedule).Error; err != nil {
		t.Fatal(err)
	}
	if schedule.Status != "published" || schedule.PublishedAt == nil {
		t.Fatalf("schedule was not completed: %#v", schedule)
	}
	var publications int64
	if err := fixture.db.Model(&model.ContentPublicationEvent{}).Where("content_type = ? AND content_id = ?", "blog", fixture.post.ID).Count(&publications).Error; err != nil {
		t.Fatal(err)
	}
	if publications != 1 {
		t.Fatalf("expected publication event, got %d", publications)
	}
}
