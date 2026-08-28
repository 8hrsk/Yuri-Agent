// Package proactivity contains the provider-neutral policy and delivery
// boundary for Yuri's proactive notifications. Scheduler persistence and
// platform-specific notification APIs are deliberately outside this package.
package proactivity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

var (
	ErrNoNotifier = errors.New("proactivity: notifier is not configured")
)

// Notification and NotificationSource are aliases so application adapters can
// use the proactivity package without importing the lower-level domain types
// for ordinary delivery flows.
type Notification = domain.Notification
type NotificationSource = domain.NotificationSource
type NotificationType = domain.NotificationType

const (
	NotificationTypeTaskCompleted       = domain.NotificationTypeTaskCompleted
	NotificationTypeBackgroundCompleted = domain.NotificationTypeBackgroundCompleted
	NotificationTypePluginEvent         = domain.NotificationTypePluginEvent
	NotificationTypeRuleTriggered       = domain.NotificationTypeRuleTriggered
	NotificationTypeAgentMessage        = domain.NotificationTypeAgentMessage
)

// Notifier is the local side-effect boundary. A Wails adapter can emit an
// application event and/or call the native macOS notification API; tests can
// implement it with a function or an in-memory sink.
type Notifier = domain.Notifier

// FuncNotifier adapts a function to Notifier, which keeps Wails and test
// adapters free from a dependency on the policy implementation.
type FuncNotifier func(context.Context, Notification) error

func (f FuncNotifier) Notify(ctx context.Context, notification Notification) error {
	if f == nil {
		return ErrNoNotifier
	}
	return f(ctx, notification)
}

// QuietHours is a local wall-clock interval in 24-hour HH:MM form. If End is
// earlier than Start, the interval crosses midnight (for example 22:00-07:00).
// Equal Start and End deliberately mean all-day quiet time; an empty slice is
// the way to disable quiet hours.
type QuietHours struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

func (q QuietHours) Validate() error {
	if _, err := parseTimeOfDay(q.Start); err != nil {
		return fmt.Errorf("%w: quiet hours start: %v", domain.ErrInvalidArgument, err)
	}
	if _, err := parseTimeOfDay(q.End); err != nil {
		return fmt.Errorf("%w: quiet hours end: %v", domain.ErrInvalidArgument, err)
	}
	return nil
}

// Settings controls proactive delivery. DailyLimit == 0 means unlimited;
// cooldown values of zero mean that a notification type has no cooldown.
// Timezone must be an IANA timezone name so limits and quiet hours remain
// deterministic across DST changes and machine locale changes.
type Settings struct {
	Enabled    bool                               `json:"enabled"`
	Timezone   string                             `json:"timezone"`
	QuietHours []QuietHours                       `json:"quiet_hours,omitempty"`
	DailyLimit int                                `json:"daily_limit"`
	Cooldowns  map[NotificationType]time.Duration `json:"cooldowns,omitempty"`
}

// DefaultSettings returns a safe baseline. Proactive delivery is disabled
// until the user enables it explicitly; UTC avoids accidentally using the
// host timezone before onboarding has selected one.
func DefaultSettings() Settings {
	return Settings{Timezone: "UTC"}
}

func (s Settings) Validate() error {
	if strings.TrimSpace(s.Timezone) == "" {
		return fmt.Errorf("%w: proactive timezone is required", domain.ErrInvalidArgument)
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return fmt.Errorf("%w: invalid proactive timezone %q: %v", domain.ErrInvalidArgument, s.Timezone, err)
	}
	if s.DailyLimit < 0 {
		return fmt.Errorf("%w: daily notification limit cannot be negative", domain.ErrInvalidArgument)
	}
	for index, quiet := range s.QuietHours {
		if err := quiet.Validate(); err != nil {
			return fmt.Errorf("%w: quiet interval %d: %v", domain.ErrInvalidArgument, index, err)
		}
	}
	for notificationType, cooldown := range s.Cooldowns {
		if !notificationType.Valid() {
			return fmt.Errorf("%w: invalid cooldown notification type %q", domain.ErrInvalidArgument, notificationType)
		}
		if cooldown < 0 {
			return fmt.Errorf("%w: cooldown for %q cannot be negative", domain.ErrInvalidArgument, notificationType)
		}
	}
	return nil
}

