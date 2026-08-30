package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/proactivity"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const notificationEventName = "yuri:notification"

type ProactivitySettingsView struct {
	Enabled                       bool   `json:"enabled"`
	QuietHoursEnabled             bool   `json:"quietHoursEnabled"`
	QuietHoursStart               string `json:"quietHoursStart"`
	QuietHoursEnd                 string `json:"quietHoursEnd"`
	Timezone                      string `json:"timezone"`
	DailyLimit                    int    `json:"dailyLimit"`
	CooldownMinutes               int    `json:"cooldownMinutes"`
	AllowLocalNotifications       bool   `json:"allowLocalNotifications"`
	AutonomousPeerDialogues       bool   `json:"autonomousPeerDialogues"`
	AutonomousPeerDailyLimit      int    `json:"autonomousPeerDailyLimit"`
	AutonomousPeerCooldownMinutes int    `json:"autonomousPeerCooldownMinutes"`
}

type NotificationEventView struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	Source         string `json:"source"`
	Reason         string `json:"reason"`
	ConversationID string `json:"conversationId,omitempty"`
	DeepLink       string `json:"deepLink,omitempty"`
	AllowNative    bool   `json:"allowNative"`
	CreatedAt      string `json:"createdAt"`
}

func proactivitySettings(value config.ProactivityConfig) proactivity.Settings {
	settings := proactivity.Settings{
		Enabled: value.Enabled, Timezone: value.Timezone, DailyLimit: value.DailyLimit,
		Cooldowns: make(map[domain.NotificationType]time.Duration),
	}
	if value.QuietHoursEnabled {
		settings.QuietHours = []proactivity.QuietHours{{Start: value.QuietHoursStart, End: value.QuietHoursEnd}}
	}
	cooldown := time.Duration(value.CooldownMinutes) * time.Minute
	for _, notificationType := range []domain.NotificationType{
		domain.NotificationTypeTaskCompleted, domain.NotificationTypeBackgroundCompleted,
		domain.NotificationTypePluginEvent, domain.NotificationTypeRuleTriggered, domain.NotificationTypeAgentMessage,
	} {
		settings.Cooldowns[notificationType] = cooldown
	}
	return settings
}

func (b *Bridge) GetProactivitySettings() ProactivitySettingsView {
	b.mu.RLock()
	value := b.config.Proactivity
	b.mu.RUnlock()
	return proactivitySettingsView(value)
}

func (b *Bridge) SaveProactivitySettings(input ProactivitySettingsView) error {
	ctx, cancel := b.context()
	defer cancel()
	value := config.ProactivityConfig{
		Enabled: input.Enabled, QuietHoursEnabled: input.QuietHoursEnabled,
		QuietHoursStart: strings.TrimSpace(input.QuietHoursStart), QuietHoursEnd: strings.TrimSpace(input.QuietHoursEnd),
		Timezone: strings.TrimSpace(input.Timezone), DailyLimit: input.DailyLimit,
		CooldownMinutes: input.CooldownMinutes, AllowLocalNotifications: input.AllowLocalNotifications,
		AutonomousPeerDialogues: input.AutonomousPeerDialogues, AutonomousPeerDailyLimit: input.AutonomousPeerDailyLimit,
		AutonomousPeerCooldownMinutes: input.AutonomousPeerCooldownMinutes,
	}
	defaults := config.Default(b.paths).Proactivity
	if value.AutonomousPeerDailyLimit == 0 {
		value.AutonomousPeerDailyLimit = defaults.AutonomousPeerDailyLimit
	}
	if value.AutonomousPeerCooldownMinutes == 0 {
		value.AutonomousPeerCooldownMinutes = defaults.AutonomousPeerCooldownMinutes
	}
	if err := value.Validate(); err != nil {
		return err
	}
	b.mu.Lock()
	candidate := b.config
	candidate.Proactivity = value
	if err := config.Save(b.paths, candidate); err != nil {
		b.mu.Unlock()
		return err
	}
	b.config = candidate
	service := b.proactivity
	b.mu.Unlock()
	if service == nil {
		return fmt.Errorf("proactivity service is unavailable")
	}
	if err := service.UpdateSettings(proactivitySettings(value)); err != nil {
		return err
	}
	if err := b.appendProactivityAudit(ctx, "proactivity.settings", "local owner settings", domain.PermissionAllow); err != nil {
		if b.logger != nil {
			b.logger.ErrorContext(ctx, "append proactivity settings audit", "error", err)
		}
	}
	return nil
}

func proactivitySettingsView(value config.ProactivityConfig) ProactivitySettingsView {
	return ProactivitySettingsView{
		Enabled: value.Enabled, QuietHoursEnabled: value.QuietHoursEnabled,
		QuietHoursStart: value.QuietHoursStart, QuietHoursEnd: value.QuietHoursEnd,
		Timezone: value.Timezone, DailyLimit: value.DailyLimit, CooldownMinutes: value.CooldownMinutes,
		AllowLocalNotifications: value.AllowLocalNotifications,
		AutonomousPeerDialogues: value.AutonomousPeerDialogues, AutonomousPeerDailyLimit: value.AutonomousPeerDailyLimit,
		AutonomousPeerCooldownMinutes: value.AutonomousPeerCooldownMinutes,
	}
}

