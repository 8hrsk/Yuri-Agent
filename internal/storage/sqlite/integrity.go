package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	moderncsqlite "modernc.org/sqlite"
)

const (
	// DefaultBackupRetention is the number of pre-migration snapshots kept for
	// one database. Older snapshots are removed after a new snapshot is
	// durably committed.
	DefaultBackupRetention = 3

	backupMetadataVersion = 1
	backupStepPages       = 32
)

// ErrDatabaseIntegrity is returned when SQLite reports that the database is
// corrupt or cannot be checked. Callers can use errors.Is to distinguish a
// corrupt database from an unavailable database or a migration error.
var ErrDatabaseIntegrity = errors.New("sqlite database integrity check failed")

// ErrIntegrityCheck is kept as a descriptive alias for callers that use the
// shorter integrity-check terminology.
var ErrIntegrityCheck = ErrDatabaseIntegrity

// IntegrityError contains all diagnostics returned by SQLite's
// integrity_check pragma. SQLite may return more than one diagnostic row, so
// callers should not rely on only the first one.
type IntegrityError struct {
	Diagnostics []string
}

func (e *IntegrityError) Error() string {
	if len(e.Diagnostics) == 0 {
		return ErrDatabaseIntegrity.Error()
	}
	return fmt.Sprintf("%s: %s", ErrDatabaseIntegrity, strings.Join(e.Diagnostics, "; "))
}

func (e *IntegrityError) Unwrap() error { return ErrDatabaseIntegrity }

// IntegrityReport is the read-only result of SQLite's integrity_check
// pragma. A successful report contains one "ok" result and no diagnostics.
type IntegrityReport struct {
	OK          bool     `json:"ok"`
	Diagnostics []string `json:"diagnostics,omitempty"`
}

// InspectIntegrity runs SQLite's read-only integrity_check pragma and
// returns every diagnostic row. It does not create tables, change pragmas, or
// otherwise mutate the database. The full check verifies every index entry
// against its table, so its cost grows with the size of the database; startup
// uses InspectQuickIntegrity instead and this remains the explicit
// repair/verify path.
func InspectIntegrity(ctx context.Context, database *sql.DB) (IntegrityReport, error) {
	return inspectIntegrityPragma(ctx, database, "integrity_check")
}

// InspectQuickIntegrity runs SQLite's read-only quick_check pragma. It skips
// the index-content verification that dominates the cost of integrity_check
// while still detecting the page-level corruption that makes a database
// unusable, which is the condition startup must refuse to open on.
func InspectQuickIntegrity(ctx context.Context, database *sql.DB) (IntegrityReport, error) {
	return inspectIntegrityPragma(ctx, database, "quick_check")
}

func inspectIntegrityPragma(ctx context.Context, database *sql.DB, pragma string) (IntegrityReport, error) {
	if err := checkContext(ctx); err != nil {
		return IntegrityReport{}, err
	}
	if database == nil {
		return IntegrityReport{}, errors.New("sqlite database is required")
	}

	rows, err := database.QueryContext(ctx, "PRAGMA "+pragma)
	if err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return IntegrityReport{}, contextErr
		}
		return IntegrityReport{}, fmt.Errorf("%w: execute %s: %v", ErrDatabaseIntegrity, pragma, err)
	}
	defer rows.Close()

	results := make([]string, 0, 1)
	for rows.Next() {
		if err := checkContext(ctx); err != nil {
			return IntegrityReport{}, err
		}
		var result string
		if err := rows.Scan(&result); err != nil {
			return IntegrityReport{}, fmt.Errorf("%w: scan %s: %v", ErrDatabaseIntegrity, pragma, err)
		}
		results = append(results, strings.TrimSpace(result))
	}
	if err := rows.Err(); err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return IntegrityReport{}, contextErr
		}
		return IntegrityReport{}, fmt.Errorf("%w: read %s: %v", ErrDatabaseIntegrity, pragma, err)
	}
	if err := checkContext(ctx); err != nil {
		return IntegrityReport{}, err
	}
	if len(results) == 0 {
		return IntegrityReport{}, fmt.Errorf("%w: %s returned no result", ErrDatabaseIntegrity, pragma)
	}

	allOK := true
	diagnostics := make([]string, 0, len(results))
	for _, result := range results {
		if !strings.EqualFold(result, "ok") {
			allOK = false
			diagnostics = append(diagnostics, result)
		}
	}
	if !allOK {
		return IntegrityReport{OK: false, Diagnostics: diagnostics}, &IntegrityError{Diagnostics: diagnostics}
	}
	return IntegrityReport{OK: true}, nil
}

