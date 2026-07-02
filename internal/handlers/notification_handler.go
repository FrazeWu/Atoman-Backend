package handlers

import (
	"github.com/google/uuid"

	"atoman/internal/collab"
	"atoman/internal/model"
)

func WsPushNotif(userHub *collab.UserHub) func(uuid.UUID, *model.Notification) {
	return func(recipientID uuid.UUID, notif *model.Notification) {
		if userHub == nil || notif == nil {
			return
		}
		userHub.Push(recipientID, "notification", notif)
	}
}
