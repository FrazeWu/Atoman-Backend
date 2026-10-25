package handlers

import "atoman/internal/model"

type timelineEventDTO struct {
	model.TimelineEvent
	User *model.PublicUserDTO `json:"user,omitempty"`
}

func timelineEventFromModel(event model.TimelineEvent) timelineEventDTO {
	return timelineEventDTO{TimelineEvent: event, User: model.PublicUserFromUser(event.User)}
}

func timelineEventsFromModels(events []model.TimelineEvent) []timelineEventDTO {
	result := make([]timelineEventDTO, 0, len(events))
	for _, event := range events {
		result = append(result, timelineEventFromModel(event))
	}
	return result
}

type timelinePersonDTO struct {
	model.TimelinePerson
	User *model.PublicUserDTO `json:"user,omitempty"`
}

func timelinePersonFromModel(person model.TimelinePerson) timelinePersonDTO {
	return timelinePersonDTO{TimelinePerson: person, User: model.PublicUserFromUser(person.User)}
}

func timelinePersonsFromModels(persons []model.TimelinePerson) []timelinePersonDTO {
	result := make([]timelinePersonDTO, 0, len(persons))
	for _, person := range persons {
		result = append(result, timelinePersonFromModel(person))
	}
	return result
}

type timelineRevisionDTO struct {
	model.TimelineRevision
	Editor *model.PublicUserDTO `json:"editor,omitempty"`
}

func timelineRevisionsFromModels(revisions []model.TimelineRevision) []timelineRevisionDTO {
	result := make([]timelineRevisionDTO, 0, len(revisions))
	for _, revision := range revisions {
		result = append(result, timelineRevisionDTO{
			TimelineRevision: revision,
			Editor:           model.PublicUserFromUser(revision.Editor),
		})
	}
	return result
}
