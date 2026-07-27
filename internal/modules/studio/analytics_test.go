package studio

import (
	"fmt"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
)

func TestStudioAnalyticsUsesEventTimeForSevenTwentyEightAndNinetyDays(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	now := time.Now().UTC()
	contentID := fixture.channel.ID
	for _, input := range []struct {
		age time.Duration
	}{
		{age: 6 * 24 * time.Hour},
		{age: 20 * 24 * time.Hour},
		{age: 60 * 24 * time.Hour},
	} {
		event := model.StudioMetricEvent{
			Base: model.Base{CreatedAt: now.Add(-input.age)}, ChannelID: fixture.channel.ID,
			ContentType: string(ModuleBlog), ContentID: contentID, Metric: "view",
		}
		if err := fixture.db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
	}

	for _, test := range []struct {
		days int
		want int64
	}{{days: 7, want: 1}, {days: 28, want: 2}, {days: 90, want: 3}} {
		analytics, err := fixture.service.GetAnalytics(fixture.user, ModuleBlog, AnalyticsQuery{
			ChannelID: fixture.channel.ID, Range: test.days,
		})
		if err != nil {
			t.Fatalf("get %d-day analytics: %v", test.days, err)
		}
		if analytics.Totals["view"] != test.want || len(analytics.Trend) != test.days {
			t.Fatalf("expected %d-day views=%d and complete trend, got %#v", test.days, test.want, analytics)
		}
	}
}

func TestStudioAnalyticsIncludesLifecycleFunnelEvents(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	collection := fixture.collections[ModuleBlog]
	post := model.Post{
		UserID: fixture.user.ID, ChannelID: &fixture.channel.ID, CollectionID: &collection.ID,
		Title: "Funnel post", Content: "body", Status: "published", Visibility: "public",
	}
	if err := fixture.db.Create(&post).Error; err != nil {
		t.Fatal(err)
	}
	for _, metric := range []string{"impression", "open", "engaged", "complete"} {
		event := model.ContentLifecycleEvent{
			ChannelID: fixture.channel.ID, ContentType: "blog", ContentID: post.ID,
			Event: metric, Source: "home", ClientEventID: metric + "-event",
		}
		if err := fixture.db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
	}

	analytics, err := fixture.service.GetAnalytics(fixture.user, ModuleBlog, AnalyticsQuery{ChannelID: fixture.channel.ID, Range: 7})
	if err != nil {
		t.Fatal(err)
	}
	for _, metric := range []string{"impression", "open", "engaged", "complete"} {
		if analytics.Totals[metric] != 1 {
			t.Fatalf("expected %s total 1, got %#v", metric, analytics.Totals)
		}
		if analytics.Top[0].Metrics[metric] != 1 {
			t.Fatalf("expected %s content total 1, got %#v", metric, analytics.Top[0].Metrics)
		}
	}
	if len(analytics.Sources) != 1 || analytics.Sources[0].Source != "home" || analytics.Sources[0].Count != 4 {
		t.Fatalf("expected home source count 4, got %#v", analytics.Sources)
	}
}

func TestStudioShareRecordsEventAndRejectsPrivateContent(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	collection := fixture.collections[ModuleBlog]
	publicPost := model.Post{
		UserID: fixture.user.ID, ChannelID: &fixture.channel.ID, CollectionID: &collection.ID,
		Title: "Shareable", Content: "body", Status: "published", Visibility: "public",
	}
	privatePost := model.Post{
		UserID: fixture.user.ID, ChannelID: &fixture.channel.ID, CollectionID: &collection.ID,
		Title: "Private", Content: "body", Status: "published", Visibility: "private",
	}
	if err := fixture.db.Create(&publicPost).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Create(&privatePost).Error; err != nil {
		t.Fatal(err)
	}

	shared, err := fixture.service.ShareContent(fixture.user, ModuleBlog, fixture.channel.ID, publicPost.ID)
	if err != nil {
		t.Fatalf("share public post: %v", err)
	}
	if shared.Path != "/posts/post/"+publicPost.ID.String() {
		t.Fatalf("unexpected share path: %#v", shared)
	}
	var count int64
	if err := fixture.db.Model(&model.StudioMetricEvent{}).
		Where("channel_id = ? AND content_type = ? AND content_id = ? AND metric = ?", fixture.channel.ID, ModuleBlog, publicPost.ID, "share").
		Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("expected one share event, count=%d err=%v", count, err)
	}
	_, err = fixture.service.ShareContent(fixture.user, ModuleBlog, fixture.channel.ID, privatePost.ID)
	appErr := apperr.FromError(err)
	if appErr == nil || appErr.HTTPStatus != 400 {
		t.Fatalf("expected private share 400, got %v", err)
	}
}

func TestStudioAnalyticsCalculatesReturningConsumersAcrossDays(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	collection := fixture.collections[ModuleBlog]
	post := model.Post{UserID: fixture.user.ID, ChannelID: &fixture.channel.ID, CollectionID: &collection.ID, Title: "Retention", Content: "body", Status: "published", Visibility: "public"}
	if err := fixture.db.Create(&post).Error; err != nil {
		t.Fatal(err)
	}
	viewerID := fixture.foreignUser.UUID
	for index, age := range []time.Duration{24 * time.Hour, 0} {
		event := model.ContentLifecycleEvent{
			Base: model.Base{CreatedAt: time.Now().UTC().Add(-age)}, UserID: &viewerID,
			ChannelID: fixture.channel.ID, ContentType: "blog", ContentID: post.ID,
			Event: "open", Source: "direct", ClientEventID: fmt.Sprintf("retention-%d", index),
		}
		if err := fixture.db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
	}

	analytics, err := fixture.service.GetAnalytics(fixture.user, ModuleBlog, AnalyticsQuery{ChannelID: fixture.channel.ID, Range: 7})
	if err != nil {
		t.Fatal(err)
	}
	if analytics.Retention.Consumers != 1 || analytics.Retention.ReturningConsumers != 1 || analytics.Retention.Rate != 100 {
		t.Fatalf("unexpected retention: %#v", analytics.Retention)
	}
}