func (s Settings) clone() Settings {
	s.QuietHours = append([]QuietHours(nil), s.QuietHours...)
	if s.Cooldowns != nil {
		cooldowns := s.Cooldowns
		s.Cooldowns = make(map[NotificationType]time.Duration, len(cooldowns))
		for notificationType, cooldown := range cooldowns {
			s.Cooldowns[notificationType] = cooldown
		}
	}
	return s
}

// DeliveryDisposition describes what the policy expects the caller to do.
type DeliveryDisposition string

const (
	DispositionDeliver    DeliveryDisposition = "deliver"
	DispositionDeferred   DeliveryDisposition = "deferred"
	DispositionSuppressed DeliveryDisposition = "suppressed"
)

// DecisionReason is stable machine-readable context for a policy result.
type DecisionReason string

const (
	DecisionReasonNone             DecisionReason = ""
	DecisionReasonDisabled         DecisionReason = "disabled"
	DecisionReasonQuietHours       DecisionReason = "quiet_hours"
	DecisionReasonDailyLimit       DecisionReason = "daily_limit"
	DecisionReasonCooldown         DecisionReason = "cooldown"
	DecisionReasonAlreadyDelivered DecisionReason = "already_delivered"
)

// Decision is an explainable result for one notification candidate. A
// deferred decision has DeliverAt set to the earliest time at which all known
// finite blockers have cleared. The scheduler should re-evaluate the
// notification at that time because another policy rule may still apply.
type Decision struct {
	NotificationID domain.ID                 `json:"notification_id"`
	Type           domain.NotificationType   `json:"type"`
	Source         domain.NotificationSource `json:"source"`
	Disposition    DeliveryDisposition       `json:"disposition"`
	Reason         DecisionReason            `json:"reason"`
	Reasons        []DecisionReason          `json:"reasons,omitempty"`
	Explanation    string                    `json:"explanation"`
	EvaluatedAt    time.Time                 `json:"evaluated_at"`
	DeliverAt      time.Time                 `json:"deliver_at,omitempty"`
	DailyCount     int                       `json:"daily_count"`
	DailyLimit     int                       `json:"daily_limit"`
	CooldownUntil  time.Time                 `json:"cooldown_until,omitempty"`
}

func (d Decision) Allowed() bool    { return d.Disposition == DispositionDeliver }
func (d Decision) Deferred() bool   { return d.Disposition == DispositionDeferred }
func (d Decision) Suppressed() bool { return d.Disposition == DispositionSuppressed }

// ClockOption makes policy tests deterministic and allows the application to
// share its clock with scheduler decisions.
type Option func(*Policy)

func WithClock(clock domain.Clock) Option {
	return func(policy *Policy) {
		if clock != nil {
			policy.clock = clock
		}
	}
}

// Policy evaluates proactive delivery and keeps the in-memory rate ledger for
// the current process. The durable scheduler may persist its own delivery
// records later; this package intentionally does not introduce a persistence
// dependency.
type Policy struct {
	mu       sync.Mutex
	settings Settings
	location *time.Location
	clock    domain.Clock

	dailyCounts map[string]int
	lastByType  map[domain.NotificationType]time.Time
	delivered   map[domain.ID]time.Time
	pending     map[domain.ID]reservation
}

type reservation struct {
	date         string
	typ          domain.NotificationType
	reservedAt   time.Time
	previousLast time.Time
}

