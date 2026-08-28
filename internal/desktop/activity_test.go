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
