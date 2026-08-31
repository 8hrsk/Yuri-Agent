package desktop

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

func TestListActivityReturnsRedactedNewestFirst(t *testing.T) {
	database, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "yuri.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	for index, event := range []storage.AuditEvent{
		{ID: "audit-old", Actor: domain.ActorAgent, Action: "schedule.create", Target: "Morning brief", CreatedAt: now},
		{ID: "audit-new", Actor: domain.ActorSystem, Action: "notification.deferred", Target: "quiet hours", CreatedAt: now.Add(time.Minute)},
	} {
		if err := repositories.Audit.Append(context.Background(), event); err != nil {
			t.Fatalf("append event %d: %v", index, err)
		}
	}
	bridge := &Bridge{database: database, repositories: repositories}
	items, err := bridge.ListActivity(ActivityListInput{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != "audit-new" || items[0].Type != "proactive" {
		t.Fatalf("ListActivity() = %#v", items)
	}
	if items[1].Title != "Создано расписание" || items[1].Detail != "Morning brief" {
		t.Fatalf("schedule activity = %#v", items[1])
	}
}

func TestListActivityEnrichesPersonaRevisionWithExplainableChanges(t *testing.T) {
	database, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "yuri.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 8, 31, 16, 0, 0, 0, time.UTC)
	persona, err := domain.NewMutablePersona("persona-activity", map[string]float64{"warmth": .4, "trust": .5}, "calm", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Persona.Create(ctx, persona); err != nil {
		t.Fatal(err)
	}
	next := persona
	next.Version = 2
	next.Traits = map[string]float64{"warmth": .55, "trust": .5}
	next.Reason = "Тёплый подтверждённый разговор"
	next.Evidence = []domain.EvidenceLink{{SourceType: "message", MessageID: "message-activity", ExcerptHash: "sha256:activity"}}
	next.UpdatedAt = now.Add(time.Minute)
	if _, err := repositories.Persona.AppendVersion(ctx, next, 1); err != nil {
		t.Fatal(err)
	}

	bridge := &Bridge{database: database, repositories: repositories}
	items, err := bridge.ListActivity(ActivityListInput{Limit: 10, Type: "reflection"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("reflection activity = %#v", items)
	}
	latest := items[0]
	if latest.Layer != "mutable_persona" || latest.Version != 2 || latest.Operation != "update" || latest.Reason != next.Reason || latest.Evidence != 1 {
		t.Fatalf("enriched persona activity = %#v", latest)
	}
	if len(latest.Changes) != 1 || latest.Changes[0].Key != "warmth" || latest.Changes[0].Delta < .149 || latest.Changes[0].Delta > .151 {
		t.Fatalf("persona activity changes = %#v", latest.Changes)
	}
}