func NewPolicy(settings Settings, options ...Option) (*Policy, error) {
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	location, err := time.LoadLocation(settings.Timezone)
	if err != nil {
		return nil, fmt.Errorf("load proactive timezone: %w", err)
	}
	policy := &Policy{
		settings:    settings.clone(),
		location:    location,
		clock:       domain.SystemClock{},
		dailyCounts: make(map[string]int),
		lastByType:  make(map[domain.NotificationType]time.Time),
		delivered:   make(map[domain.ID]time.Time),
		pending:     make(map[domain.ID]reservation),
	}
	for _, option := range options {
		if option != nil {
			option(policy)
		}
	}
	return policy, nil
}

// Settings returns a defensive copy suitable for a Wails DTO or config
// adapter. Mutating the returned maps/slices cannot change policy state.
func (policy *Policy) Settings() Settings {
	if policy == nil {
		return Settings{}
	}
	policy.mu.Lock()
	defer policy.mu.Unlock()
	return policy.settings.clone()
}

func (policy *Policy) UpdateSettings(settings Settings) error {
	if policy == nil {
		return fmt.Errorf("%w: nil proactive policy", domain.ErrInvalidArgument)
	}
	if err := settings.Validate(); err != nil {
		return err
	}
	location, err := time.LoadLocation(settings.Timezone)
	if err != nil {
		return fmt.Errorf("load proactive timezone: %w", err)
	}
	policy.mu.Lock()
	defer policy.mu.Unlock()
	if policy.settings.Timezone != settings.Timezone {
		// Counts are keyed by local date. A timezone change makes existing
		// buckets ambiguous, so start the new calendar under the new zone.
		policy.dailyCounts = make(map[string]int)
	}
	policy.settings = settings.clone()
	policy.location = location
	return nil
}

func (policy *Policy) now() (time.Time, error) {
	if policy == nil {
		return time.Time{}, fmt.Errorf("%w: nil proactive policy", domain.ErrInvalidArgument)
	}
	clock := policy.clock
	if clock == nil {
		clock = domain.SystemClock{}
	}
	now := clock.Now()
	if now.IsZero() {
		return time.Time{}, fmt.Errorf("%w: proactive clock returned zero time", domain.ErrInvalidArgument)
	}
	return now.UTC(), nil
}

// Decide evaluates a candidate against the policy without reserving a
// delivery slot. Use Deliver when the notifier is available and the caller
// wants an atomic check/reserve/send flow.
func (policy *Policy) Decide(notification Notification) (Decision, error) {
	now, err := policy.now()
	if err != nil {
		return Decision{}, err
	}
	return policy.DecideAt(notification, now)
}

func (policy *Policy) DecideAt(notification Notification, now time.Time) (Decision, error) {
	if policy == nil {
		return Decision{}, fmt.Errorf("%w: nil proactive policy", domain.ErrInvalidArgument)
	}
	if err := notification.Validate(); err != nil {
		return Decision{}, err
	}
	if now.IsZero() {
		return Decision{}, fmt.Errorf("%w: proactive decision timestamp is required", domain.ErrInvalidArgument)
	}
	now = now.UTC()
	policy.mu.Lock()
	defer policy.mu.Unlock()
	decision, _ := policy.decideLocked(notification, now, false)
	return decision, nil
}

