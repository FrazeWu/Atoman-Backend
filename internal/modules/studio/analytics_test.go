package studio

import (
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
	if shared.Path != "/post/"+publicPost.ID.String() {
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
