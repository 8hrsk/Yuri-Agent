package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// Open creates the authoritative local database, applies safe connection
// pragmas, rejects an unhealthy existing database, snapshots it before any
// pending migration, and runs all embedded schema migrations.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("sqlite path must be absolute: %q", path)
	}
	path = filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create sqlite directory: %w", err)
	}

	existing, err := existingDatabaseFile(path)
	if err != nil {
		return nil, err
	}
	dsn := sqliteFileDSN(path, false)
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A single writer connection avoids lock amplification in the initial
	// single-process foundation. This can be revisited with repository metrics.
	database.SetMaxOpenConns(1)
	if err := database.PingContext(ctx); err != nil {
		database.Close()
		if existing && isIntegrityPingError(err) {
			return nil, fmt.Errorf("startup sqlite integrity check: %w: ping: %v", ErrDatabaseIntegrity, err)
		}
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if existing {
		// quick_check, not integrity_check: the full check reads and verifies
		// every index entry and grows with the size of the database, so it
		// turned every start into a multi-second scan. quick_check finds the
		// page-level corruption that makes a database unusable, which is what
		// startup has to refuse to open. The full check stays available to
		// callers through IntegrityCheck/InspectIntegrity for an explicit
		// repair or verify run.
		if err := startupIntegrityCheck(ctx, database); err != nil {
			database.Close()
			return nil, fmt.Errorf("startup sqlite integrity check: %w", err)
		}
	}
	migrations, err := Migrations()
	if err != nil {
		database.Close()
		return nil, err
	}
	pending, err := pendingMigrations(ctx, database, migrations)
	if err != nil {
		database.Close()
		return nil, err
	}
	var rollbackSnapshot BackupMetadata
	if existing && len(pending) > 0 {
		rollbackSnapshot, err = createBackup(ctx, database, path, pending)
		if err != nil {
			database.Close()
			return nil, fmt.Errorf("pre-migration sqlite backup: %w", err)
		}
	} else if existing {
		// A process may have crashed after committing a migration but before
		// deleting its rollback artifact. Remove only snapshots whose metadata
		// identifies them as migration rollback copies; explicit CreateBackup
		// snapshots remain available to their caller.
		if err := cleanupRollbackSnapshots(ctx, path); err != nil {
			database.Close()
			return nil, fmt.Errorf("cleanup stale sqlite rollback snapshots: %w", err)
		}
	}
	if err := Migrate(ctx, database); err != nil {
		database.Close()
		return nil, err
	}
	if rollbackSnapshot.Path != "" {
		if err := removeRollbackSnapshot(rollbackSnapshot); err != nil {
			database.Close()
			return nil, fmt.Errorf("remove successful-migration sqlite rollback snapshot: %w", err)
		}
		if err := cleanupRollbackSnapshots(ctx, path); err != nil {
			database.Close()
			return nil, fmt.Errorf("cleanup previous sqlite rollback snapshots: %w", err)
		}
	}
	if err := checkContext(ctx); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

func isIntegrityPingError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "malformed") ||
		strings.Contains(message, "not a database") ||
		strings.Contains(message, "file is encrypted")
}

func sqliteFileDSN(path string, readOnly bool) string {
	databaseURL := url.URL{Scheme: "file", Path: path}
	query := databaseURL.Query()
	if readOnly {
		query.Set("mode", "ro")
		query.Add("_pragma", "query_only(1)")
	} else {
		query.Add("_pragma", "busy_timeout(5000)")
		query.Add("_pragma", "foreign_keys(1)")
		query.Add("_pragma", "journal_mode(WAL)")
		// synchronous is set explicitly because the SQLite default in WAL mode
		// is FULL, which fsyncs on every commit. This database commits on
		// paths that are not user-visible writes at all - the recall "touch",
		// every scheduler lease renewal - so FULL is the write-throughput
		// ceiling of the whole storage layer. NORMAL keeps commits durable
		// across a process or application crash and risks only the most recent
		// transactions on an OS crash or power loss, which is the accepted
		// trade-off for a local-first desktop app that also keeps
		// pre-migration snapshots and user backups.
		query.Add("_pragma", "synchronous(NORMAL)")
	}
	databaseURL.RawQuery = query.Encode()
	return databaseURL.String()
}

func existingDatabaseFile(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			// A WAL without its main database cannot be safely recovered by
			// creating a new database at the same path.
			if _, walErr := os.Stat(path + "-wal"); walErr == nil {
				return false, fmt.Errorf("sqlite database is missing but WAL sidecar exists: %q", path)
			} else if !os.IsNotExist(walErr) {
				return false, fmt.Errorf("inspect sqlite WAL sidecar: %w", walErr)
			}
			return false, nil
		}
		return false, fmt.Errorf("inspect sqlite database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("sqlite path is not a regular file: %q", path)
	}
	if info.Size() == 0 {
		return false, fmt.Errorf("%w: sqlite database is empty or truncated: %q", ErrDatabaseIntegrity, path)
	}
	return true, nil
}

func pendingMigrations(ctx context.Context, database *sql.DB, migrations []Migration) ([]Migration, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	var tableExists int
	if err := database.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'
		)`).Scan(&tableExists); err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return nil, contextErr
		}
		return nil, fmt.Errorf("inspect schema migrations table: %w", err)
	}
	if tableExists == 0 {
		return append([]Migration(nil), migrations...), nil
	}
	applied, err := appliedMigrations(ctx, database)
	if err != nil {
		return nil, err
	}
	pending := make([]Migration, 0)
	for _, migration := range migrations {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		if _, ok := applied[migration.Version]; !ok {
			pending = append(pending, migration)
		}
	}
	return pending, nil
}

// removeRollbackSnapshot removes the ephemeral raw snapshot created for one
// startup migration. A successful migration no longer needs rollback data;
// keeping it would turn a transient recovery artifact into an unencrypted
// portable copy of the user's database.
func removeRollbackSnapshot(metadata BackupMetadata) error {
	if strings.TrimSpace(metadata.Path) == "" || !filepath.IsAbs(metadata.Path) {
		return fmt.Errorf("rollback snapshot path must be absolute")
	}
	paths := []string{metadata.Path, backupMetadataPath(metadata.Path)}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("inspect rollback snapshot %q: %w", path, err)
		}
		if info.IsDir() {
			return fmt.Errorf("rollback snapshot path is a directory: %q", path)
		}
		// os.Remove unlinks a symlink itself and does not follow it. This is
		// safe for the exact, generated artifact path and avoids retaining a
		// dangling metadata/snapshot name after an interrupted cleanup.
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove rollback snapshot %q: %w", path, err)
		}
	}
	return syncDirectory(filepath.Dir(metadata.Path))
}

func cleanupRollbackSnapshots(ctx context.Context, databasePath string) error {
	entries, err := os.ReadDir(filepath.Dir(databasePath))
	if err != nil {
		return err
	}
	prefix := filepath.Base(databasePath) + ".backup-"
	for _, entry := range entries {
		if err := checkContext(ctx); err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) || !strings.HasSuffix(entry.Name(), ".sqlite3") {
			continue
		}
		path := filepath.Join(filepath.Dir(databasePath), entry.Name())
		metadata, err := ReadBackupMetadata(path)
		if err != nil || len(metadata.PendingMigrations) == 0 || filepath.Clean(metadata.SourcePath) != filepath.Clean(databasePath) {
			continue
		}
		if err := removeRollbackSnapshot(metadata); err != nil {
			return err
		}
	}
	return nil
}
