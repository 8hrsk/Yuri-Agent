package proactivity

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

type mutableClock struct {
	mu  sync.RWMutex
	now time.Time
}

type notificationRecorder struct {
	mu    sync.Mutex
	items []Notification
	err   error
}

func (r *notificationRecorder) Notify(_ context.Context, notification Notification) error {
	r.mu.Lock()
	r.items = append(r.items, notification)
	err := r.err
	r.mu.Unlock()
	return err
}

func (c *mutableClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *mutableClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

func proactiveNotification(id domain.ID, typ domain.NotificationType) Notification {
	return Notification{
		ID:        id,
		Type:      typ,
		Title:     "Yuri",
		Body:      "I have something to tell you.",
		Source:    NotificationSource{Kind: "rule", ID: "rule-1", Label: "Evening check-in", Reason: "The user's configured evening rule fired."},
		CreatedAt: time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC),
	}
}

func newPolicyForTest(t *testing.T, settings Settings, now time.Time) (*Policy, *mutableClock) {
	t.Helper()
	clock := &mutableClock{now: now}
	policy, err := NewPolicy(settings, WithClock(clock))
	if err != nil {
		t.Fatalf("NewPolicy() error = %v", err)
	}
	return policy, clock
}

func TestQuietHoursOvernightAreTimezoneAware(t *testing.T) {
	settings := Settings{
		Enabled:    true,
		Timezone:   "Europe/Moscow",
		QuietHours: []QuietHours{{Start: "22:00", End: "07:00"}},
	}
	policy, _ := newPolicyForTest(t, settings, time.Time{})

	cases := []struct {
		name       string
		now        time.Time
		deferred   bool
		expectedAt time.Time
	}{
		{
			name:       "evening before midnight",
			now:        time.Date(2026, time.August, 28, 20, 30, 0, 0, time.UTC), // 23:30 MSK
			deferred:   true,
			expectedAt: time.Date(2026, time.August, 29, 4, 0, 0, 0, time.UTC), // 07:00 MSK
		},
		{
			name:       "early morning after midnight",
			now:        time.Date(2026, time.August, 29, 3, 30, 0, 0, time.UTC), // 06:30 MSK
			deferred:   true,
			expectedAt: time.Date(2026, time.August, 29, 4, 0, 0, 0, time.UTC),
		},
		{
			name:     "daytime",
			now:      time.Date(2026, time.August, 29, 5, 0, 0, 0, time.UTC), // 08:00 MSK
			deferred: false,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			decision, err := policy.DecideAt(proactiveNotification(domain.ID(test.name), domain.NotificationTypeAgentMessage), test.now)
			if err != nil {
				t.Fatalf("DecideAt() error = %v", err)
			}
			if decision.Deferred() != test.deferred {
				t.Fatalf("Deferred() = %v, want %v (%+v)", decision.Deferred(), test.deferred, decision)
			}
			if test.deferred && !decision.DeliverAt.Equal(test.expectedAt) {
				t.Fatalf("DeliverAt() = %s, want %s", decision.DeliverAt, test.expectedAt)
			}
			if test.deferred {
				if decision.Reason != DecisionReasonQuietHours || len(decision.Reasons) != 1 {
					t.Fatalf("quiet decision = %+v", decision)
				}
				if decision.Explanation == "" || !containsAll(decision.Explanation, "quiet hours", "Europe/Moscow") {
					t.Fatalf("quiet decision is not explainable: %q", decision.Explanation)
				}
			}
		})
	}
}

func TestQuietHoursUseDSTCorrectly(t *testing.T) {
	settings := Settings{
		Enabled:    true,
		Timezone:   "America/New_York",
		QuietHours: []QuietHours{{Start: "22:00", End: "07:00"}},
	}
	policy, _ := newPolicyForTest(t, settings, time.Time{})

	// March 8, 2026 is the spring-forward transition in New York. The local
	// interval still ends at 07:00, but the UTC offset changes while it runs.
	now := time.Date(2026, time.March, 8, 5, 30, 0, 0, time.UTC) // 00:30 EST
	decision, err := policy.DecideAt(proactiveNotification("dst", domain.NotificationTypeAgentMessage), now)
	if err != nil {
		t.Fatalf("DecideAt() error = %v", err)
	}
	if !decision.Deferred() {
		t.Fatalf("DST quiet hours did not defer: %+v", decision)
	}
	expected := time.Date(2026, time.March, 8, 11, 0, 0, 0, time.UTC) // 07:00 EDT
	if !decision.DeliverAt.Equal(expected) {
		t.Fatalf("DST DeliverAt() = %s, want %s", decision.DeliverAt, expected)
	}
}

