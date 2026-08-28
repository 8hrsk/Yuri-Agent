package domain

import (
	"testing"
	"time"
)

func TestNotificationValidationRequiresExplainableSource(t *testing.T) {
	notification := Notification{
		ID:        ID("notification-1"),
		Type:      NotificationTypeTaskCompleted,
		Title:     "Task complete",
		Body:      "The report is ready.",
		Source:    NotificationSource{Kind: "schedule", ID: "schedule-1", Reason: "The scheduled report finished."},
		CreatedAt: time.Unix(100, 0),
	}
	if err := notification.Validate(); err != nil {
		t.Fatalf("valid notification rejected: %v", err)
	}
	notification.Source.Reason = ""
	if notification.Valid() {
		t.Fatal("notification without a source reason was accepted")
	}
}

func TestNotificationTypeAllowsNamespacedValues(t *testing.T) {
	for _, value := range []NotificationType{"plugin.calendar.event", "custom-type"} {
		if !value.Valid() {
			t.Errorf("%q should be valid", value)
		}
	}
	for _, value := range []NotificationType{"", " task", "task\n"} {
		if value.Valid() {
			t.Errorf("%q should be invalid", value)
		}
	}
}
