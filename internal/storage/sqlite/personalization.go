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

// PersonalizationRepository stores the owner-authored v2 baseline separately
// from mutable persona/relationship/affect journals. Reflection has no write
// path to this repository.
type PersonalizationRepository struct {
	db *sql.DB
}

func NewPersonalizationRepository(database *sql.DB) *PersonalizationRepository {
	return &PersonalizationRepository{db: database}
}

func (r *PersonalizationRepository) Create(ctx context.Context, seed domain.PersonalizationSeed) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := seed.Validate(); err != nil {
		return err
	}
	if seed.Version != 1 {
		return fmt.Errorf("%w: initial personalization seed must be version one", domain.ErrInvalidArgument)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin create personalization seed", err)
	}
	defer tx.Rollback()
	if err := r.appendTx(ctx, tx, seed); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrappedSQLError("commit personalization seed", err)
	}
	return nil
}

func (r *PersonalizationRepository) Get(ctx context.Context, agentID domain.ID) (domain.PersonalizationSeed, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.PersonalizationSeed{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.PersonalizationSeed{}, err
	}
	if agentID.Empty() {
		return domain.PersonalizationSeed{}, fmt.Errorf("%w: personalization agent id is required", domain.ErrInvalidArgument)
	}
	return scanPersonalizationSeed(r.db.QueryRowContext(ctx, personalizationSelect+`
		FROM personalization_seed_heads AS ph
		JOIN personalization_seed_versions AS pv ON pv.agent_id = ph.agent_id AND pv.version = ph.version
		WHERE ph.agent_id = ?`, string(agentID)))
}

func (r *PersonalizationRepository) GetVersion(ctx context.Context, agentID domain.ID, version uint64) (domain.PersonalizationSeed, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.PersonalizationSeed{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.PersonalizationSeed{}, err
	}
	if agentID.Empty() || version == 0 {
		return domain.PersonalizationSeed{}, fmt.Errorf("%w: personalization agent id and version are required", domain.ErrInvalidArgument)
	}
	return scanPersonalizationSeed(r.db.QueryRowContext(ctx, personalizationSelect+`
		FROM personalization_seed_versions AS pv WHERE pv.agent_id = ? AND pv.version = ?`, string(agentID), version))
}

