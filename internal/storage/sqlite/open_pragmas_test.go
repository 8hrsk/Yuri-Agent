package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestOpenSetsWALAndNormalSynchronous pins L-4: WAL without an explicit
// synchronous leaves SQLite at FULL, which fsyncs on every commit.
func TestOpenSetsWALAndNormalSynchronous(t *testing.T) {
	database, err := Open(context.Background(), filepath.Join(t.TempDir(), "yuri.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	var journalMode string
	if err := database.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
	var synchronous int
	if err := database.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	// 0 = OFF, 1 = NORMAL, 2 = FULL, 3 = EXTRA.
	if synchronous != 1 {
		t.Fatalf("synchronous = %d, want 1 (NORMAL)", synchronous)
	}
}

// TestOpenUsesQuickIntegrityCheck pins the first half of M-6: reopening an
// existing database must not pay for a full integrity_check, whose cost grows
// with the size of the database.
func TestOpenUsesQuickIntegrityCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yuri.sqlite3")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	original := startupIntegrityCheck
	if reflect.ValueOf(original).Pointer() != reflect.ValueOf(QuickIntegrityCheck).Pointer() {
		t.Fatal("the normal open path does not use QuickIntegrityCheck")
	}
	if reflect.ValueOf(original).Pointer() == reflect.ValueOf(IntegrityCheck).Pointer() {
		t.Fatal("the normal open path still runs the full integrity_check")
	}

	t.Cleanup(func() { startupIntegrityCheck = original })
	calls := 0
	startupIntegrityCheck = func(ctx context.Context, db *sql.DB) error {
		calls++
		return original(ctx, db)
	}

	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if calls != 1 {
		t.Fatalf("startup integrity check ran %d times, want 1", calls)
	}
}

// TestQuickIntegrityCheckReportsCorruption keeps the cheaper startup gate
// honest: it must still refuse a database whose pages are damaged.
func TestQuickIntegrityCheckReportsCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yuri.sqlite3")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := InspectQuickIntegrity(context.Background(), database)
	if err != nil || !report.OK || len(report.Diagnostics) != 0 {
		t.Fatalf("quick check on a healthy database = %#v, %v", report, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, 16); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), path); err == nil {
		t.Fatal("Open() accepted a corrupted database")
	} else if !errors.Is(err, ErrDatabaseIntegrity) {
		t.Fatalf("Open() error = %v, want ErrDatabaseIntegrity", err)
	}
}