func (policy *Policy) decideLocked(notification Notification, now time.Time, reserve bool) (Decision, *reservation) {
	localNow := now.In(policy.location)
	date := localNow.Format("2006-01-02")
	decision := Decision{
		NotificationID: notification.ID,
		Type:           notification.Type,
		Source:         notification.Source,
		Disposition:    DispositionDeliver,
		Reason:         DecisionReasonNone,
		EvaluatedAt:    now,
		DailyCount:     policy.dailyCounts[date],
		DailyLimit:     policy.settings.DailyLimit,
	}

	if _, alreadyDelivered := policy.delivered[notification.ID]; alreadyDelivered {
		return policy.suppressDecision(decision, DecisionReasonAlreadyDelivered, "this proactive notification has already been delivered"), nil
	}
	if _, pending := policy.pending[notification.ID]; pending {
		return policy.suppressDecision(decision, DecisionReasonAlreadyDelivered, "this proactive notification is already being delivered"), nil
	}
	if !policy.settings.Enabled {
		return policy.suppressDecision(decision, DecisionReasonDisabled, "proactive notifications are disabled"), nil
	}

	var reasons []DecisionReason
	var dueTimes []time.Time
	if quiet, quietUntil := policy.quietUntil(now); quiet {
		reasons = append(reasons, DecisionReasonQuietHours)
		if !quietUntil.IsZero() {
			dueTimes = append(dueTimes, quietUntil)
		}
	}
	if policy.settings.DailyLimit > 0 && decision.DailyCount >= policy.settings.DailyLimit {
		reasons = append(reasons, DecisionReasonDailyLimit)
		dueTimes = append(dueTimes, nextLocalMidnight(localNow))
	}
	if cooldown := policy.settings.Cooldowns[notification.Type]; cooldown > 0 {
		if last, exists := policy.lastByType[notification.Type]; exists {
			due := last.Add(cooldown)
			if now.Before(due) {
				reasons = append(reasons, DecisionReasonCooldown)
				dueTimes = append(dueTimes, due)
				decision.CooldownUntil = due
			}
		}
	}
	if len(reasons) == 0 {
		if reserve {
			item := policy.reserveLocked(notification, now, date)
			return decision, &item
		}
		return decision, nil
	}

	decision.Disposition = DispositionDeferred
	decision.Reasons = append([]DecisionReason(nil), reasons...)
	decision.Reason = reasons[0]
	decision.Explanation = explainDeferred(reasons, policy.settings.Timezone)
	for _, due := range dueTimes {
		if due.IsZero() {
			decision.DeliverAt = time.Time{}
			break
		}
		if decision.DeliverAt.IsZero() || due.After(decision.DeliverAt) {
			decision.DeliverAt = due
		}
	}
	return decision, nil
}

func (policy *Policy) suppressDecision(decision Decision, reason DecisionReason, explanation string) Decision {
	decision.Disposition = DispositionSuppressed
	decision.Reason = reason
	decision.Reasons = []DecisionReason{reason}
	decision.Explanation = explanation
	return decision
}

func (policy *Policy) reserveLocked(notification Notification, now time.Time, date string) reservation {
	item := reservation{
		date:         date,
		typ:          notification.Type,
		reservedAt:   now,
		previousLast: policy.lastByType[notification.Type],
	}
	policy.dailyCounts[date]++
	policy.lastByType[notification.Type] = now
	policy.pending[notification.ID] = item
	return item
}

func (policy *Policy) commitLocked(notification Notification, item reservation) {
	delete(policy.pending, notification.ID)
	policy.delivered[notification.ID] = item.reservedAt
}

func (policy *Policy) rollbackLocked(notification Notification, item reservation) {
	delete(policy.pending, notification.ID)
	if count := policy.dailyCounts[item.date]; count > 1 {
		policy.dailyCounts[item.date] = count - 1
	} else {
		delete(policy.dailyCounts, item.date)
	}
	if current := policy.lastByType[item.typ]; current.Equal(item.reservedAt) {
		latest := item.previousLast
		for _, pending := range policy.pending {
			if pending.typ == item.typ && pending.reservedAt.After(latest) {
				latest = pending.reservedAt
			}
		}
		if latest.IsZero() {
			delete(policy.lastByType, item.typ)
		} else {
			policy.lastByType[item.typ] = latest
		}
	}
}

// RecordDelivered reserves and commits a slot without invoking a notifier.
// It is useful when a scheduler owns the actual platform adapter and wants to
// keep this policy's in-memory ledger in sync.
func (policy *Policy) RecordDelivered(notification Notification) (Decision, error) {
	now, err := policy.now()
	if err != nil {
		return Decision{}, err
	}
	return policy.RecordDeliveredAt(notification, now)
}

