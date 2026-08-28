// Package sqlite contains the authoritative SQLite storage foundation.
package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// compatibleMigrationChecksums contains checksums emitted by pre-release
// builds whose SQL schema is identical to the committed migration. Keep this
// list exact and intentionally small: arbitrary checksum mismatches must still
// stop startup instead of silently accepting a modified migration.
var compatibleMigrationChecksums = map[int]map[string]struct{}{
	4: {
		"6a6e29516f13a6d786e420025101e65ca1ebbdee37673ceac85988ac915ee4d3": {},
	},
}

// Migration is an immutable schema change bundled with the application.
type Migration struct {
	Version  int
	Name     string
	SQL      string
	Checksum string
}

// Migrations returns all embedded migrations in ascending order.
func Migrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	migrations := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		parts := strings.SplitN(entry.Name(), "_", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid migration filename %q", entry.Name())
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid migration version in %q", entry.Name())
		}
		content, err := fs.ReadFile(migrationFiles, "migrations/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		digest := sha256.Sum256(content)
		migrations = append(migrations, Migration{
			Version:  version,
			Name:     strings.TrimSuffix(parts[1], ".sql"),
			SQL:      string(content),
			Checksum: hex.EncodeToString(digest[:]),
		})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	for index := 1; index < len(migrations); index++ {
		if migrations[index-1].Version == migrations[index].Version {
			return nil, fmt.Errorf("duplicate migration version %d", migrations[index].Version)
		}
	}
	return migrations, nil
}

// Migrate applies pending migrations and rejects modified migrations that were
// already applied. Open performs startup integrity checks and pre-migration
// backups; callers invoking Migrate directly own that lifecycle themselves.
func Migrate(ctx context.Context, database *sql.DB) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if database == nil {
		return fmt.Errorf("sqlite database is required")
	}
	migrations, err := Migrations()
	if err != nil {
		return err
	}
	if _, err := database.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}

	applied, err := appliedMigrations(ctx, database)
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		if err := checkContext(ctx); err != nil {
			return err
		}
		if checksum, ok := applied[migration.Version]; ok {
			if checksum != migration.Checksum {
				if !compatibleMigrationChecksum(migration.Version, checksum) {
					return fmt.Errorf("migration %d checksum changed", migration.Version)
				}
				result, err := database.ExecContext(ctx,
					"UPDATE schema_migrations SET checksum = ? WHERE version = ? AND checksum = ?",
					migration.Checksum, migration.Version, checksum,
				)
				if err != nil {
					return fmt.Errorf("upgrade compatible migration %d checksum: %w", migration.Version, err)
				}
				changed, err := result.RowsAffected()
				if err != nil || changed != 1 {
					return fmt.Errorf("upgrade compatible migration %d checksum: concurrent migration change", migration.Version)
				}
			}
			continue
		}
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", migration.Version, err)
		}
		if _, err := transaction.ExecContext(ctx, migration.SQL); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("apply migration %d: %w", migration.Version, err)
		}
		if _, err := transaction.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, name, checksum) VALUES (?, ?, ?)",
			migration.Version, migration.Name, migration.Checksum,
		); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("record migration %d: %w", migration.Version, err)
		}
		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", migration.Version, err)
		}
	}
	return nil
}

func compatibleMigrationChecksum(version int, checksum string) bool {
	allowed, ok := compatibleMigrationChecksums[version]
	if !ok {
		return false
	}
	_, ok = allowed[checksum]
	return ok
}

func appliedMigrations(ctx context.Context, database *sql.DB) (map[int]string, error) {
	rows, err := database.QueryContext(ctx, "SELECT version, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, fmt.Errorf("query applied migrations: %w", err)
	}
	defer rows.Close()
	result := make(map[int]string)
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, fmt.Errorf("scan applied migration: %w", err)
		}
		result[version] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied migrations: %w", err)
	}
	return result, nil
}
