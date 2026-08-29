package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestAgentRepositoryCreateGetAndList(t *testing.T) {
	database, err := Open(context.Background(), filepath.Join(t.TempDir(), "yuri.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repository := NewAgentRepository(database)
	now := time.Now().UTC().Truncate(time.Microsecond)
	first, err := domain.NewAgentProfile("agent_yuri", "Юри", 21, "female", "Любит лаконичность.", now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := domain.NewAgentProfile("agent_mira", "Мира", 24, "female", "Предпочитает анализ.", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Create(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := repository.Create(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := repository.Create(context.Background(), first); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate error = %v", err)
	}
	got, err := repository.Get(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != first.Name || got.Preferences != first.Preferences || !got.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("Get() = %#v, want %#v", got, first)
	}
	profiles, err := repository.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || profiles[0].ID != first.ID || profiles[1].ID != second.ID {
		t.Fatalf("List() = %#v", profiles)
	}
}
