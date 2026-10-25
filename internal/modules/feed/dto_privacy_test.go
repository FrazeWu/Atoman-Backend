package feed

import (
	"encoding/json"
	"strings"
	"testing"

	"atoman/internal/model"
)

func TestTimelinePostDTODoesNotExposePrivateUserFields(t *testing.T) {
	user := &model.User{Username: "feed-author", Email: "private-feed-author@example.test", Password: "private-hash"}
	channel := &model.Channel{User: user}
	post := model.Post{
		User:        user,
		Channel:     channel,
		Collection:  &model.Collection{Channel: channel},
		Collections: []model.Collection{{Channel: channel}},
	}

	payload, err := json.Marshal(timelinePostDTO(post, PostEngagementCount{}))
	if err != nil {
		t.Fatalf("marshal timeline post: %v", err)
	}
	for _, privateField := range []string{`"email"`, `"password"`, `"auth_version"`} {
		if strings.Contains(string(payload), privateField) {
			t.Fatalf("timeline post response includes private field %s: %s", privateField, payload)
		}
	}
}

func TestTimelineContentDTOsDoNotExposePrivateUserFields(t *testing.T) {
	user := &model.User{Username: "feed-author", Email: "private-feed-author@example.test", Password: "private-hash"}
	channel := &model.Channel{User: user}
	post := model.Post{User: user, Channel: channel, Collection: &model.Collection{Channel: channel}}
	video := model.Video{User: user, Channel: channel, Collection: &model.Collection{Channel: channel}, Collections: []model.Collection{{Channel: channel}}}
	note := model.ShortNote{User: user}
	episode := model.PodcastEpisode{Post: &post, Channel: channel}

	items := []any{
		timelineShortNoteDTO(note),
		timelinePodcastEpisodeDTO(episode),
		timelineVideoDTO(video),
	}
	for _, item := range items {
		payload, err := json.Marshal(item)
		if err != nil {
			t.Fatalf("marshal timeline item: %v", err)
		}
		for _, privateField := range []string{`"email"`, `"password"`, `"auth_version"`} {
			if strings.Contains(string(payload), privateField) {
				t.Fatalf("timeline response includes private field %s: %s", privateField, payload)
			}
		}
	}
}
