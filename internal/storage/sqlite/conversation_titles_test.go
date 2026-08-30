package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestMigration016BackfillsConversationTitleSource(t *testing.T) {
	ctx := context.Background()
	database, err := sql.Open("sqlite", sqliteFileDSN(filepath.Join(t.TempDir(), "legacy.sqlite3"), false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.ExecContext(ctx, `
		PRAGMA foreign_keys = ON;
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.Version >= 16 {
			break
		}
		if _, err := database.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatalf("apply legacy migration %d: %v", migration.Version, err)
		}
		if _, err := database.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, name, checksum) VALUES (?, ?, ?)",
			migration.Version, migration.Name, migration.Checksum); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Format(sqliteTimeLayout)
	if _, err := database.ExecContext(ctx, `
		INSERT INTO agent_profiles(id, name, age, gender, preferences, created_at, updated_at)
		VALUES ('owner', 'Yuri', 21, 'female', '', ?, ?);
		INSERT INTO conversations(id, agent_id, title, created_at, updated_at)
		VALUES ('placeholder', 'owner', 'Новый диалог', ?, ?),
		       ('custom', 'owner', 'Моя ручная тема', ?, ?)`, now, now, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, database); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	for _, test := range []struct {
		id, want string
	}{
		{id: "placeholder", want: ConversationTitleSourceDefault},
		{id: "custom", want: ConversationTitleSourceUser},
	} {
		var got string
		if err := database.QueryRowContext(ctx, `SELECT title_source FROM conversations WHERE id = ?`, test.id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != test.want {
			t.Errorf("conversation %q title_source = %q, want %q", test.id, got, test.want)
		}
	}
}

func TestConversationTitleCASAndRenameAreAgentScoped(t *testing.T) {
	database, ctx := testDatabase(t)
	repository := NewConversationRepository(database)
	now := time.Now().UTC()
	if err := repository.Create(ctx, Conversation{ID: "conversation-title", AgentID: "agent-a", Title: DefaultConversationTitle, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if updated, err := repository.UpdateTitleIfDefault(ctx, "conversation-title", "agent-b", "wrong owner", now.Add(time.Second)); err != nil || updated {
		t.Fatalf("wrong-owner title CAS = %v, %v; want false, nil", updated, err)
	}
	if updated, err := repository.UpdateTitleIfDefault(ctx, "conversation-title", "agent-a", "Сводка проекта", now.Add(time.Second)); err != nil || !updated {
		t.Fatalf("first title CAS = %v, %v; want true, nil", updated, err)
	}
	if updated, err := repository.UpdateTitleIfDefault(ctx, "conversation-title", "agent-a", "Вторая попытка", now.Add(2*time.Second)); err != nil || updated {
		t.Fatalf("second title CAS = %v, %v; want false, nil", updated, err)
	}
	conversation, err := repository.Get(ctx, "conversation-title")
	if err != nil {
		t.Fatal(err)
	}
	if conversation.Title != "Сводка проекта" || conversation.TitleSource != ConversationTitleSourceGenerated {
		t.Fatalf("generated conversation = %#v", conversation)
	}
	if err := repository.Rename(ctx, "conversation-title", "agent-a", "Ручное название", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	conversation, err = repository.Get(ctx, "conversation-title")
	if err != nil {
		t.Fatal(err)
	}
	if conversation.Title != "Ручное название" || conversation.TitleSource != ConversationTitleSourceUser {
		t.Fatalf("renamed conversation = %#v", conversation)
	}
	if updated, err := repository.UpdateTitleIfDefault(ctx, "conversation-title", "agent-a", "не перезаписывай", now.Add(4*time.Second)); err != nil || updated {
		t.Fatalf("rename-protected title CAS = %v, %v; want false, nil", updated, err)
	}

	if err := repository.Create(ctx, Conversation{ID: "custom-title", AgentID: "agent-a", Title: "Вручную до первого сообщения", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	custom, err := repository.Get(ctx, "custom-title")
	if err != nil {
		t.Fatal(err)
	}
	if custom.TitleSource != ConversationTitleSourceUser {
		t.Fatalf("custom omitted source = %q, want %q", custom.TitleSource, ConversationTitleSourceUser)
	}
}

func TestConversationTitleValidationAndBound(t *testing.T) {
	database, ctx := testDatabase(t)
	repository := NewConversationRepository(database)
	now := time.Now().UTC()
	long := "agent " + strings.Repeat("я", 80)
	if err := repository.Create(ctx, Conversation{ID: domain.ID("bounded-title"), AgentID: "agent-a", Title: long, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	conversation, err := repository.Get(ctx, "bounded-title")
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(conversation.Title)) > maxConversationTitleRunes {
		t.Fatalf("persisted title has %d runes, want <= %d", len([]rune(conversation.Title)), maxConversationTitleRunes)
	}
	if err := repository.Create(ctx, Conversation{ID: "invalid-source", AgentID: "agent-a", Title: "Title", TitleSource: "other", CreatedAt: now}); err == nil {
		t.Fatal("invalid title source accepted")
	}
}