func (policy *Policy) RecordDeliveredAt(notification Notification, now time.Time) (Decision, error) {
	if policy == nil {
		return Decision{}, fmt.Errorf("%w: nil proactive policy", domain.ErrInvalidArgument)
	}
	if err := notification.Validate(); err != nil {
		return Decision{}, err
	}
	if now.IsZero() {
		return Decision{}, fmt.Errorf("%w: proactive delivery timestamp is required", domain.ErrInvalidArgument)
	}
	now = now.UTC()
	policy.mu.Lock()
	defer policy.mu.Unlock()
	decision, item := policy.decideLocked(notification, now, true)
	if item != nil {
		policy.commitLocked(notification, *item)
	}
	return decision, nil
}

// ResetLedger forgets only in-process rate/idempotency state. It does not
// delete or alter durable conversations, tasks, or notification records.
func (policy *Policy) ResetLedger() {
	if policy == nil {
		return
	}
	policy.mu.Lock()
	defer policy.mu.Unlock()
	policy.dailyCounts = make(map[string]int)
	policy.lastByType = make(map[domain.NotificationType]time.Time)
	policy.delivered = make(map[domain.ID]time.Time)
	policy.pending = make(map[domain.ID]reservation)
}

// RestoreDelivered rebuilds the rate/idempotency ledger from a durable audit
// record without re-running current delivery policy. It is intended for app
// startup before any new notification candidates are evaluated.
func (policy *Policy) RestoreDelivered(id domain.ID, notificationType domain.NotificationType, deliveredAt time.Time) error {
	if policy == nil {
		return fmt.Errorf("%w: nil proactive policy", domain.ErrInvalidArgument)
	}
	if id.Empty() || !notificationType.Valid() || deliveredAt.IsZero() {
		return fmt.Errorf("%w: invalid delivered notification record", domain.ErrInvalidArgument)
	}
	deliveredAt = deliveredAt.UTC()
	policy.mu.Lock()
	defer policy.mu.Unlock()
	if _, exists := policy.delivered[id]; exists {
		return nil
	}
	date := deliveredAt.In(policy.location).Format("2006-01-02")
	policy.delivered[id] = deliveredAt
	policy.dailyCounts[date]++
	if deliveredAt.After(policy.lastByType[notificationType]) {
		policy.lastByType[notificationType] = deliveredAt
	}
	return nil
}

// Service combines Policy with a local notifier. It is the intended
// application-facing API for an atomic policy-check/reserve/notify sequence.
type Service struct {
	policy   *Policy
	notifier Notifier
}

func NewService(settings Settings, notifier Notifier, options ...Option) (*Service, error) {
	policy, err := NewPolicy(settings, options...)
	if err != nil {
		return nil, err
	}
	return &Service{policy: policy, notifier: notifier}, nil
}

func (service *Service) Policy() *Policy {
	if service == nil {
		return nil
	}
	return service.policy
}

func (service *Service) Settings() Settings {
	if service == nil || service.policy == nil {
		return Settings{}
	}
	return service.policy.Settings()
}

func (service *Service) UpdateSettings(settings Settings) error {
	if service == nil || service.policy == nil {
		return fmt.Errorf("%w: nil proactive service", domain.ErrInvalidArgument)
	}
	return service.policy.UpdateSettings(settings)
}

func (service *Service) Decide(notification Notification) (Decision, error) {
	if service == nil || service.policy == nil {
		return Decision{}, fmt.Errorf("%w: nil proactive service", domain.ErrInvalidArgument)
	}
	return service.policy.Decide(notification)
}

func (service *Service) DecideAt(notification Notification, now time.Time) (Decision, error) {
	if service == nil || service.policy == nil {
		return Decision{}, fmt.Errorf("%w: nil proactive service", domain.ErrInvalidArgument)
	}
	return service.policy.DecideAt(notification, now)
}

