package music

import (
	"net/http"
	"strings"
	"testing"

	"atoman/internal/model"

	"github.com/google/uuid"
)

func TestRecordMusicRecommendationEventsStoresValidatedBatch(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	requestID := uuid.New()
	albumID := uuid.New()
	songID := uuid.New()

	input := RecordMusicRecommendationEventsRequest{
		RequestID: requestID.String(),
		Surface:   musicRecommendationSurfaceHome,
		Events: []MusicRecommendationEventInput{
			{Event: MusicRecommendationEventImpression, EntityType: "album", EntityID: albumID, Position: 1, Reason: "结合你的音乐记录"},
			{Event: MusicRecommendationEventPlayStart, EntityType: "song", EntityID: songID, Position: 1},
		},
	}
	if err := service.RecordMusicRecommendationEvents(user, input); err != nil {
		t.Fatalf("record recommendation events: %v", err)
	}

	var events []model.MusicRecommendationEvent
	if err := db.Where("user_id = ? AND request_id = ?", user.ID, requestID).Order("created_at ASC").Find(&events).Error; err != nil {
		t.Fatalf("load recommendation events: %v", err)
	}
	if len(events) != 2 || events[0].Event != string(MusicRecommendationEventImpression) || events[1].EntityType != "song" {
		t.Fatalf("unexpected recommendation events: %#v", events)
	}

	invalid := input
	invalid.RequestID = "invalid"
	if err := service.RecordMusicRecommendationEvents(user, invalid); err == nil {
		t.Fatal("expected invalid request id to fail")
	}
	invalid = input
	invalid.Surface = "profile"
	if err := service.RecordMusicRecommendationEvents(user, invalid); err == nil {
		t.Fatal("expected unsupported surface to fail")
	}
}

func TestRegisterRoutesMusicRecommendationEventsRequireAuth(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	requestID := uuid.NewString()
	body := `{"request_id":"` + requestID + `","surface":"music_home","events":[{"event":"click","entity_type":"album","entity_id":"` + uuid.NewString() + `","position":1}]}`
	path := "/api/v1/music/recommendation-events"

	anonymous := performMusicJSONRequest(t, newMusicHTTPRouter(service, nil), http.MethodPost, path, body)
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("expected anonymous request 401, got %d: %s", anonymous.Code, anonymous.Body.String())
	}

	response := performMusicJSONRequest(t, newMusicHTTPRouter(service, &user), http.MethodPost, path, body)
	if response.Code != http.StatusNoContent {
		t.Fatalf("expected authenticated request 204, got %d: %s", response.Code, response.Body.String())
	}
	var count int64
	if err := db.Model(&model.MusicRecommendationEvent{}).Where("user_id = ?", user.ID).Count(&count).Error; err != nil {
		t.Fatalf("count recommendation events: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one stored recommendation event, got %d", count)
	}

	tooMany := strings.Repeat(`{"event":"click","entity_type":"album","entity_id":"`+uuid.NewString()+`"},`, musicRecommendationEventBatchLimit)
	tooMany = `{"request_id":"` + requestID + `","surface":"music_home","events":[` + tooMany + `{"event":"click","entity_type":"album","entity_id":"` + uuid.NewString() + `"}]}`
	rejected := performMusicJSONRequest(t, newMusicHTTPRouter(service, &user), http.MethodPost, path, tooMany)
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("expected oversized batch 400, got %d: %s", rejected.Code, rejected.Body.String())
	}
}