func TestPolicyDisabledSuppressesWithReason(t *testing.T) {
	settings := Settings{Timezone: "UTC"}
	policy, _ := newPolicyForTest(t, settings, time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	decision, err := policy.Decide(proactiveNotification("disabled", domain.NotificationTypeAgentMessage))
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if !decision.Suppressed() || decision.Reason != DecisionReasonDisabled {
		t.Fatalf("disabled decision = %+v", decision)
	}
	if decision.DeliverAt != (time.Time{}) {
		t.Fatalf("disabled notification unexpectedly has DeliverAt: %s", decision.DeliverAt)
	}
}

func TestDailyLimitDefersUntilNextLocalDay(t *testing.T) {
	settings := Settings{Enabled: true, Timezone: "Asia/Tokyo", DailyLimit: 2}
	now := time.Date(2026, time.August, 28, 14, 55, 0, 0, time.UTC) // 23:55 JST
	policy, _ := newPolicyForTest(t, settings, now)

	for index := 0; index < 2; index++ {
		decision, err := policy.RecordDeliveredAt(proactiveNotification(domain.ID("daily-"+string(rune('1'+index))), domain.NotificationTypeAgentMessage), now.Add(time.Duration(index)*time.Second))
		if err != nil {
			t.Fatalf("RecordDeliveredAt() error = %v", err)
		}
		if !decision.Allowed() {
			t.Fatalf("delivery %d was not allowed: %+v", index, decision)
		}
	}

	blocked, err := policy.DecideAt(proactiveNotification("daily-3", domain.NotificationTypeTaskCompleted), now.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("DecideAt() error = %v", err)
	}
	if !blocked.Deferred() || blocked.Reason != DecisionReasonDailyLimit {
		t.Fatalf("daily limit decision = %+v", blocked)
	}
	expected := time.Date(2026, time.August, 28, 15, 0, 0, 0, time.UTC) // midnight JST
	if !blocked.DeliverAt.Equal(expected) {
		t.Fatalf("daily DeliverAt() = %s, want %s", blocked.DeliverAt, expected)
	}

	allowed, err := policy.DecideAt(proactiveNotification("daily-4", domain.NotificationTypeTaskCompleted), expected)
	if err != nil {
		t.Fatalf("DecideAt() at reset error = %v", err)
	}
	if !allowed.Allowed() {
		t.Fatalf("notification was not allowed after local reset: %+v", allowed)
	}
}

func TestCooldownIsPerNotificationType(t *testing.T) {
	settings := Settings{
		Enabled:   true,
		Timezone:  "UTC",
		Cooldowns: map[NotificationType]time.Duration{domain.NotificationTypeAgentMessage: time.Hour},
	}
	now := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)
	policy, _ := newPolicyForTest(t, settings, now)
	first := proactiveNotification("cooldown-1", domain.NotificationTypeAgentMessage)
	if decision, err := policy.RecordDeliveredAt(first, now); err != nil || !decision.Allowed() {
		t.Fatalf("first delivery = %+v, err=%v", decision, err)
	}

	blocked, err := policy.DecideAt(proactiveNotification("cooldown-2", domain.NotificationTypeAgentMessage), now.Add(15*time.Minute))
	if err != nil {
		t.Fatalf("cooldown DecideAt() error = %v", err)
	}
	if !blocked.Deferred() || blocked.Reason != DecisionReasonCooldown {
		t.Fatalf("cooldown decision = %+v", blocked)
	}
	if !blocked.DeliverAt.Equal(now.Add(time.Hour)) || !blocked.CooldownUntil.Equal(now.Add(time.Hour)) {
		t.Fatalf("cooldown due times = %+v", blocked)
	}

	differentType, err := policy.DecideAt(proactiveNotification("cooldown-3", domain.NotificationTypeTaskCompleted), now.Add(15*time.Minute))
	if err != nil {
		t.Fatalf("different type DecideAt() error = %v", err)
	}
	if !differentType.Allowed() {
		t.Fatalf("different notification type was blocked: %+v", differentType)
	}
}

