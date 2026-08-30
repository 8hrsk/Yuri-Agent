package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestCreateBackupDoesNotHoldTheApplicationConnection pins the second half of
// M-6. The application pool is a single connection, so a backup that borrowed
// it froze every reader and writer for the whole copy. The test holds that one
// connection for the duration of the backup: before the fix CreateBackup could
// not obtain it and blocked until the context expired.
func TestCreateBackupDoesNotHoldTheApplicationConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yuri.sqlite3")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Occupy the single pooled connection, exactly as an in-flight query or
	// transaction would.
	held, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if _, err := held.ExecContext(ctx, `CREATE TABLE backup_probe(id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}

	metadata, err := CreateBackup(ctx, database, path)
	if err != nil {
		t.Fatalf("CreateBackup() while the application connection is busy: %v", err)
	}
	if metadata.Path == "" || metadata.Size == 0 {
		t.Fatalf("backup metadata = %#v", metadata)
	}

	// The held connection is still usable, so the backup neither took it nor
	// left the database locked behind it.
	if _, err := held.ExecContext(ctx, `INSERT INTO backup_probe(value) VALUES ('after-backup')`); err != nil {
		t.Fatalf("write on the held connection after the backup: %v", err)
	}
	var rows int
	if err := held.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_probe`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("backup_probe rows = %d, want 1", rows)
	}
}

// concurrentBackupWriteWindow bounds how long the test hammers writes at the
// database while a snapshot is in flight. See the comment on
// TestCreateBackupRunsAlongsideConcurrentWrites for why the window has to be
// bounded rather than open-ended.
const concurrentBackupWriteWindow = 2 * time.Second

// minWritesDuringBackup is the throughput floor that separates the fixed
// behaviour from the M-6 regression. Measured on this fixture: with the
// snapshot on its own connection between 679 (no -race) and 25679 (-race)
// writes complete while it runs; with the snapshot on the application pool's
// single connection only 2-3 complete, and each of those blocks for ~97% of
// the whole backup. The two populations are two orders of magnitude apart, so
// any floor between them is stable.
const minWritesDuringBackup = 50

// TestCreateBackupRunsAlongsideConcurrentWrites checks the M-6 property from
// the application's side: writes issued while a backup is running complete
// instead of queueing behind it.
//
// Two things about the shape of this test are deliberate, because the obvious
// shape does not work.
//
// First, the write loop runs for a bounded window and then stops, instead of
// writing until the backup finishes. SQLite's online backup API restarts the
// copy from the current snapshot whenever the source is written through
// another connection, which is precisely the mechanism that lets writes keep
// running (see onlineBackup). An unbounded write loop therefore starves the
// copy: instrumenting backup.Step showed 399 restarts and 9896 steps to copy a
// 652-page database that needs 21 clean steps, with convergence times ranging
// from 0.69s to over 30s on the same machine. That is a property of the backup
// API under an unrealistic write storm, not a defect in the storage layer, and
// asserting on it only produces a flaky test. Once the window closes the copy
// converges immediately.
//
// Second, every write gets a deadline of its own rather than sharing the
// backup's context. When the two share a context, a copy that fails to
// converge kills the context and the *next write* reports "context deadline
// exceeded" - blaming the write for a stall that belongs to the backup.
func TestCreateBackupRunsAlongsideConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yuri.sqlite3")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := database.ExecContext(ctx, `CREATE TABLE backup_probe(id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	// Enough pages that the copy takes several backup.Step cycles.
	for index := 0; index < 2000; index++ {
		if _, err := database.ExecContext(ctx, `INSERT INTO backup_probe(value) VALUES (?)`, "seed-payload-for-page-growth"); err != nil {
			t.Fatal(err)
		}
	}

	done := make(chan error, 1)
	go func() {
		_, backupErr := CreateBackup(ctx, database, path)
		done <- backupErr
	}()

	writes := 0
	var slowestWrite time.Duration
	windowStart := time.Now()
	backupFinished := false

writeLoop:
	for time.Since(windowStart) < concurrentBackupWriteWindow {
		select {
		case backupErr := <-done:
			if backupErr != nil {
				t.Fatalf("CreateBackup() error = %v", backupErr)
			}
			backupFinished = true
			break writeLoop
		default:
		}
		// A deadline of its own: a slow copy must not be able to expire the
		// context this write depends on, or the failure is reported against
		// the wrong operation.
		writeCtx, writeCancel := context.WithTimeout(context.Background(), 30*time.Second)
		started := time.Now()
		_, err := database.ExecContext(writeCtx, `INSERT INTO backup_probe(value) VALUES ('concurrent')`)
		latency := time.Since(started)
		writeCancel()
		if err != nil {
			t.Fatalf("write %d while a backup is running (blocked %v): %v", writes+1, latency, err)
		}
		writes++
		if latency > slowestWrite {
			slowestWrite = latency
		}
	}

	// The write storm is over; the copy now converges without restarts.
	if !backupFinished {
		select {
		case backupErr := <-done:
			if backupErr != nil {
				t.Fatalf("CreateBackup() error = %v (after %d concurrent writes)", backupErr, writes)
			}
		case <-ctx.Done():
			t.Fatalf("CreateBackup() did not finish within %v after the write window closed; %d writes had completed", ctx.Err(), writes)
		}
	}

	if writes < minWritesDuringBackup {
		t.Fatalf("only %d writes completed while the backup was running, want at least %d: "+
			"the snapshot is serialising against the application pool's single connection "+
			"(slowest write %v)", writes, minWritesDuringBackup, slowestWrite)
	}
	t.Logf("%d writes completed alongside the backup, slowest %v", writes, slowestWrite)
}
