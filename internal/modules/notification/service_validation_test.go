package notification

import "testing"

func TestNotificationInputBounds(t *testing.T) {
	if !validNotificationType("comment_like") || validNotificationType("made_up_type") {
		t.Fatal("notification type allowlist is incorrect")
	}
	if maxNotificationPreferences != 100 || maxNotificationEventType != 64 || maxNotificationMuteReason != 255 {
		t.Fatal("notification input bounds changed unexpectedly")
	}
}