// IntegrityCheck is the read-only integrity-check API used by startup and
// diagnostic callers. It succeeds only when SQLite returns "ok" for every
// integrity_check row.
func IntegrityCheck(ctx context.Context, database *sql.DB) error {
	_, err := InspectIntegrity(ctx, database)
	return err
}

// CheckIntegrity is an alias for IntegrityCheck for callers that prefer the
// verb-first name.
func CheckIntegrity(ctx context.Context, database *sql.DB) error {
	return IntegrityCheck(ctx, database)
}

// QuickIntegrityCheck is the cheap startup gate. It succeeds only when SQLite
// returns "ok" for every quick_check row.
func QuickIntegrityCheck(ctx context.Context, database *sql.DB) error {
	_, err := InspectQuickIntegrity(ctx, database)
	return err
}

// startupIntegrityCheck is the check Open runs against an existing database.
// It is a variable so tests can observe that the normal open path takes the
// cheap quick_check route rather than the full integrity_check.
var startupIntegrityCheck = QuickIntegrityCheck

// BackupMigration describes one migration that was pending when a snapshot
// was taken.
type BackupMigration struct {
	Version  int    `json:"version"`
	Name     string `json:"name"`
	Checksum string `json:"checksum"`
}

// BackupMetadata describes an atomic snapshot. It is an ephemeral rollback
// artifact for migration recovery, not a portable backup: it is raw SQLite
// with the same at-rest exposure as the active database. Checksum is the
// SHA-256 digest of the completed SQLite snapshot, not of the live database
// or its WAL sidecar. Path and SourcePath are absolute paths in the returned
// Go value; the on-disk metadata sidecar stores only their basenames.
type BackupMetadata struct {
	Version           int               `json:"version"`
	SourcePath        string            `json:"source_path"`
	Path              string            `json:"path"`
	CreatedAt         time.Time         `json:"created_at"`
	Checksum          string            `json:"checksum"`
	ChecksumAlgorithm string            `json:"checksum_algorithm"`
	Size              int64             `json:"size"`
	PendingMigrations []BackupMigration `json:"pending_migrations,omitempty"`
}

// CreateBackup takes a consistent, owner-readable raw SQLite snapshot of
// database at databasePath. The snapshot is made through SQLite's online
// backup API, so committed WAL content is included. The resulting file and
// checksum metadata are committed with atomic renames, and at most
// DefaultBackupRetention snapshots are retained. This is a local rollback
// artifact with the same at-rest exposure as the active database, not a
// portable or encrypted backup.
func CreateBackup(ctx context.Context, database *sql.DB, databasePath string) (BackupMetadata, error) {
	return createBackup(ctx, database, databasePath, nil)
}

// Backup is a compatibility alias for CreateBackup.
func Backup(ctx context.Context, database *sql.DB, databasePath string) (BackupMetadata, error) {
	return CreateBackup(ctx, database, databasePath)
}

// ReadBackupMetadata reads and validates checksum metadata written by
// CreateBackup. It is intentionally read-only and does not open the backup
// as a live application database.
func ReadBackupMetadata(backupPath string) (BackupMetadata, error) {
	if strings.TrimSpace(backupPath) == "" {
		return BackupMetadata{}, errors.New("backup path is required")
	}
	metadataPath := backupMetadataPath(backupPath)
	info, err := os.Lstat(metadataPath)
	if err != nil {
		return BackupMetadata{}, fmt.Errorf("inspect backup metadata: %w", err)
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode()&0o077 != 0) {
		return BackupMetadata{}, errors.New("backup metadata is not owner-only")
	}
	content, err := os.ReadFile(metadataPath)
	if err != nil {
		return BackupMetadata{}, fmt.Errorf("read backup metadata: %w", err)
	}
	var onDisk backupMetadataFile
	if err := json.Unmarshal(content, &onDisk); err != nil {
		return BackupMetadata{}, fmt.Errorf("decode backup metadata: %w", err)
	}
	backupPath = filepath.Clean(backupPath)
	if onDisk.Version != backupMetadataVersion || onDisk.Path != filepath.Base(backupPath) || filepath.Base(onDisk.SourcePath) != onDisk.SourcePath || onDisk.ChecksumAlgorithm != "sha256" || len(onDisk.Checksum) != sha256.Size*2 {
		return BackupMetadata{}, errors.New("invalid backup metadata")
	}
	metadata := BackupMetadata{
		Version:           onDisk.Version,
		SourcePath:        filepath.Join(filepath.Dir(backupPath), onDisk.SourcePath),
		Path:              backupPath,
		CreatedAt:         onDisk.CreatedAt,
		Checksum:          onDisk.Checksum,
		ChecksumAlgorithm: onDisk.ChecksumAlgorithm,
		Size:              onDisk.Size,
		PendingMigrations: onDisk.PendingMigrations,
	}
	return metadata, nil
}

