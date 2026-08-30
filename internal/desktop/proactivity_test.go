package desktop

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/proactivity"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

func TestProactivitySettingsRoundTrip(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		ConfigDirectory: filepath.Join(root, "config"), DataDirectory: filepath.Join(root, "data"),
	}
	paths.ConfigFile = filepath.Join(paths.ConfigDirectory, "config.json")
	database, err := storage.Open(context.Background(), filepath.Join(paths.DataDirectory, "yuri.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	value := config.Default(paths)
	service, err := proactivity.NewService(proactivitySettings(value.Proactivity), proactivity.FuncNotifier(func(_ context.Context, _ proactivity.Notification) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	bridge := &Bridge{paths: paths, config: value, proactivity: service, repositories: repositories, database: database}
	input := ProactivitySettingsView{
		Enabled: true, QuietHoursEnabled: true, QuietHoursStart: "22:00", QuietHoursEnd: "06:30",
		Timezone: "Europe/Moscow", DailyLimit: 7, CooldownMinutes: 45, AllowLocalNotifications: true,
	}
	if err := bridge.SaveProactivitySettings(input); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	if got := proactivitySettingsView(loaded.Proactivity); got != input {
		t.Fatalf("settings round trip = %#v, want %#v", got, input)
	}
	if got := bridge.GetProactivitySettings(); got != input {
		t.Fatalf("bridge settings = %#v, want %#v", got, input)
	}
}

func TestRestoreProactivityLedgerFromAudit(t *testing.T) {
	root := t.TempDir()
	database, err := storage.Open(context.Background(), filepath.Join(root, "yuri.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	settings := proactivity.Settings{Enabled: true, Timezone: "UTC", DailyLimit: 1}
	service, err := proactivity.NewService(settings, proactivity.FuncNotifier(func(context.Context, proactivity.Notification) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	// Relative to the wall clock: the restore only reads a bounded recent
	// window of the audit journal, so a hard-coded calendar date would fall
	// out of that window as time passes.
	now := time.Now().UTC().Truncate(time.Second)
	if err := repositories.Audit.Append(context.Background(), storage.AuditEvent{
		ID: "audit_notification", Actor: domain.ActorSystem, Action: "notification.sent", Target: "notification_durable",
		Decision: domain.PermissionAllow, PayloadRedacted: `{"reason":"","type":"agent_message"}`, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	bridge := &Bridge{repositories: repositories, proactivity: service}
	if err := bridge.restoreProactivityLedger(context.Background()); err != nil {
		t.Fatal(err)
	}
	decision, err := service.DecideAt(domain.Notification{
		ID: "notification_durable", Type: domain.NotificationTypeAgentMessage, Title: "title", Body: "body",
		Source: domain.NotificationSource{Kind: domain.NotificationSourceRule, ID: "rule", Reason: "reason"}, CreatedAt: now,
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Suppressed() || decision.Reason != proactivity.DecisionReasonAlreadyDelivered {
		t.Fatalf("restored decision = %+v", decision)
	}
}

// countingAuditSource records how the ledger restore queries the audit
// journal so the bound on that query can be asserted directly.
type countingAuditSource struct {
	events []storage.AuditEvent
	calls  int
	limits []int
}

func (s *countingAuditSource) List(_ context.Context, limit ...int) ([]storage.AuditEvent, error) {
	s.calls++
	if len(limit) > 0 {
		s.limits = append(s.limits, limit[0])
	} else {
		s.limits = append(s.limits, 0)
	}
	if len(limit) > 0 && limit[0] > 0 && len(s.events) > limit[0] {
		return append([]storage.AuditEvent(nil), s.events[:limit[0]]...), nil
	}
	return append([]storage.AuditEvent(nil), s.events...), nil
}

// proactivityTestNow anchors the ledger fixtures to a fixed instant in the
// middle of a UTC calendar day.
//
// The restored ledger's daily counter is bucketed per UTC date
// (Policy.RestoreDelivered keys dailyCounts by deliveredAt.Format("2006-01-02")),
// so a fixture built by subtracting from the ambient wall clock silently
// crosses into the previous day whenever the suite happens to run shortly
// after UTC midnight - a deterministic failure for a slice of every day
// rather than a flake. Both the code under test (restoreProactivityLedgerAt)
// and the probe (DecideAt) take the evaluation instant explicitly, so these
// tests need no ambient clock at all. Noon leaves twelve hours of headroom on
// either side of the fixture offsets used below.
var proactivityTestNow = time.Date(2026, time.March, 15, 12, 0, 0, 0, time.UTC)

func notificationSentEvent(id string, at time.Time) storage.AuditEvent {
	return storage.AuditEvent{
		ID: domain.ID("audit_" + id), Actor: domain.ActorSystem, Action: "notification.sent",
		Target: id, Decision: domain.PermissionAllow,
		PayloadRedacted: `{"reason":"","type":"agent_message"}`, CreatedAt: at,
	}
}

// TestRestoreProactivityLedgerBoundsAuditRead pins the property the fix
// exists for: the startup restore asks for a bounded number of rows and stops
// at the edge of its window, so the work it does is the same whether the
// installation has a hundred audit rows or a hundred thousand.
func TestRestoreProactivityLedgerBoundsAuditRead(t *testing.T) {
	now := proactivityTestNow
	build := func(history int) *countingAuditSource {
		source := &countingAuditSource{}
		// Newest first, exactly as AuditRepository.List returns them.
		source.events = append(source.events,
			notificationSentEvent("fresh_a", now.Add(-time.Minute)),
			notificationSentEvent("fresh_b", now.Add(-2*time.Minute)),
		)
		for index := 0; index < history; index++ {
			age := proactivityLedgerWindow + time.Duration(index+1)*time.Minute
			source.events = append(source.events, notificationSentEvent(fmt.Sprintf("stale_%d", index), now.Add(-age)))
		}
		return source
	}
	restore := func(source *countingAuditSource) *proactivity.Policy {
		policy, err := proactivity.NewPolicy(proactivity.Settings{Enabled: true, Timezone: "UTC", DailyLimit: 100})
		if err != nil {
			t.Fatal(err)
		}
		if err := restoreProactivityLedgerAt(context.Background(), source, policy, now); err != nil {
			t.Fatalf("restoreProactivityLedgerAt() error = %v", err)
		}
		return policy
	}
	probe := func(policy *proactivity.Policy, id string) proactivity.Decision {
		decision, err := policy.DecideAt(domain.Notification{
			ID: domain.ID(id), Type: domain.NotificationTypeAgentMessage, Title: "title", Body: "body",
			Source:    domain.NotificationSource{Kind: domain.NotificationSourceRule, ID: "rule", Reason: "reason"},
			CreatedAt: now,
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		return decision
	}

	small, large := build(10), build(50_000)
	smallPolicy, largePolicy := restore(small), restore(large)

	for name, source := range map[string]*countingAuditSource{"small": small, "large": large} {
		if source.calls != 1 {
			t.Fatalf("%s history: audit journal read %d times, want exactly 1", name, source.calls)
		}
		if len(source.limits) != 1 || source.limits[0] != proactivityLedgerScanLimit {
			t.Fatalf("%s history: audit read limits = %v, want [%d]", name, source.limits, proactivityLedgerScanLimit)
		}
	}

	// The rebuilt ledger is identical for both installations: only the two
	// in-window deliveries were replayed, and the 50k stale rows behind the
	// window boundary contributed no ledger entries at all.
	smallCount := probe(smallPolicy, "candidate").DailyCount
	largeCount := probe(largePolicy, "candidate").DailyCount
	if smallCount != 2 || largeCount != 2 {
		t.Fatalf("restored daily counts = %d (small) / %d (large), want 2 and 2", smallCount, largeCount)
	}

	// Idempotency still holds for the deliveries inside the window ...
	if decision := probe(largePolicy, "fresh_a"); !decision.Suppressed() || decision.Reason != proactivity.DecisionReasonAlreadyDelivered {
		t.Fatalf("in-window delivery decision = %+v, want already_delivered", decision)
	}
	// ... and the stale rows outside it are simply not part of the ledger.
	if decision := probe(largePolicy, "stale_0"); decision.Suppressed() && decision.Reason == proactivity.DecisionReasonAlreadyDelivered {
		t.Fatalf("out-of-window delivery was restored into the ledger: %+v", decision)
	}
}

// TestRestoreProactivityLedgerSkipsAgedAuditRows runs the same bound against a
// real database: aged rows stay on disk, but they no longer inflate the
// in-memory ledger the restore builds.
func TestRestoreProactivityLedgerSkipsAgedAuditRows(t *testing.T) {
	root := t.TempDir()
	database, err := storage.Open(context.Background(), filepath.Join(root, "yuri.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := proactivityTestNow
	if err := repositories.Audit.Append(context.Background(), notificationSentEvent("recent", now.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 200; index++ {
		age := proactivityLedgerWindow + time.Duration(index+1)*time.Hour
		if err := repositories.Audit.Append(context.Background(), notificationSentEvent(fmt.Sprintf("aged_%d", index), now.Add(-age))); err != nil {
			t.Fatal(err)
		}
	}
	settings := proactivity.Settings{Enabled: true, Timezone: "UTC", DailyLimit: 100}
	service, err := proactivity.NewService(settings, proactivity.FuncNotifier(func(context.Context, proactivity.Notification) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	// restoreProactivityLedgerAt is the injected-instant seam that
	// Bridge.restoreProactivityLedger wraps with time.Now(). Driving it
	// directly keeps the real audit repository, the real window bound and the
	// real restore logic in play while removing the ambient clock from the
	// fixture. The Bridge wiring itself stays covered by
	// TestRestoreProactivityLedgerFromAudit.
	if err := restoreProactivityLedgerAt(context.Background(), repositories.Audit, service.Policy(), now); err != nil {
		t.Fatal(err)
	}
	decide := func(id string) proactivity.Decision {
		decision, err := service.DecideAt(domain.Notification{
			ID: domain.ID(id), Type: domain.NotificationTypeAgentMessage, Title: "title", Body: "body",
			Source:    domain.NotificationSource{Kind: domain.NotificationSourceRule, ID: "rule", Reason: "reason"},
			CreatedAt: now,
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		return decision
	}
	if decision := decide("recent"); !decision.Suppressed() || decision.Reason != proactivity.DecisionReasonAlreadyDelivered {
		t.Fatalf("recent delivery decision = %+v, want already_delivered", decision)
	}
	if decision := decide("aged_0"); decision.Suppressed() && decision.Reason == proactivity.DecisionReasonAlreadyDelivered {
		t.Fatalf("aged delivery was restored into the ledger: %+v", decision)
	}
	if count := decide("candidate").DailyCount; count != 1 {
		t.Fatalf("restored daily count = %d, want 1 (only the in-window delivery)", count)
	}
}
