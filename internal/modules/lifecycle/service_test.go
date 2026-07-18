package lifecycle

import (
	"testing"
	"time"

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

func newLifecycleFixture(t *testing.T) lifecycleFixture {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Channel{}, &model.Collection{}, &model.Post{}, &model.PodcastEpisode{}, &model.Video{},
		&model.ContentLifecycleEvent{}, &model.ContentProgress{}, &model.ContentNotificationPreference{},
		&model.ContentPublicationEvent{}, &model.FeedSource{}, &model.Subscription{}, &model.Follow{}, &model.Notification{},
	)
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
	now := time.Now().UTC()
	post := model.Post{
		UserID: ownerModel.UUID, ChannelID: &channel.ID, CollectionID: &collection.ID,
		Title: "Lifecycle article", Content: "body", Status: "published", Visibility: "public", PublishedAt: &now,
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatal(err)
	}
	return lifecycleFixture{
		db: db, service: NewService(db), channel: channel, post: post,
		owner:  authctx.CurrentUser{ID: ownerModel.UUID, Username: ownerModel.Username, Role: authctx.RoleUser},
		viewer: authctx.CurrentUser{ID: viewerModel.UUID, Username: viewerModel.Username, Role: authctx.RoleUser},
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

func TestSchedulePublishesDueContentAndEnqueuesPublication(t *testing.T) {
	fixture := newLifecycleFixture(t)
	if err := fixture.db.Model(&fixture.post).Updates(map[string]any{"status": "draft", "published_at": nil}).Error; err != nil {
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
	if err := fixture.service.PublishDue(due.Add(-time.Second), 10); err != nil {
		t.Fatal(err)
	}
	var before model.Post
	if err := fixture.db.First(&before, "id = ?", fixture.post.ID).Error; err != nil {
		t.Fatal(err)
	}
	if before.Status != "scheduled" {
		t.Fatalf("content published early: %#v", before)
	}
	if err := fixture.service.PublishDue(due.Add(time.Second), 10); err != nil {
		t.Fatal(err)
	}
	var after model.Post
	if err := fixture.db.First(&after, "id = ?", fixture.post.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status != "published" || after.PublishedAt == nil || after.ScheduledAt != nil {
		t.Fatalf("content was not published: %#v", after)
	}
	var publications int64
	if err := fixture.db.Model(&model.ContentPublicationEvent{}).Where("content_type = ? AND content_id = ?", "blog", fixture.post.ID).Count(&publications).Error; err != nil {
		t.Fatal(err)
	}
	if publications != 1 {
		t.Fatalf("expected publication event, got %d", publications)
	}
}
