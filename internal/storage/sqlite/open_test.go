package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenCreatesAndMigratesDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "yuri.sqlite3")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer database.Close()

	var generation string
	if err := database.QueryRow("SELECT value FROM app_metadata WHERE key = 'schema_generation'").Scan(&generation); err != nil {
		t.Fatalf("query schema generation: %v", err)
	}
	if generation != "foundation-v1" {
		t.Fatalf("schema generation = %q", generation)
	}
}

func TestOpenRejectsRelativePath(t *testing.T) {
	if _, err := Open(context.Background(), "relative.db"); err == nil {
		t.Fatal("Open() expected relative path error")
	}
}
