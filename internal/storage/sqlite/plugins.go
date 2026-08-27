package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// PluginRecord is the authoritative metadata for one locally installed plugin.
// ManifestJSON is the validated manifest snapshot used for this installation.
type PluginRecord struct {
	ID              domain.ID `json:"id"`
	Name            string    `json:"name"`
	Publisher       string    `json:"publisher"`
	Version         string    `json:"version"`
	ProtocolVersion string    `json:"protocol_version"`
	Enabled         bool      `json:"enabled"`
	InstallPath     string    `json:"install_path"`
	ManifestJSON    string    `json:"manifest_json"`
	SignatureStatus string    `json:"signature_status"`
	Checksum        string    `json:"checksum"`
	RuntimeStatus   string    `json:"runtime_status"`
	LastError       string    `json:"last_error,omitempty"`
	InstalledAt     time.Time `json:"installed_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type PluginSource struct {
	PluginID      domain.ID `json:"plugin_id"`
	RepositoryURL string    `json:"repository_url,omitempty"`
	ReleaseTag    string    `json:"release_tag,omitempty"`
	CommitSHA     string    `json:"commit_sha,omitempty"`
	Checksum      string    `json:"checksum,omitempty"`
	CheckedAt     time.Time `json:"checked_at"`
}

type PluginRepository struct{ database *sql.DB }

func NewPluginRepository(database *sql.DB) *PluginRepository {
	return &PluginRepository{database: database}
}

func (repository *PluginRepository) Upsert(ctx context.Context, plugin PluginRecord, source PluginSource) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := validatePluginRecord(plugin); err != nil {
		return err
	}
	installedAt, _ := timeValue(plugin.InstalledAt)
	updatedAt, _ := timeValue(plugin.UpdatedAt)
	checkedAt, err := timeValue(source.CheckedAt)
	if err != nil {
		return err
	}
	transaction, err := repository.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin plugin upsert: %w", err)
	}
	defer transaction.Rollback()
	_, err = transaction.ExecContext(ctx, `
		INSERT INTO plugins (
			id, name, publisher, version, protocol_version, enabled, install_path,
			manifest_json, signature_status, checksum, runtime_status, last_error,
			installed_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			publisher = excluded.publisher,
			version = excluded.version,
			protocol_version = excluded.protocol_version,
			enabled = excluded.enabled,
			install_path = excluded.install_path,
			manifest_json = excluded.manifest_json,
			signature_status = excluded.signature_status,
			checksum = excluded.checksum,
			runtime_status = excluded.runtime_status,
			last_error = excluded.last_error,
			updated_at = excluded.updated_at`,
		plugin.ID, plugin.Name, plugin.Publisher, plugin.Version, plugin.ProtocolVersion,
		plugin.Enabled, plugin.InstallPath, plugin.ManifestJSON, plugin.SignatureStatus,
		plugin.Checksum, plugin.RuntimeStatus, plugin.LastError, installedAt, updatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert plugin: %w", translateSQLError(err))
	}
	_, err = transaction.ExecContext(ctx, `
		INSERT INTO plugin_sources (
			plugin_id, repository_url, release_tag, commit_sha, checksum, checked_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(plugin_id) DO UPDATE SET
			repository_url = excluded.repository_url,
			release_tag = excluded.release_tag,
			commit_sha = excluded.commit_sha,
			checksum = excluded.checksum,
			checked_at = excluded.checked_at`,
		source.PluginID, source.RepositoryURL, source.ReleaseTag, source.CommitSHA, source.Checksum, checkedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert plugin source: %w", translateSQLError(err))
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit plugin upsert: %w", err)
	}
	return nil
}

func (repository *PluginRepository) Get(ctx context.Context, id domain.ID) (PluginRecord, error) {
	if id.Empty() {
		return PluginRecord{}, fmt.Errorf("%w: plugin id is required", domain.ErrInvalidArgument)
	}
	return scanPlugin(repository.database.QueryRowContext(ctx, pluginSelect+" WHERE id = ?", id))
}

func (repository *PluginRepository) List(ctx context.Context) ([]PluginRecord, error) {
	rows, err := repository.database.QueryContext(ctx, pluginSelect+" ORDER BY name COLLATE NOCASE, id")
	if err != nil {
		return nil, fmt.Errorf("list plugins: %w", err)
	}
	defer rows.Close()
	result := make([]PluginRecord, 0)
	for rows.Next() {
		plugin, err := scanPlugin(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, plugin)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate plugins: %w", err)
	}
	return result, nil
}

func (repository *PluginRepository) SetEnabled(ctx context.Context, id domain.ID, enabled bool, updatedAt time.Time) error {
	return repository.update(ctx, id, "enabled = ?, updated_at = ?", enabled, updatedAt.UTC().Format(time.RFC3339Nano))
}

func (repository *PluginRepository) SetRuntimeStatus(ctx context.Context, id domain.ID, status, lastError string, updatedAt time.Time) error {
	status = strings.TrimSpace(status)
	if status == "" {
		return fmt.Errorf("%w: runtime status is required", domain.ErrInvalidArgument)
	}
	return repository.update(ctx, id, "runtime_status = ?, last_error = ?, updated_at = ?", status, lastError, updatedAt.UTC().Format(time.RFC3339Nano))
}

func (repository *PluginRepository) Delete(ctx context.Context, id domain.ID) error {
	result, err := repository.database.ExecContext(ctx, "DELETE FROM plugins WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete plugin: %w", translateSQLError(err))
	}
	return requireAffected(result)
}

func (repository *PluginRepository) update(ctx context.Context, id domain.ID, statement string, arguments ...any) error {
	if id.Empty() {
		return fmt.Errorf("%w: plugin id is required", domain.ErrInvalidArgument)
	}
	arguments = append(arguments, id)
	result, err := repository.database.ExecContext(ctx, "UPDATE plugins SET "+statement+" WHERE id = ?", arguments...)
	if err != nil {
		return fmt.Errorf("update plugin: %w", translateSQLError(err))
	}
	return requireAffected(result)
}

func (repository *PluginRepository) ReplaceGrants(ctx context.Context, subjectID domain.ID, grants []domain.PermissionGrant) error {
	if subjectID.Empty() {
		return fmt.Errorf("%w: subject id is required", domain.ErrInvalidArgument)
	}
	transaction, err := repository.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replace grants: %w", err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, "DELETE FROM permission_grants WHERE subject_id = ?", subjectID); err != nil {
		return fmt.Errorf("clear grants: %w", err)
	}
	for _, grant := range grants {
		if !grant.Valid() || grant.SubjectID != subjectID {
			return fmt.Errorf("%w: invalid permission grant", domain.ErrInvalidArgument)
		}
		scope, err := marshalJSON(grant.Scope, "{}")
		if err != nil {
			return err
		}
		grantedAt, _ := timeValue(grant.GrantedAt)
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO permission_grants(id, subject_id, capability, scope_json, granted_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?)`, grant.ID, grant.SubjectID, grant.Capability, scope, grantedAt, nullableTimeValue(grant.ExpiresAt)); err != nil {
			return fmt.Errorf("insert grant: %w", translateSQLError(err))
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit grants: %w", err)
	}
	return nil
}

func (repository *PluginRepository) Grants(ctx context.Context, subjectID domain.ID) ([]domain.PermissionGrant, error) {
	rows, err := repository.database.QueryContext(ctx, `
		SELECT id, subject_id, capability, scope_json, granted_at, expires_at
		FROM permission_grants WHERE subject_id = ? ORDER BY capability, id`, subjectID)
	if err != nil {
		return nil, fmt.Errorf("list grants: %w", err)
	}
	defer rows.Close()
	result := make([]domain.PermissionGrant, 0)
	for rows.Next() {
		var grant domain.PermissionGrant
		var scopeJSON, grantedAt string
		var expiresAt sql.NullString
		if err := rows.Scan(&grant.ID, &grant.SubjectID, &grant.Capability, &scopeJSON, &grantedAt, &expiresAt); err != nil {
			return nil, fmt.Errorf("scan grant: %w", err)
		}
		if err := json.Unmarshal([]byte(scopeJSON), &grant.Scope); err != nil {
			return nil, fmt.Errorf("decode grant scope: %w", err)
		}
		grant.GrantedAt, err = scanTime(grantedAt)
		if err != nil {
			return nil, err
		}
		grant.ExpiresAt, err = scanNullableTime(expiresAt)
		if err != nil {
			return nil, err
		}
		result = append(result, grant)
	}
	return result, rows.Err()
}

const pluginSelect = `SELECT id, name, publisher, version, protocol_version, enabled,
	install_path, manifest_json, signature_status, checksum, runtime_status,
	last_error, installed_at, updated_at FROM plugins`

type rowScanner interface{ Scan(...any) error }

func scanPlugin(row rowScanner) (PluginRecord, error) {
	var plugin PluginRecord
	var enabled int
	var installedAt, updatedAt string
	if err := row.Scan(&plugin.ID, &plugin.Name, &plugin.Publisher, &plugin.Version,
		&plugin.ProtocolVersion, &enabled, &plugin.InstallPath, &plugin.ManifestJSON,
		&plugin.SignatureStatus, &plugin.Checksum, &plugin.RuntimeStatus,
		&plugin.LastError, &installedAt, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return PluginRecord{}, domain.ErrNotFound
		}
		return PluginRecord{}, fmt.Errorf("scan plugin: %w", err)
	}
	plugin.Enabled = enabled != 0
	var err error
	plugin.InstalledAt, err = scanTime(installedAt)
	if err != nil {
		return PluginRecord{}, err
	}
	plugin.UpdatedAt, err = scanTime(updatedAt)
	return plugin, err
}

func validatePluginRecord(plugin PluginRecord) error {
	if plugin.ID.Empty() || strings.TrimSpace(plugin.Name) == "" || strings.TrimSpace(plugin.Publisher) == "" ||
		strings.TrimSpace(plugin.Version) == "" || strings.TrimSpace(plugin.ProtocolVersion) == "" ||
		strings.TrimSpace(plugin.InstallPath) == "" || strings.TrimSpace(plugin.SignatureStatus) == "" ||
		strings.TrimSpace(plugin.RuntimeStatus) == "" || plugin.InstalledAt.IsZero() || plugin.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: incomplete plugin record", domain.ErrInvalidArgument)
	}
	if err := validJSON(plugin.ManifestJSON, "manifest_json"); err != nil {
		return err
	}
	return nil
}

func requireAffected(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected rows: %w", err)
	}
	if count == 0 {
		return domain.ErrNotFound
	}
	return nil
}
