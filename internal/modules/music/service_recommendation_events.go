package music

import (
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
)

const (
	musicRecommendationEventBatchLimit = 32
	musicRecommendationSurfaceHome     = "music_home"
)

func (s *Service) RecordMusicRecommendationEvents(user authctx.CurrentUser, input RecordMusicRecommendationEventsRequest) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if len(input.Events) == 0 || len(input.Events) > musicRecommendationEventBatchLimit {
		return apperr.BadRequest("validation.invalid_request", "events must contain between 1 and 32 items")
	}
	requestID, err := uuid.Parse(strings.TrimSpace(input.RequestID))
	if err != nil {
		return apperr.BadRequest("validation.invalid_request", "request_id must be a valid UUID")
	}
	surface := strings.TrimSpace(input.Surface)
	if surface != musicRecommendationSurfaceHome {
		return apperr.BadRequest("validation.invalid_request", "surface is not supported")
	}

	events := make([]model.MusicRecommendationEvent, 0, len(input.Events))
	for _, event := range input.Events {
		if !isMusicRecommendationEvent(event.Event) {
			return apperr.BadRequest("validation.invalid_request", "event is not supported")
		}
		if event.EntityType != "album" && event.EntityType != "song" {
			return apperr.BadRequest("validation.invalid_request", "entity_type must be album or song")
		}
		if event.EntityID == uuid.Nil {
			return apperr.BadRequest("validation.invalid_request", "entity_id is required")
		}
		if event.Position < -1 || event.Position > 1000 {
			return apperr.BadRequest("validation.invalid_request", "position is out of range")
		}
		if len([]rune(event.Reason)) > 256 {
			return apperr.BadRequest("validation.invalid_request", "reason is too long")
		}
		events = append(events, model.MusicRecommendationEvent{
			UserID:     user.ID,
			RequestID:  requestID,
			Surface:    surface,
			Event:      string(event.Event),
			EntityType: event.EntityType,
			EntityID:   event.EntityID,
			Position:   event.Position,
			Reason:     strings.TrimSpace(event.Reason),
		})
	}

	return s.db.Create(&events).Error
}

func isMusicRecommendationEvent(event MusicRecommendationEventName) bool {
	switch event {
	case MusicRecommendationEventImpression,
		MusicRecommendationEventClick,
		MusicRecommendationEventPlayStart,
		MusicRecommendationEventPlayComplete,
		MusicRecommendationEventSkip:
		return true
	default:
		return false
	}
}
