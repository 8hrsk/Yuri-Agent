package domain

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// NotificationType identifies the kind of proactive delivery. Built-in
// values cover the first scheduler and plugin integrations; namespaced custom
// values are allowed so plugins can introduce their own notification types.
type NotificationType string

const (
	NotificationTypeTaskCompleted       NotificationType = "task.completed"
	NotificationTypeBackgroundCompleted NotificationType = "background.completed"
	NotificationTypePluginEvent         NotificationType = "plugin.event"
	NotificationTypeRuleTriggered       NotificationType = "rule.triggered"
	NotificationTypeAgentMessage        NotificationType = "agent.message"
)

func (t NotificationType) Valid() bool {
	value := strings.TrimSpace(string(t))
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character == ' ' || character == '\t' || character == '\n' || character == '\r' {
			return false
		}
	}
	return value == string(t)
}

// NotificationSource explains why Yuri is initiating a delivery. Reason is
// deliberately required so a notification cannot become an unexplained
// ambient interruption. ID and Label are optional because manual and system
// triggers may not have a durable source entity.
type NotificationSource struct {
	Kind   string `json:"kind"`
	ID     string `json:"id,omitempty"`
	Label  string `json:"label,omitempty"`
	Reason string `json:"reason"`
}

const (
	NotificationSourceSchedule   = "schedule"
	NotificationSourcePlugin     = "plugin"
	NotificationSourceBackground = "background"
	NotificationSourceRule       = "rule"
	NotificationSourceManual     = "manual"
	NotificationSourceSystem     = "system"
)

func (s NotificationSource) Valid() bool {
	kind := strings.TrimSpace(s.Kind)
	reason := strings.TrimSpace(s.Reason)
	if kind == "" || reason == "" || len(kind) > 128 || len(reason) > 2048 {
		return false
	}
	for _, character := range kind {
		if character == ' ' || character == '\t' || character == '\n' || character == '\r' {
			return false
		}
	}
	return kind == s.Kind
}

// Notification is the provider-neutral payload delivered to the local
// notifier. It contains no platform-specific fields, making it suitable for
// a Wails runtime event, a native macOS notification adapter, or tests.
type Notification struct {
	ID             ID                 `json:"id"`
	Type           NotificationType   `json:"type"`
	Title          string             `json:"title"`
	Body           string             `json:"body"`
	Source         NotificationSource `json:"source"`
	CreatedAt      time.Time          `json:"created_at"`
	ConversationID ID                 `json:"conversation_id,omitempty"`
	DeepLink       string             `json:"deep_link,omitempty"`
}

func (n Notification) Valid() bool {
	if n.ID.Empty() || !n.Type.Valid() || strings.TrimSpace(n.Title) == "" || strings.TrimSpace(n.Body) == "" || !n.Source.Valid() || n.CreatedAt.IsZero() {
		return false
	}
	return len(n.Title) <= 512 && len(n.Body) <= 32*1024
}

func (n Notification) Validate() error {
	if !n.Valid() {
		return fmt.Errorf("%w: invalid notification", ErrInvalidArgument)
	}
	return nil
}

// Notifier is the side-effect boundary for local delivery. Implementations
// may bridge to Wails events, UserNotifications on macOS, or a test sink.
// The policy must be evaluated before Notify is called.
type Notifier interface {
	Notify(context.Context, Notification) error
}
