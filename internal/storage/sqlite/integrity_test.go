package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectIntegrityIsReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yuri.sqlite3")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := InspectIntegrity(context.Background(), database)
	if err != nil {
		t.Fatalf("InspectIntegrity() error = %v", err)
	}
	if !report.OK || len(report.Diagnostics) != 0 {
		t.Fatalf("integrity report = %#v", report)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("integrity check modified the database file")
	}
}

func TestOpenRejectsTruncatedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yuri.sqlite3")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, 16); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), path); err == nil {
		t.Fatal("Open() accepted a truncated database")
	} else if !errors.Is(err, ErrDatabaseIntegrity) {
		t.Fatalf("Open() error = %v, want ErrDatabaseIntegrity", err)
	}
}

func TestOpenCleansSuccessfulMigrationBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yuri.sqlite3")
	createLegacyDatabase(t, path, 4)

	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	database.Close()

	backups := backupFiles(t, path)
	if len(backups) != 0 {
		t.Fatalf("successful migration left raw backup files: %#v", backups)
	}
	metadata, err := filepath.Glob(path + ".backup-*.sqlite3.metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata) != 0 {
		t.Fatalf("successful migration left backup metadata: %#v", metadata)
	}
}

func TestOpenLeavesBackupWhenMigrationFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yuri.sqlite3")
	database := createLegacyDatabase(t, path, 4)
	// Migration 5 expects these columns for its index. The IF NOT EXISTS
	// table clause preserves this incompatible table, so index creation fails
	// inside the migration transaction after the pre-migration backup exists.
	if _, err := database.Exec("CREATE TABLE schedules (id TEXT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(context.Background(), path); err == nil {
		t.Fatal("Open() unexpectedly accepted a failing migration")
	}
	backups := backupFiles(t, path)
	if len(backups) != 1 {
		t.Fatalf("backup files = %#v, want one recoverable backup", backups)
	}
	metadata, err := ReadBackupMetadata(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if metadata.SourcePath != path || metadata.Path != backups[0] {
		t.Fatalf("backup metadata leaks or loses path identity: %#v", metadata)
	}
	if _, err := VerifyBackup(context.Background(), backups[0]); err != nil {
		t.Fatalf("VerifyBackup() error = %v", err)
	}
	checkOwnerOnly(t, backups[0])
	checkOwnerOnly(t, backupMetadataPath(backups[0]))
	backupDatabase, err := sql.Open("sqlite", sqliteFileDSN(backups[0], true))
	if err != nil {
		t.Fatal(err)
	}
	defer backupDatabase.Close()
	var applied int
	if err := backupDatabase.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 4 {
		t.Fatalf("recoverable backup migration count = %d, want 4", applied)
	}
}

func TestOpenDoesNotBackupCleanDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yuri.sqlite3")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if backups := backupFiles(t, path); len(backups) != 0 {
		t.Fatalf("clean startup unexpectedly created backups: %#v", backups)
	}
}

func TestOpenCleansOrphanedSuccessfulMigrationRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yuri.sqlite3")
	database := createLegacyDatabase(t, path, 4)
	migrations, err := Migrations()
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if _, err := createBackup(context.Background(), database, path, migrations[4:]); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), database); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if len(backupFiles(t, path)) != 1 {
		t.Fatal("test setup did not leave rollback snapshot")
	}

	database, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if backups := backupFiles(t, path); len(backups) != 0 {
		t.Fatalf("orphaned successful-migration backups = %#v", backups)
	}
}

func TestCreateBackupRetentionAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yuri.sqlite3")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	firstBackup, err := CreateBackup(context.Background(), database, path)
	if err != nil {
		t.Fatal(err)
	}
	if firstBackup.SourcePath != path {
		t.Fatalf("CreateBackup() source path = %q, want %q", firstBackup.SourcePath, path)
	}
	metadataBytes, err := os.ReadFile(backupMetadataPath(firstBackup.Path))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(metadataBytes, []byte(path)) {
		t.Fatal("backup metadata contains an absolute database path")
	}
	for index := 0; index < DefaultBackupRetention+1; index++ {
		if _, err := CreateBackup(context.Background(), database, path); err != nil {
			t.Fatalf("CreateBackup(%d): %v", index, err)
		}
	}
	if backups := backupFiles(t, path); len(backups) != DefaultBackupRetention {
		t.Fatalf("retained backups = %#v, want %d", backups, DefaultBackupRetention)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CreateBackup(canceled, database, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled CreateBackup() error = %v", err)
	}
}

func createLegacyDatabase(t *testing.T, path string, version int) *sql.DB {
	t.Helper()
	migrations, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", sqliteFileDSN(path, false))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Ping(); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.Version > version {
			break
		}
		transaction, err := database.Begin()
		if err != nil {
			database.Close()
			t.Fatal(err)
		}
		if _, err := transaction.Exec(migration.SQL); err != nil {
			_ = transaction.Rollback()
			database.Close()
			t.Fatalf("legacy migration %d: %v", migration.Version, err)
		}
		if _, err := transaction.Exec("INSERT INTO schema_migrations(version, name, checksum) VALUES (?, ?, ?)", migration.Version, migration.Name, migration.Checksum); err != nil {
			_ = transaction.Rollback()
			database.Close()
			t.Fatalf("legacy migration record %d: %v", migration.Version, err)
		}
		if err := transaction.Commit(); err != nil {
			database.Close()
			t.Fatalf("legacy migration commit %d: %v", migration.Version, err)
		}
	}
	return database
}

func backupFiles(t *testing.T, path string) []string {
	t.Helper()
	backups, err := filepath.Glob(path + ".backup-*.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	return backups
}

func checkOwnerOnly(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("%s permissions = %o, want 600", path, got)
	}
}