// VerifyBackup validates both the checksum metadata and SQLite integrity of a
// completed snapshot. It performs only reads against the backup file.
func VerifyBackup(ctx context.Context, backupPath string) (BackupMetadata, error) {
	if err := checkContext(ctx); err != nil {
		return BackupMetadata{}, err
	}
	metadata, err := ReadBackupMetadata(backupPath)
	if err != nil {
		return BackupMetadata{}, err
	}
	info, err := os.Lstat(backupPath)
	if err != nil {
		return BackupMetadata{}, fmt.Errorf("stat backup: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&0o077 != 0 || info.Size() != metadata.Size {
		return BackupMetadata{}, errors.New("backup size does not match metadata")
	}
	checksum, err := checksumFile(ctx, backupPath)
	if err != nil {
		return BackupMetadata{}, fmt.Errorf("checksum backup: %w", err)
	}
	if checksum != metadata.Checksum {
		return BackupMetadata{}, errors.New("backup checksum does not match metadata")
	}
	if err := verifySnapshot(ctx, backupPath); err != nil {
		return BackupMetadata{}, err
	}
	return metadata, nil
}

func createBackup(ctx context.Context, database *sql.DB, databasePath string, pending []Migration) (metadata BackupMetadata, err error) {
	if err := checkContext(ctx); err != nil {
		return BackupMetadata{}, err
	}
	// database is the caller's live handle. The snapshot itself is taken on a
	// dedicated connection (see onlineBackup) so it does not occupy the single
	// application connection, but an absent handle still means the caller has
	// no open database to snapshot.
	if database == nil {
		return BackupMetadata{}, errors.New("sqlite database is required")
	}
	if !filepath.IsAbs(databasePath) {
		return BackupMetadata{}, fmt.Errorf("sqlite path must be absolute: %q", databasePath)
	}

	databasePath = filepath.Clean(databasePath)
	directory := filepath.Dir(databasePath)
	if err := ensureBackupDirectory(directory); err != nil {
		return BackupMetadata{}, err
	}

	temporary, err := os.CreateTemp(directory, "."+filepath.Base(databasePath)+".backup-*.tmp")
	if err != nil {
		return BackupMetadata{}, fmt.Errorf("create temporary sqlite backup: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanupTemporary := true
	defer func() {
		if cleanupTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return BackupMetadata{}, fmt.Errorf("set temporary sqlite backup permissions: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return BackupMetadata{}, fmt.Errorf("close temporary sqlite backup: %w", err)
	}

	if err := onlineBackup(ctx, databasePath, temporaryPath); err != nil {
		return BackupMetadata{}, fmt.Errorf("create sqlite backup snapshot: %w", err)
	}
	if err := syncFile(temporaryPath); err != nil {
		return BackupMetadata{}, fmt.Errorf("sync sqlite backup snapshot: %w", err)
	}
	if err := checkContext(ctx); err != nil {
		return BackupMetadata{}, err
	}

	fileInfo, err := os.Stat(temporaryPath)
	if err != nil {
		return BackupMetadata{}, fmt.Errorf("stat sqlite backup snapshot: %w", err)
	}
	if !fileInfo.Mode().IsRegular() || fileInfo.Size() == 0 {
		return BackupMetadata{}, errors.New("sqlite backup snapshot is empty or not a regular file")
	}
	checksum, err := checksumFile(ctx, temporaryPath)
	if err != nil {
		return BackupMetadata{}, fmt.Errorf("checksum sqlite backup snapshot: %w", err)
	}
	if err := verifySnapshot(ctx, temporaryPath); err != nil {
		return BackupMetadata{}, fmt.Errorf("verify sqlite backup snapshot: %w", err)
	}

	createdAt := time.Now().UTC()
	finalPath := backupPath(databasePath, createdAt, checksum, temporaryPath)
	metadata = BackupMetadata{
		Version:           backupMetadataVersion,
		SourcePath:        databasePath,
		Path:              finalPath,
		CreatedAt:         createdAt,
		Checksum:          checksum,
		ChecksumAlgorithm: "sha256",
		Size:              fileInfo.Size(),
		PendingMigrations: backupMigrations(pending),
	}

	if err := checkContext(ctx); err != nil {
		return BackupMetadata{}, err
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return BackupMetadata{}, fmt.Errorf("commit sqlite backup snapshot: %w", err)
	}
	cleanupTemporary = false
	if err := os.Chmod(finalPath, 0o600); err != nil {
		return metadata, fmt.Errorf("set sqlite backup permissions: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return metadata, fmt.Errorf("sync sqlite backup directory: %w", err)
	}

	if err := writeBackupMetadata(ctx, metadata); err != nil {
		// The snapshot has already been committed and remains recoverable even
		// if metadata cannot be published (for example, a full filesystem).
		return metadata, err
	}
	if err := retainBackups(ctx, databasePath); err != nil {
		return metadata, err
	}
	return metadata, nil
}

func backupMigrations(pending []Migration) []BackupMigration {
	if len(pending) == 0 {
		return nil
	}
	migrations := make([]BackupMigration, 0, len(pending))
	for _, migration := range pending {
		migrations = append(migrations, BackupMigration{
			Version:  migration.Version,
			Name:     migration.Name,
			Checksum: migration.Checksum,
		})
	}
	return migrations
}

func ensureBackupDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create sqlite backup directory: %w", err)
	}
	return nil
}

// onlineBackup copies sourcePath into destination through SQLite's online
// backup API on a connection of its own.
//
// The application pool is deliberately a single connection
// (SetMaxOpenConns(1)), so borrowing it for the whole backup.Step cycle froze
// every reader and writer in the process for as long as the copy took - on a
// multi-gigabyte database that is the entire UI hanging on a pre-migration
// snapshot. A dedicated connection is what the backup API is designed for:
// other connections keep reading and writing, and a concurrent write simply
// makes the next Step restart the copy from the current snapshot.
func onlineBackup(ctx context.Context, sourcePath, destination string) (err error) {
	if err := checkContext(ctx); err != nil {
		return err
	}
	source, err := sql.Open("sqlite", sqliteFileDSN(sourcePath, false))
	if err != nil {
		return fmt.Errorf("open sqlite backup source: %w", err)
	}
	defer func() {
		closeErr := source.Close()
		if err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	source.SetMaxOpenConns(1)
	connection, err := source.Conn(ctx)
	if err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return contextErr
		}
		return err
	}
	defer connection.Close()

	err = connection.Raw(func(driverConnection any) (callbackErr error) {
		backuper, ok := driverConnection.(interface {
			NewBackup(string) (*moderncsqlite.Backup, error)
		})
		if !ok {
			return errors.New("sqlite driver does not support online backups")
		}
		backup, err := backuper.NewBackup(destination)
		if err != nil {
			return err
		}
		defer func() {
			finishErr := backup.Finish()
			if callbackErr == nil && finishErr != nil {
				callbackErr = finishErr
			}
		}()

		for {
			if contextErr := contextError(ctx); contextErr != nil {
				return contextErr
			}
			more, stepErr := backup.Step(backupStepPages)
			if stepErr != nil {
				if contextErr := contextError(ctx); contextErr != nil {
					return contextErr
				}
				return stepErr
			}
			if !more {
				return nil
			}
		}
	})
	if contextErr := contextError(ctx); contextErr != nil {
		return contextErr
	}
	return err
}

func verifySnapshot(ctx context.Context, path string) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	dsn := sqliteFileDSN(path, true)
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	if err := database.PingContext(ctx); err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return contextErr
		}
		return err
	}
	return IntegrityCheck(ctx, database)
}