func (service *Service) Deliver(ctx context.Context, notification Notification) (Decision, error) {
	if service == nil || service.policy == nil {
		return Decision{}, fmt.Errorf("%w: nil proactive service", domain.ErrInvalidArgument)
	}
	now, err := service.policy.now()
	if err != nil {
		return Decision{}, err
	}
	return service.DeliverAt(ctx, notification, now)
}

func (service *Service) DeliverAt(ctx context.Context, notification Notification, now time.Time) (Decision, error) {
	if service == nil || service.policy == nil {
		return Decision{}, fmt.Errorf("%w: nil proactive service", domain.ErrInvalidArgument)
	}
	if ctx == nil {
		return Decision{}, fmt.Errorf("%w: nil delivery context", domain.ErrInvalidArgument)
	}
	if service.notifier == nil {
		return Decision{}, ErrNoNotifier
	}
	if err := notification.Validate(); err != nil {
		return Decision{}, err
	}
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	if now.IsZero() {
		return Decision{}, fmt.Errorf("%w: proactive delivery timestamp is required", domain.ErrInvalidArgument)
	}
	now = now.UTC()

	service.policy.mu.Lock()
	decision, item := service.policy.decideLocked(notification, now, true)
	service.policy.mu.Unlock()
	if item == nil {
		return decision, nil
	}

	if err := service.notifier.Notify(ctx, notification); err != nil {
		service.policy.mu.Lock()
		service.policy.rollbackLocked(notification, *item)
		service.policy.mu.Unlock()
		return decision, err
	}
	service.policy.mu.Lock()
	service.policy.commitLocked(notification, *item)
	service.policy.mu.Unlock()
	return decision, nil
}

func explainDeferred(reasons []DecisionReason, timezone string) string {
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		switch reason {
		case DecisionReasonQuietHours:
			parts = append(parts, fmt.Sprintf("quiet hours are active in %s", timezone))
		case DecisionReasonDailyLimit:
			parts = append(parts, "the daily proactive notification limit has been reached")
		case DecisionReasonCooldown:
			parts = append(parts, "this notification type is in its cooldown period")
		}
	}
	return "delivery deferred: " + strings.Join(parts, "; ")
}

func parseTimeOfDay(value string) (int, error) {
	if len(value) != 5 || value[2] != ':' || value[0] < '0' || value[0] > '9' || value[1] < '0' || value[1] > '9' || value[3] < '0' || value[3] > '9' || value[4] < '0' || value[4] > '9' {
		return 0, fmt.Errorf("expected HH:MM, got %q", value)
	}
	hour := int(value[0]-'0')*10 + int(value[1]-'0')
	minute := int(value[3]-'0')*10 + int(value[4]-'0')
	if hour > 23 || minute > 59 {
		return 0, fmt.Errorf("time is outside 00:00-23:59: %q", value)
	}
	return hour*60 + minute, nil
}

func (policy *Policy) quietUntil(now time.Time) (bool, time.Time) {
	local := now.In(policy.location)
	var latest time.Time
	for _, quiet := range policy.settings.QuietHours {
		start, _ := parseTimeOfDay(quiet.Start)
		end, _ := parseTimeOfDay(quiet.End)
		current := local.Hour()*60 + local.Minute()
		if start == end {
			return true, time.Time{}
		}
		active := false
		var quietEnd time.Time
		today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, policy.location)
		switch {
		case start < end && current >= start && current < end:
			active = true
			quietEnd = atMinute(today, end, policy.location)
		case start > end && current >= start:
			active = true
			quietEnd = atMinute(today.AddDate(0, 0, 1), end, policy.location)
		case start > end && current < end:
			active = true
			quietEnd = atMinute(today, end, policy.location)
		}
		if active && (latest.IsZero() || quietEnd.After(latest)) {
			latest = quietEnd
		}
	}
	return !latest.IsZero(), latest
}

func atMinute(day time.Time, minutes int, location *time.Location) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), minutes/60, minutes%60, 0, 0, location)
}

func nextLocalMidnight(local time.Time) time.Time {
	return time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, local.Location())
}
