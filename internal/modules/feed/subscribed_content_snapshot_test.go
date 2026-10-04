package feed

import (
	"testing"

	"atoman/internal/model"
	"github.com/google/uuid"
)

func TestBuildSubscribedTimelineItemsPreservesContentTypes(t *testing.T) {
	items := buildSubscribedTimelineItems(
		[]model.Post{{Title: "post"}},
		nil,
		nil,
		[]model.ShortNote{{Content: "note"}},
		map[uuid.UUID]bool{},
		map[uuid.UUID]PostEngagementCount{},
		map[uuid.UUID]model.PodcastEpisode{},
		map[uuid.UUID]bool{},
	)
	if len(items) != 2 || items[0].Type != "short_note" || items[1].Type != "post" {
		t.Fatalf("unexpected timeline items: %#v", items)
	}
}