func TestDecisionCombinesBlockersAndUsesLatestFiniteDueTime(t *testing.T) {
	settings := Settings{
		Enabled:    true,
		Timezone:   "UTC",
		QuietHours: []QuietHours{{Start: "22:00", End: "07:00"}},
		DailyLimit: 1,
		Cooldowns:  map[NotificationType]time.Duration{domain.NotificationTypeAgentMessage: 2 * time.Hour},
	}
	now := time.Date(2026, time.August, 28, 21, 0, 0, 0, time.UTC)
	policy, _ := newPolicyForTest(t, settings, now)
	if decision, err := policy.RecordDeliveredAt(proactiveNotification("blocker-1", domain.NotificationTypeAgentMessage), now); err != nil || !decision.Allowed() {
		t.Fatalf("seed delivery = %+v, err=%v", decision, err)
	}
	blocked, err := policy.DecideAt(proactiveNotification("blocker-2", domain.NotificationTypeAgentMessage), now.Add(90*time.Minute))
	if err != nil {
		t.Fatalf("combined DecideAt() error = %v", err)
	}
	if !blocked.Deferred() || len(blocked.Reasons) != 3 {
		t.Fatalf("combined decision = %+v", blocked)
	}
	// Daily reset is tomorrow at 00:00 and cooldown expires at 23:00, but
	// quiet hours last until 07:00. The scheduler should wake at the latter
	// and re-evaluate the policy.
	expected := time.Date(2026, time.August, 29, 7, 0, 0, 0, time.UTC)
	if !blocked.DeliverAt.Equal(expected) {
		t.Fatalf("combined DeliverAt() = %s, want %s", blocked.DeliverAt, expected)
	}
	if blocked.Explanation == "" || !containsAll(blocked.Explanation, "quiet hours", "daily", "cooldown") {
		t.Fatalf("combined decision is not explainable: %q", blocked.Explanation)
	}
}

func TestAllDayQuietHoursHaveNoFiniteDeliveryTime(t *testing.T) {
	settings := Settings{Enabled: true, Timezone: "UTC", QuietHours: []QuietHours{{Start: "00:00", End: "00:00"}}}
	policy, _ := newPolicyForTest(t, settings, time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC))
	decision, err := policy.Decide(proactiveNotification("all-day", domain.NotificationTypeAgentMessage))
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if !decision.Deferred() || !decision.DeliverAt.IsZero() {
		t.Fatalf("all-day quiet decision = %+v", decision)
	}
}

func TestServiceDeliverReservesAndRollsBackOnNotifierFailure(t *testing.T) {
	settings := Settings{Enabled: true, Timezone: "UTC", DailyLimit: 1}
	now := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)
	recorder := &notificationRecorder{err: errors.New("native notification unavailable")}
	service, err := NewService(settings, recorder, WithClock(&mutableClock{now: now}))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	notification := proactiveNotification("delivery-failure", domain.NotificationTypeAgentMessage)
	decision, err := service.Deliver(context.Background(), notification)
	if !errors.Is(err, recorder.err) || !decision.Allowed() {
		t.Fatalf("failed delivery = %+v, err=%v", decision, err)
	}
	if len(recorder.items) != 1 {
		t.Fatalf("notifier calls = %d, want 1", len(recorder.items))
	}
	// The failed notifier did not consume the daily slot, so a retry is
	// allowed. The recorder still fails, but policy evaluation must allow it.
	retry, err := service.Decide(notification)
	if err != nil {
		t.Fatalf("retry Decide() error = %v", err)
	}
	if !retry.Allowed() {
		t.Fatalf("failed delivery consumed a slot: %+v", retry)
	}
}

func TestServiceDeliverIsIdempotentForNotificationID(t *testing.T) {
	settings := Settings{Enabled: true, Timezone: "UTC"}
	now := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)
	recorder := &notificationRecorder{}
	service, err := NewService(settings, recorder, WithClock(&mutableClock{now: now}))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	notification := proactiveNotification("same-id", domain.NotificationTypeAgentMessage)
	first, err := service.Deliver(context.Background(), notification)
	if err != nil || !first.Allowed() {
		t.Fatalf("first Deliver() = %+v, err=%v", first, err)
	}
	second, err := service.Deliver(context.Background(), notification)
	if err != nil {
		t.Fatalf("second Deliver() error = %v", err)
	}
	if !second.Suppressed() || second.Reason != DecisionReasonAlreadyDelivered {
		t.Fatalf("duplicate decision = %+v", second)
	}
	if len(recorder.items) != 1 {
		t.Fatalf("notifier calls = %d, want 1", len(recorder.items))
	}
}

