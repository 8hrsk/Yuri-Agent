package desktop

import (
	"context"
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
	now := time.Date(2026, time.August, 28, 10, 0, 0, 0, time.UTC)
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