// AppendVersion accepts only an explicit owner revision whose parent is the
// current head. It intentionally has no model/evidence-shaped API.
func (r *PersonalizationRepository) AppendVersion(ctx context.Context, seed domain.PersonalizationSeed, expectedVersion uint64) (domain.PersonalizationSeed, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.PersonalizationSeed{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.PersonalizationSeed{}, err
	}
	if expectedVersion == 0 || seed.AgentID.Empty() {
		return domain.PersonalizationSeed{}, fmt.Errorf("%w: expected version and agent id are required", domain.ErrInvalidArgument)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.PersonalizationSeed{}, wrappedSQLError("begin append personalization seed", err)
	}
	defer tx.Rollback()
	seed, err = r.appendVersionTx(ctx, tx, seed, expectedVersion)
	if err != nil {
		return domain.PersonalizationSeed{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.PersonalizationSeed{}, wrappedSQLError("commit personalization seed revision", err)
	}
	return seed, nil
}

func (r *PersonalizationRepository) appendVersionTx(ctx context.Context, tx *sql.Tx, seed domain.PersonalizationSeed, expectedVersion uint64) (domain.PersonalizationSeed, error) {
	current, err := scanPersonalizationSeed(tx.QueryRowContext(ctx, personalizationSelect+`
		FROM personalization_seed_heads AS ph
		JOIN personalization_seed_versions AS pv ON pv.agent_id = ph.agent_id AND pv.version = ph.version
		WHERE ph.agent_id = ?`, string(seed.AgentID)))
	if err != nil {
		return domain.PersonalizationSeed{}, err
	}
	if current.Version != expectedVersion || seed.Version != expectedVersion+1 {
		return domain.PersonalizationSeed{}, domain.ErrConflict
	}
	if !seed.UpdatedAt.After(current.UpdatedAt) {
		return domain.PersonalizationSeed{}, fmt.Errorf("%w: personalization revision timestamp must advance", domain.ErrInvalidArgument)
	}
	seed.ParentVersion = current.Version
	seed.ParentID = current.RevisionID
	seed.CreatedAt = current.CreatedAt
	if seed.RevisionID.Empty() || seed.RevisionID == current.RevisionID {
		seed.RevisionID = domain.ID(fmt.Sprintf("%s:personalization:v%d", seed.AgentID, seed.Version))
	}
	if seed.Operation != domain.PersonalizationOperationUpdate && seed.Operation != domain.PersonalizationOperationReset {
		return domain.PersonalizationSeed{}, fmt.Errorf("%w: append requires owner update or reset operation", domain.ErrInvalidArgument)
	}
	if err := seed.Validate(); err != nil {
		return domain.PersonalizationSeed{}, err
	}
	if err := r.appendTx(ctx, tx, seed); err != nil {
		return domain.PersonalizationSeed{}, err
	}
	return seed, nil
}

// AppendPersonalizationWithAudit persists an explicit owner revision and its
// redacted audit event atomically. Runtime reflection has no access to this
// aggregate boundary.
func (repositories *Repositories) AppendPersonalizationWithAudit(ctx context.Context, seed domain.PersonalizationSeed, expectedVersion uint64, event AuditEvent) (domain.PersonalizationSeed, error) {
	if repositories == nil || repositories.Personalization == nil || repositories.Audit == nil {
		return domain.PersonalizationSeed{}, fmt.Errorf("%w: personalization and audit repositories are required", domain.ErrInvalidArgument)
	}
	if err := validateAuditEvent(event); err != nil {
		return domain.PersonalizationSeed{}, err
	}
	tx, err := repositories.Personalization.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.PersonalizationSeed{}, wrappedSQLError("begin audited personalization append", err)
	}
	defer tx.Rollback()
	seed, err = repositories.Personalization.appendVersionTx(ctx, tx, seed, expectedVersion)
	if err != nil {
		return domain.PersonalizationSeed{}, err
	}
	if err := appendAuditEvent(ctx, tx, event); err != nil {
		return domain.PersonalizationSeed{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.PersonalizationSeed{}, wrappedSQLError("commit audited personalization append", err)
	}
	return seed, nil
}

// MigrateLegacyBackstory is a narrow trusted migration boundary. Unlike
// AppendVersion it does not accept caller-authored replacement state: it loads
// the current head and applies the deterministic domain migration itself.
// Reflection and model output therefore cannot use it to rewrite owner identity.
func (r *PersonalizationRepository) MigrateLegacyBackstory(ctx context.Context, agentID domain.ID, now time.Time) (domain.PersonalizationSeed, bool, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.PersonalizationSeed{}, false, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.PersonalizationSeed{}, false, err
	}
	if agentID.Empty() || now.IsZero() {
		return domain.PersonalizationSeed{}, false, fmt.Errorf("%w: personalization agent id and migration timestamp are required", domain.ErrInvalidArgument)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.PersonalizationSeed{}, false, wrappedSQLError("begin migrate legacy backstory", err)
	}
	defer tx.Rollback()
	current, err := scanPersonalizationSeed(tx.QueryRowContext(ctx, personalizationSelect+`
		FROM personalization_seed_heads AS ph
		JOIN personalization_seed_versions AS pv ON pv.agent_id = ph.agent_id AND pv.version = ph.version
		WHERE ph.agent_id = ?`, string(agentID)))
	if err != nil {
		return domain.PersonalizationSeed{}, false, err
	}
	migrated, changed, err := domain.MigrateLegacyBackstory(current, now)
	if err != nil {
		return domain.PersonalizationSeed{}, false, err
	}
	if !changed {
		return current, false, nil
	}
	if err := r.appendTx(ctx, tx, migrated); err != nil {
		return domain.PersonalizationSeed{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.PersonalizationSeed{}, false, wrappedSQLError("commit migrate legacy backstory", err)
	}
	return migrated, true, nil
}

func (r *PersonalizationRepository) appendTx(ctx context.Context, tx *sql.Tx, seed domain.PersonalizationSeed) error {
	identityJSON, err := marshalJSON(seed.Identity, "{}")
	if err != nil {
		return err
	}
	communicationJSON, err := marshalJSON(seed.CommunicationStyle, "{}")
	if err != nil {
		return err
	}
	temperamentJSON, err := marshalJSON(seed.Temperament, "{}")
	if err != nil {
		return err
	}
	emotionalJSON, err := marshalJSON(seed.EmotionalDynamics, "{}")
	if err != nil {
		return err
	}
	relationshipJSON, err := marshalJSON(seed.RelationshipSeed, "{}")
	if err != nil {
		return err
	}
	backstoryJSON, err := marshalJSON(seed.Backstory, "{}")
	if err != nil {
		return err
	}
	evolutionJSON, err := marshalJSON(seed.EvolutionPolicy, "{}")
	if err != nil {
		return err
	}
	createdAt, err := timeValue(seed.CreatedAt)
	if err != nil {
		return err
	}
	updatedAt, err := timeValue(seed.UpdatedAt)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO personalization_seed_versions(
			agent_id, version, revision_id, parent_id, parent_version, schema_version, operation,
			identity_json, communication_style_json, temperament_json, emotional_dynamics_json,
			relationship_seed_json, backstory_json, evolution_policy_json, reason, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(seed.AgentID), seed.Version, string(seed.RevisionID), nullableID(seed.ParentID), seed.ParentVersion,
		seed.SchemaVersion, string(seed.Operation), identityJSON, communicationJSON, temperamentJSON, emotionalJSON,
		relationshipJSON, backstoryJSON, evolutionJSON, strings.TrimSpace(seed.Reason), createdAt, updatedAt)
	if err != nil {
		return wrappedSQLError("insert personalization seed version", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO personalization_seed_heads(agent_id, version, revision_id, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET version = excluded.version, revision_id = excluded.revision_id, updated_at = excluded.updated_at`,
		string(seed.AgentID), seed.Version, string(seed.RevisionID), updatedAt)
	return wrappedSQLError("update personalization seed head", err)
}

const personalizationSelect = `SELECT pv.agent_id, pv.schema_version, pv.version, pv.revision_id,
	pv.parent_id, pv.parent_version, pv.operation, pv.identity_json, pv.communication_style_json,
	pv.temperament_json, pv.emotional_dynamics_json, pv.relationship_seed_json, pv.backstory_json,
	pv.evolution_policy_json, pv.reason, pv.created_at, pv.updated_at `

func scanPersonalizationSeed(scanner interface{ Scan(...any) error }) (domain.PersonalizationSeed, error) {
	var (
		seed                                                            domain.PersonalizationSeed
		agentID, revisionID, operation, reason, createdAt, updatedAt    string
		parentID                                                        sql.NullString
		identityJSON, communicationJSON, temperamentJSON, emotionalJSON string
		relationshipJSON, backstoryJSON, evolutionJSON                  string
	)
	if err := scanner.Scan(
		&agentID, &seed.SchemaVersion, &seed.Version, &revisionID, &parentID, &seed.ParentVersion,
		&operation, &identityJSON, &communicationJSON, &temperamentJSON, &emotionalJSON,
		&relationshipJSON, &backstoryJSON, &evolutionJSON, &reason, &createdAt, &updatedAt,
	); err != nil {
		return domain.PersonalizationSeed{}, wrappedSQLError("scan personalization seed", err)
	}
	seed.AgentID, seed.RevisionID = domain.ID(agentID), domain.ID(revisionID)
	if parentID.Valid {
		seed.ParentID = domain.ID(parentID.String)
	}
	seed.Operation, seed.Reason = domain.PersonalizationOperation(operation), reason
	for _, item := range []struct {
		name string
		raw  string
		to   any
	}{
		{"identity", identityJSON, &seed.Identity},
		{"communication style", communicationJSON, &seed.CommunicationStyle},
		{"temperament", temperamentJSON, &seed.Temperament},
		{"emotional dynamics", emotionalJSON, &seed.EmotionalDynamics},
		{"relationship seed", relationshipJSON, &seed.RelationshipSeed},
		{"backstory", backstoryJSON, &seed.Backstory},
		{"evolution policy", evolutionJSON, &seed.EvolutionPolicy},
	} {
		if err := json.Unmarshal([]byte(item.raw), item.to); err != nil {
			return domain.PersonalizationSeed{}, fmt.Errorf("decode personalization %s: %w", item.name, err)
		}
	}
	var err error
	if seed.CreatedAt, err = scanTime(createdAt); err != nil {
		return domain.PersonalizationSeed{}, err
	}
	if seed.UpdatedAt, err = scanTime(updatedAt); err != nil {
		return domain.PersonalizationSeed{}, err
	}
	if err := seed.Validate(); err != nil {
		return domain.PersonalizationSeed{}, err
	}
	return seed, nil
}
