package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

func TestEmbeddedMigrationsAreOrderedAndChecksummed(t *testing.T) {
	migrations, err := Migrations()
	if err != nil {
		t.Fatalf("Migrations() error = %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("Migrations() returned no migrations")
	}
	for index, migration := range migrations {
		if migration.Version <= 0 || migration.Name == "" || migration.SQL == "" || len(migration.Checksum) != 64 {
			t.Fatalf("invalid migration: %#v", migration)
		}
		if index > 0 && migrations[index-1].Version >= migration.Version {
			t.Fatalf("migrations are not strictly ordered: %#v", migrations)
		}
	}
}

func TestMigrateUpgradesKnownPreReleaseChecksum(t *testing.T) {
	database, err := Open(context.Background(), filepath.Join(t.TempDir(), "yuri.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	const legacy = "6a6e29516f13a6d786e420025101e65ca1ebbdee37673ceac85988ac915ee4d3"
	if _, err := database.Exec("UPDATE schema_migrations SET checksum = ? WHERE version = 4", legacy); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), database); err != nil {
		t.Fatalf("Migrate() rejected known compatible checksum: %v", err)
	}

	var got string
	if err := database.QueryRow("SELECT checksum FROM schema_migrations WHERE version = 4").Scan(&got); err != nil {
		t.Fatal(err)
	}
	migrations, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if got != migrations[3].Checksum {
		t.Fatalf("checksum = %q, want %q", got, migrations[3].Checksum)
	}
}

func TestMigrateRejectsUnknownChecksum(t *testing.T) {
	database, err := Open(context.Background(), filepath.Join(t.TempDir(), "yuri.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if _, err := database.Exec("UPDATE schema_migrations SET checksum = 'unknown' WHERE version = 4"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), database); err == nil {
		t.Fatal("Migrate() accepted unknown checksum")
	}
}