func (b *Bridge) emitNotification(_ context.Context, notification domain.Notification) error {
	b.mu.RLock()
	appContext := b.appCtx
	allowNative := b.config.Proactivity.AllowLocalNotifications
	b.mu.RUnlock()
	if appContext == nil {
		return fmt.Errorf("desktop notification runtime is unavailable")
	}
	wailsruntime.EventsEmit(appContext, notificationEventName, NotificationEventView{
		ID: string(notification.ID), Type: string(notification.Type), Title: notification.Title, Body: notification.Body,
		Source: notification.Source.Kind, Reason: notification.Source.Reason,
		ConversationID: string(notification.ConversationID), DeepLink: notification.DeepLink,
		AllowNative: allowNative, CreatedAt: notification.CreatedAt.UTC().Format(time.RFC3339Nano),
	})
	return nil
}

func (b *Bridge) deliverNotification(ctx context.Context, notification domain.Notification) (proactivity.Decision, error) {
	b.mu.RLock()
	service := b.proactivity
	b.mu.RUnlock()
	if service == nil {
		return proactivity.Decision{}, fmt.Errorf("proactivity service is unavailable")
	}
	decision, err := service.Deliver(ctx, notification)
	action := "notification.sent"
	policyDecision := domain.PermissionAllow
	if err != nil {
		action = "notification.failed"
		policyDecision = domain.PermissionDeny
	} else if decision.Deferred() {
		action = "notification.deferred"
		policyDecision = domain.PermissionDeny
	} else if decision.Suppressed() {
		action = "notification.suppressed"
		policyDecision = domain.PermissionDeny
	}
	payload, _ := json.Marshal(map[string]string{"reason": string(decision.Reason), "type": string(notification.Type)})
	auditErr := b.appendProactivityAuditPayload(ctx, action, string(notification.ID), policyDecision, string(payload))
	if err != nil {
		return decision, err
	}
	if auditErr != nil {
		// Delivery has already happened (or was conclusively suppressed). A
		// failed audit append must not turn that side effect into a scheduler
		// retry and send it a second time.
		if b.logger != nil {
			b.logger.ErrorContext(ctx, "append notification audit", "notification_id", notification.ID, "error", auditErr)
		}
	}
	return decision, nil
}

func (b *Bridge) appendProactivityAudit(ctx context.Context, action, target string, decision domain.PermissionDecision) error {
	return b.appendProactivityAuditPayload(ctx, action, target, decision, "{}")
}

func (b *Bridge) appendProactivityAuditPayload(ctx context.Context, action, target string, decision domain.PermissionDecision, payload string) error {
	id, err := domain.NewID("audit")
	if err != nil {
		return err
	}
	return b.repositories.Audit.Append(ctx, storage.AuditEvent{
		ID: id, Actor: domain.ActorSystem, Action: action, Target: target,
		Decision: decision, PayloadRedacted: payload, CreatedAt: time.Now().UTC(),
	})
}

const (
	// proactivityLedgerAction is the only audit action the startup ledger
	// restore cares about.
	proactivityLedgerAction = "notification.sent"

	// proactivityLedgerWindow is how far back the startup restore reads the
	// audit journal. The ledger it rebuilds holds exactly three horizons: the
	// idempotency set (a notification is only re-suppressed while its own
	// cooldown is live), the per-type cooldown (configured in minutes), and
	// the per-day counter for the daily limit. The longest of those is one
	// calendar day in the owner's timezone, so two days covers every entry
	// that can still change a delivery decision at any UTC offset. Before this
	// bound the restore read the entire audit journal - a table with no
	// retention that is appended to on every tool call, notification, install
	// and persona/relationship/affect revision - so both startup time and
	// resident memory grew with the age of the installation.
	proactivityLedgerWindow = 48 * time.Hour

	// proactivityLedgerScanLimit additionally caps how many audit rows the
	// restore inspects, so a burst of unrelated audit traffic inside the
	// window cannot reintroduce unbounded startup work either. The journal is
	// listed newest first, so the cap keeps the most recent rows.
	proactivityLedgerScanLimit = 2000
)

// auditLedgerSource is the slice of the audit repository the ledger restore
// uses. It exists so the bound on the read is testable without a live
// database.
type auditLedgerSource interface {
	List(ctx context.Context, limit ...int) ([]storage.AuditEvent, error)
}

func (b *Bridge) restoreProactivityLedger(ctx context.Context) error {
	return restoreProactivityLedgerAt(ctx, b.repositories.Audit, b.proactivity.Policy(), time.Now().UTC())
}

func restoreProactivityLedgerAt(ctx context.Context, source auditLedgerSource, policy *proactivity.Policy, now time.Time) error {
	events, err := source.List(ctx, proactivityLedgerScanLimit)
	if err != nil {
		return err
	}
	cutoff := now.Add(-proactivityLedgerWindow)
	for _, event := range events {
		// List returns newest first, so the first row older than the window
		// ends the scan: everything behind it is older still.
		if event.CreatedAt.Before(cutoff) {
			break
		}
		if event.Action != proactivityLedgerAction || strings.TrimSpace(event.Target) == "" {
			continue
		}
		var payload struct {
			Type domain.NotificationType `json:"type"`
		}
		if json.Unmarshal([]byte(event.PayloadRedacted), &payload) != nil || !payload.Type.Valid() {
			continue
		}
		if err := policy.RestoreDelivered(domain.ID(event.Target), payload.Type, event.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}
