package studio

import (
	"testing"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
)

func TestStudioCalendarReturnsScheduledContentWithPreflightHints(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	from := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)
	inside := from.Add(8 * 24 * time.Hour)
	outside := to.Add(24 * time.Hour)

	blog := model.Post{
		UserID: fixture.user.ID, ChannelID: &fixture.channel.ID,
		Title: "Scheduled blog", Content: "body", Status: "scheduled", Visibility: "public", ScheduledAt: &inside,
	}
	if err := fixture.db.Create(&blog).Error; err != nil {
		t.Fatal(err)
	}
	video := model.Video{
		UserID: fixture.user.ID, ChannelID: &fixture.channel.ID, Title: "Scheduled video",
		StorageType: "external", Status: "scheduled", Visibility: "public", ScheduledAt: &inside,
		ProcessingStatus: "failed",
	}
	if err := fixture.db.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	outOfRange := model.Post{
		UserID: fixture.user.ID, ChannelID: &fixture.channel.ID,
		Title: "Later", Content: "body", Status: "scheduled", Visibility: "public", ScheduledAt: &outside,
	}
	if err := fixture.db.Create(&outOfRange).Error; err != nil {
		t.Fatal(err)
	}

	items, err := fixture.service.ListCalendar(fixture.user, StudioCalendarQuery{
		ChannelID: fixture.channel.ID, From: from, To: to,
	})
	if err != nil {
		t.Fatalf("list calendar: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected two in-range scheduled items, got %#v", items)
	}
	itemsByID := make(map[uuid.UUID]StudioCalendarItem, len(items))
	for _, item := range items {
		itemsByID[item.ID] = item
	}
	if !calendarItemHasIssues(itemsByID[blog.ID], "missing_cover", "missing_collection") {
		t.Fatalf("expected blog preflight hints, got %#v", itemsByID[blog.ID])
	}
	if !calendarItemHasIssues(itemsByID[video.ID], "missing_cover", "missing_collection", "processing_failed", "external_unplayable") {
		t.Fatalf("expected video preflight hints, got %#v", itemsByID[video.ID])
	}
}

func TestStudioCalendarRejectsInvalidRange(t *testing.T) {
	fixture := newStudioQueryFixture(t)
	now := time.Now().UTC()
	_, err := fixture.service.ListCalendar(fixture.user, StudioCalendarQuery{
		ChannelID: fixture.channel.ID, From: now, To: now,
	})
	if err == nil {
		t.Fatal("expected invalid calendar range error")
	}
}

func calendarItemHasIssues(item StudioCalendarItem, want ...string) bool {
	got := make(map[string]bool, len(item.Preflight))
	for _, issue := range item.Preflight {
		got[issue.Code] = true
	}
	for _, code := range want {
		if !got[code] {
			return false
		}
	}
	return true
}