func TestServiceDeliverHonorsContextBeforeNotifier(t *testing.T) {
	called := false
	service, err := NewService(Settings{Enabled: true, Timezone: "UTC"}, FuncNotifier(func(context.Context, Notification) error {
		called = true
		return nil
	}))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = service.Deliver(ctx, proactiveNotification("cancelled", domain.NotificationTypeAgentMessage))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Deliver() error = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("notifier was called with an already-cancelled context")
	}
}

func TestSettingsValidation(t *testing.T) {
	cases := []struct {
		name     string
		settings Settings
	}{
		{name: "missing timezone", settings: Settings{}},
		{name: "unknown timezone", settings: Settings{Timezone: "Mars/Phobos"}},
		{name: "negative daily limit", settings: Settings{Timezone: "UTC", DailyLimit: -1}},
		{name: "invalid quiet time", settings: Settings{Timezone: "UTC", QuietHours: []QuietHours{{Start: "9:00", End: "17:00"}}}},
		{name: "negative cooldown", settings: Settings{Timezone: "UTC", Cooldowns: map[NotificationType]time.Duration{domain.NotificationTypeAgentMessage: -time.Second}}},
		{name: "invalid type", settings: Settings{Timezone: "UTC", Cooldowns: map[NotificationType]time.Duration{"bad type": time.Minute}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := test.settings.Validate(); err == nil {
				t.Fatal("Validate() returned nil")
			}
		})
	}
	if err := (Settings{Timezone: "UTC", QuietHours: []QuietHours{{Start: "22:00", End: "07:00"}}}).Validate(); err != nil {
		t.Fatalf("valid overnight settings rejected: %v", err)
	}
}

func TestSettingsReturnsDefensiveCopyAndTimezoneChangeResetsDateBucket(t *testing.T) {
	now := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)
	policy, _ := newPolicyForTest(t, Settings{Enabled: true, Timezone: "UTC", DailyLimit: 1}, now)
	settings := policy.Settings()
	settings.QuietHours = append(settings.QuietHours, QuietHours{Start: "01:00", End: "02:00"})
	settings.Cooldowns = map[NotificationType]time.Duration{domain.NotificationTypeAgentMessage: time.Hour}
	if policy.Settings().Cooldowns != nil || len(policy.Settings().QuietHours) != 0 {
		t.Fatal("Settings() returned mutable policy state")
	}
	if decision, err := policy.RecordDeliveredAt(proactiveNotification("timezone-reset", domain.NotificationTypeAgentMessage), now); err != nil || !decision.Allowed() {
		t.Fatalf("seed delivery = %+v, err=%v", decision, err)
	}
	if err := policy.UpdateSettings(Settings{Enabled: true, Timezone: "Asia/Tokyo", DailyLimit: 1}); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	decision, err := policy.DecideAt(proactiveNotification("timezone-reset-2", domain.NotificationTypeTaskCompleted), now)
	if err != nil {
		t.Fatalf("DecideAt() after timezone change error = %v", err)
	}
	if !decision.Allowed() {
		t.Fatalf("timezone change retained old date bucket: %+v", decision)
	}
}

func TestRestoreDeliveredRebuildsDurableLimitsAndIdempotency(t *testing.T) {
	now := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)
	policy, _ := newPolicyForTest(t, Settings{Enabled: true, Timezone: "UTC", DailyLimit: 1}, now)
	if err := policy.RestoreDelivered(domain.ID("restored"), domain.NotificationTypeAgentMessage, now.Add(-time.Minute)); err != nil {
		t.Fatalf("RestoreDelivered() error = %v", err)
	}

	same, err := policy.DecideAt(proactiveNotification("restored", domain.NotificationTypeAgentMessage), now)
	if err != nil {
		t.Fatal(err)
	}
	if !same.Suppressed() || same.Reason != DecisionReasonAlreadyDelivered {
		t.Fatalf("restored id decision = %+v", same)
	}
	another, err := policy.DecideAt(proactiveNotification("after-restore", domain.NotificationTypeTaskCompleted), now)
	if err != nil {
		t.Fatal(err)
	}
	if !another.Deferred() || another.Reason != DecisionReasonDailyLimit {
		t.Fatalf("restored daily count decision = %+v", another)
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