func checksumFile(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	digest := sha256.New()
	buffer := make([]byte, 64*1024)
	for {
		if err := checkContext(ctx); err != nil {
			return "", err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			if _, err := digest.Write(buffer[:count]); err != nil {
				return "", err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	if err := checkContext(ctx); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		// Windows does not permit opening a directory for FlushFileBuffers via
		// os.File.Sync. The preceding file sync still provides the available
		// durability guarantee on that platform.
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func backupPath(databasePath string, createdAt time.Time, checksum, temporaryPath string) string {
	// The timestamp is sortable, while the digest prefix prevents a same-time
	// snapshot from replacing another snapshot after a process restart. The
	// temporary filename contributes a random suffix as a final collision guard.
	stamp := createdAt.UTC().Format("20060102T150405.000000000Z")
	token := filepath.Base(temporaryPath)
	token = strings.TrimSuffix(token, ".tmp")
	token = strings.TrimPrefix(token, "."+filepath.Base(databasePath)+".backup-")
	return databasePath + ".backup-" + stamp + "-" + checksum[:16] + "-" + token + ".sqlite3"
}

func backupMetadataPath(path string) string { return path + ".metadata.json" }

// BackupMetadataPath returns the sidecar path used for a completed snapshot.
// It is useful to consumers that discover backup files by directory listing.
func BackupMetadataPath(path string) string { return backupMetadataPath(path) }

func writeBackupMetadata(ctx context.Context, metadata BackupMetadata) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	directory := filepath.Dir(metadata.Path)
	content, err := json.MarshalIndent(safeBackupMetadata(metadata), "", "  ")
	if err != nil {
		return fmt.Errorf("encode backup metadata: %w", err)
	}
	content = append(content, '\n')
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(metadata.Path)+".metadata-*.tmp")
	if err != nil {
		return fmt.Errorf("create backup metadata: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set backup metadata permissions: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write backup metadata: %w", err)
	}
	if err := checkContext(ctx); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync backup metadata: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close backup metadata: %w", err)
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, backupMetadataPath(metadata.Path)); err != nil {
		return fmt.Errorf("commit backup metadata: %w", err)
	}
	if err := os.Chmod(backupMetadataPath(metadata.Path), 0o600); err != nil {
		return fmt.Errorf("set committed backup metadata permissions: %w", err)
	}
	return syncDirectory(directory)
}

type backupMetadataFile struct {
	Version           int               `json:"version"`
	SourcePath        string            `json:"source_path"`
	Path              string            `json:"path"`
	CreatedAt         time.Time         `json:"created_at"`
	Checksum          string            `json:"checksum"`
	ChecksumAlgorithm string            `json:"checksum_algorithm"`
	Size              int64             `json:"size"`
	PendingMigrations []BackupMigration `json:"pending_migrations,omitempty"`
}

func safeBackupMetadata(metadata BackupMetadata) backupMetadataFile {
	return backupMetadataFile{
		Version:           metadata.Version,
		SourcePath:        filepath.Base(metadata.SourcePath),
		Path:              filepath.Base(metadata.Path),
		CreatedAt:         metadata.CreatedAt,
		Checksum:          metadata.Checksum,
		ChecksumAlgorithm: metadata.ChecksumAlgorithm,
		Size:              metadata.Size,
		PendingMigrations: metadata.PendingMigrations,
	}
}

func retainBackups(ctx context.Context, databasePath string) error {
	entries, err := os.ReadDir(filepath.Dir(databasePath))
	if err != nil {
		return fmt.Errorf("read sqlite backup directory: %w", err)
	}
	prefix := filepath.Base(databasePath) + ".backup-"
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) || !strings.HasSuffix(entry.Name(), ".sqlite3") {
			continue
		}
		paths = append(paths, filepath.Join(filepath.Dir(databasePath), entry.Name()))
	}
	sort.Strings(paths)
	if len(paths) <= DefaultBackupRetention {
		return nil
	}
	for _, path := range paths[:len(paths)-DefaultBackupRetention] {
		if err := checkContext(ctx); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("inspect old sqlite backup %q: %w", path, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove old sqlite backup %q: %w", path, err)
		}
		metadataPath := backupMetadataPath(path)
		if err := os.Remove(metadataPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove old sqlite backup metadata %q: %w", metadataPath, err)
		}
	}
	return syncDirectory(filepath.Dir(databasePath))
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("sqlite context is required")
	}
	return contextError(ctx)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("sqlite context is required")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
