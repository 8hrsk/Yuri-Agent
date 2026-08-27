package sqlite

import "testing"

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
