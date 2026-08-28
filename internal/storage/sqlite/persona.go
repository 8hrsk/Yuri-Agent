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

// PersonaVersionMetadata preserves application-owned revision identity and
// explainability fields while the repository supplies safe defaults.
type PersonaVersionMetadata struct {
	RevisionID    domain.ID
	ParentID      domain.ID
	ParentVersion uint64
	Operation     domain.PersonaOperation
	Diff          map[string]float64
	Reason        string
	Evidence      []domain.EvidenceLink
	AuthorRunID   domain.ID
}

// PersonaVersionRecord is the domain history envelope re-exported from the
// SQLite adapter for callers that already import storage.
type PersonaVersionRecord = domain.PersonaVersionRecord

// PersonaRepository is the authoritative append-only mutable-persona store.
// The immutable security policy and identity seed are deliberately not
// represented here and therefore cannot be changed by this adapter.
type PersonaRepository struct {
	db     *sql.DB
	limits domain.PersonaLimits
}

var _ domain.PersonaRepository = (*PersonaRepository)(nil)

func NewPersonaRepository(database *sql.DB) *PersonaRepository {
	return &PersonaRepository{db: database, limits: domain.DefaultPersonaLimits}
}

func (r *PersonaRepository) SetLimits(limits domain.PersonaLimits) error {
	if r == nil || !limitsValid(limits) {
		return fmt.Errorf("%w: invalid persona limits", domain.ErrInvalidArgument)
	}
	r.limits = limits
	return nil
}

func limitsValid(limits domain.PersonaLimits) bool {
	// ValidateWithLimits performs the same checks without requiring a persona.
	return limits.MinValue <= limits.MaxValue && limits.MinValue >= -1 && limits.MaxValue <= 1 &&
		limits.MaxDelta >= 0 && limits.MaxDelta <= 1 && limits.MaxTraits >= 1 && limits.MaxTraits <= 1024 &&
		limits.MaxPromptBytes >= 0 && limits.MaxPromptBytes <= 1<<20
}

func (r *PersonaRepository) Create(ctx context.Context, persona domain.MutablePersona) error {
	return r.createWithMetadata(ctx, persona, nil)
}

func (r *PersonaRepository) CreateWithMetadata(ctx context.Context, persona domain.MutablePersona, metadata PersonaVersionMetadata) error {
	return r.createWithMetadata(ctx, persona, &metadata)
}

func (r *PersonaRepository) createWithMetadata(ctx context.Context, persona domain.MutablePersona, metadata *PersonaVersionMetadata) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	persona = normalizePersonaForCreate(persona)
	if err := persona.ValidateWithLimits(r.limits); err != nil {
		return err
	}
	if metadata != nil {
		if metadata.ParentVersion != 0 || !metadata.ParentID.Empty() {
			return fmt.Errorf("%w: create persona cannot have a parent", domain.ErrInvalidArgument)
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin create persona", err)
	}
	defer tx.Rollback()
	var existing uint64
	err = tx.QueryRowContext(ctx, `SELECT version FROM persona_heads WHERE persona_id = ?`, string(persona.ID)).Scan(&existing)
	if err == nil {
		return domain.ErrConflict
	}
	if !isNoRows(err) {
		return wrappedSQLError("check persona head", err)
	}
	if err := r.appendPersonaTx(ctx, tx, persona, 0, metadata, false); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrappedSQLError("commit create persona", err)
	}
	return nil
}

// AppendVersion atomically appends the next revision after comparing the
// supplied expectedVersion with the current head. Optional metadata is
// accepted for callers that do not need a custom revision ID.
func (r *PersonaRepository) AppendVersion(ctx context.Context, persona domain.MutablePersona, expectedVersion uint64, metadata ...any) (domain.MutablePersona, error) {
	var decoded *PersonaVersionMetadata
	if len(metadata) > 1 {
		return domain.MutablePersona{}, fmt.Errorf("%w: at most one persona metadata value is allowed", domain.ErrInvalidArgument)
	}
	if len(metadata) == 1 {
		var err error
		decoded, err = personaMetadata(metadata[0])
		if err != nil {
			return domain.MutablePersona{}, err
		}
	}
	return r.appendVersionWithMetadata(ctx, persona, expectedVersion, decoded)
}

func (r *PersonaRepository) AppendVersionWithMetadata(ctx context.Context, persona domain.MutablePersona, expectedVersion uint64, metadata PersonaVersionMetadata) (domain.MutablePersona, error) {
	return r.appendVersionWithMetadata(ctx, persona, expectedVersion, &metadata)
}

func (r *PersonaRepository) appendVersionWithMetadata(ctx context.Context, persona domain.MutablePersona, expectedVersion uint64, metadata *PersonaVersionMetadata) (domain.MutablePersona, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.MutablePersona{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.MutablePersona{}, err
	}
	if expectedVersion == 0 {
		return domain.MutablePersona{}, fmt.Errorf("%w: expected persona version must be positive", domain.ErrInvalidArgument)
	}
	if persona.ID.Empty() {
		return domain.MutablePersona{}, fmt.Errorf("%w: persona id is required", domain.ErrInvalidArgument)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.MutablePersona{}, wrappedSQLError("begin append persona", err)
	}
	defer tx.Rollback()
	current, err := getPersonaVersionTx(ctx, tx, persona.ID, 0)
	if err != nil {
		return domain.MutablePersona{}, err
	}
	if current.Version != expectedVersion || persona.Version != expectedVersion+1 {
		return domain.MutablePersona{}, domain.ErrConflict
	}
	persona = normalizePersonaForAppend(persona, current)
	// Callers commonly copy the current snapshot before incrementing Version;
	// clear that snapshot's old parent metadata and let this transaction attach
	// the actual current parent below.
	if persona.ParentVersion == current.ParentVersion {
		persona.ParentVersion = 0
	}
	operation := persona.Operation
	if metadata != nil && metadata.Operation != "" {
		operation = metadata.Operation
	}
	if metadata == nil || operation == "" || operation == domain.PersonaOperationCreate {
		operation = domain.PersonaOperationUpdate
	}
	if metadata != nil && strings.TrimSpace(metadata.Reason) != "" {
		persona.Reason = metadata.Reason
	}
	if metadata != nil && len(metadata.Evidence) > 0 {
		persona.Evidence = append([]domain.EvidenceLink(nil), metadata.Evidence...)
	}
	// Pinning is an explicit owner curation operation. It changes no trait
	// value and therefore does not require model-evidence like autonomous
	// evolution does.
	if operation != domain.PersonaOperationRollback && operation != domain.PersonaOperationReset && operation != domain.PersonaOperationPin {
		if err := domain.ValidatePersonaEvolution(current, persona, r.limits); err != nil {
			return domain.MutablePersona{}, err
		}
	}
	persona.Operation = operation
	if metadata != nil && strings.TrimSpace(metadata.Reason) != "" {
		persona.Reason = metadata.Reason
	}
	if metadata != nil && len(metadata.Evidence) > 0 {
		persona.Evidence = append([]domain.EvidenceLink(nil), metadata.Evidence...)
	}
	if metadata != nil && !metadata.AuthorRunID.Empty() {
		persona.AuthorRunID = metadata.AuthorRunID
	}
	if !operation.Valid() {
		return domain.MutablePersona{}, fmt.Errorf("%w: invalid persona operation %q", domain.ErrInvalidArgument, operation)
	}
	if metadata != nil && !metadata.RevisionID.Empty() {
		persona.RevisionID = metadata.RevisionID
	}
	if persona.RevisionID == current.RevisionID {
		persona.RevisionID = ""
	}
	if metadata != nil && !metadata.ParentID.Empty() {
		persona.ParentID = metadata.ParentID
	}
	if persona.RevisionID.Empty() {
		persona.RevisionID = domain.ID(fmt.Sprintf("%s:v%d", persona.ID, persona.Version))
	}
	if persona.ParentID.Empty() {
		persona.ParentID = current.RevisionID
	}
	persona.ParentVersion = current.Version
	if err := r.appendPersonaTx(ctx, tx, persona, current.Version, metadata, true); err != nil {
		return domain.MutablePersona{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.MutablePersona{}, wrappedSQLError("commit append persona", err)
	}
	return persona, nil
}

// Get returns the current persona snapshot for a logical persona ID.
func (r *PersonaRepository) Get(ctx context.Context, id domain.ID) (domain.MutablePersona, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.MutablePersona{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.MutablePersona{}, err
	}
	if id.Empty() {
		return domain.MutablePersona{}, fmt.Errorf("%w: persona id is required", domain.ErrInvalidArgument)
	}
	return getPersonaVersion(ctx, r.db, id, 0)
}

func (r *PersonaRepository) GetCurrent(ctx context.Context, id domain.ID) (domain.MutablePersona, error) {
	return r.Get(ctx, id)
}

func (r *PersonaRepository) Current(ctx context.Context, id domain.ID) (domain.MutablePersona, error) {
	return r.Get(ctx, id)
}

func (r *PersonaRepository) GetVersion(ctx context.Context, id domain.ID, version uint64) (domain.MutablePersona, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.MutablePersona{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.MutablePersona{}, err
	}
	if id.Empty() || version == 0 {
		return domain.MutablePersona{}, fmt.Errorf("%w: persona id and positive version are required", domain.ErrInvalidArgument)
	}
	return getPersonaVersion(ctx, r.db, id, version)
}

func (r *PersonaRepository) GetVersionRecord(ctx context.Context, id domain.ID, version uint64) (PersonaVersionRecord, error) {
	if err := requireDatabase(r.db); err != nil {
		return PersonaVersionRecord{}, err
	}
	if err := contextErr(ctx); err != nil {
		return PersonaVersionRecord{}, err
	}
	if id.Empty() || version == 0 {
		return PersonaVersionRecord{}, fmt.Errorf("%w: persona id and positive version are required", domain.ErrInvalidArgument)
	}
	persona, err := getPersonaVersion(ctx, r.db, id, version)
	if err != nil {
		return PersonaVersionRecord{}, err
	}
	return personaVersionRecord(persona), nil
}

func (r *PersonaRepository) ListVersions(ctx context.Context, id domain.ID, limit ...int) ([]PersonaVersionRecord, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if id.Empty() {
		return nil, fmt.Errorf("%w: persona id is required", domain.ErrInvalidArgument)
	}
	if len(limit) > 0 && limit[0] < 0 {
		return nil, fmt.Errorf("%w: persona history limit cannot be negative", domain.ErrInvalidArgument)
	}
	query := `SELECT version FROM persona_versions WHERE persona_id = ? ORDER BY version DESC`
	args := []any{string(id)}
	if len(limit) > 0 && limit[0] > 0 {
		query += " LIMIT ?"
		args = append(args, limit[0])
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list persona versions", err)
	}
	defer rows.Close()
	versions := make([]uint64, 0)
	for rows.Next() {
		var version uint64
		if err := rows.Scan(&version); err != nil {
			return nil, wrappedSQLError("scan persona version", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate persona versions", err)
	}
	result := make([]PersonaVersionRecord, 0, len(versions))
	for _, version := range versions {
		item, err := r.GetVersionRecord(ctx, id, version)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func (r *PersonaRepository) ListHistory(ctx context.Context, id domain.ID, limit ...int) ([]PersonaVersionRecord, error) {
	return r.ListVersions(ctx, id, limit...)
}

// Rollback appends a new revision copied from targetVersion. To support both
// concise and explicit callers, args may contain expectedVersion, target
// version, reason, and/or timestamp in any order.
func (r *PersonaRepository) Rollback(ctx context.Context, id domain.ID, args ...any) (domain.MutablePersona, error) {
	expected, target, reason, at, err := parsePersonaRevisionArgs(args...)
	if err != nil {
		return domain.MutablePersona{}, err
	}
	if id.Empty() {
		return domain.MutablePersona{}, fmt.Errorf("%w: persona id is required", domain.ErrInvalidArgument)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.MutablePersona{}, wrappedSQLError("begin persona rollback", err)
	}
	defer tx.Rollback()
	current, err := getPersonaVersionTx(ctx, tx, id, 0)
	if err != nil {
		return domain.MutablePersona{}, err
	}
	if expected == 0 {
		expected = current.Version
	}
	if expected != current.Version {
		return domain.MutablePersona{}, domain.ErrConflict
	}
	if target == 0 || target > current.Version {
		return domain.MutablePersona{}, fmt.Errorf("%w: persona rollback target is invalid", domain.ErrInvalidArgument)
	}
	targetPersona, err := getPersonaVersionTx(ctx, tx, id, target)
	if err != nil {
		return domain.MutablePersona{}, err
	}
	next := clonePersona(targetPersona)
	next.Version = current.Version + 1
	next.ParentVersion = current.Version
	next.ParentID = current.RevisionID
	next.RevisionID = ""
	next.Operation = domain.PersonaOperationRollback
	next.Reason = strings.TrimSpace(reason)
	next.UpdatedAt = revisionTime(at, current.UpdatedAt)
	next.CreatedAt = current.CreatedAt
	next.Diff = targetPersona.DeltaFrom(current)
	if err := r.appendPersonaTx(ctx, tx, next, current.Version, nil, true); err != nil {
		return domain.MutablePersona{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.MutablePersona{}, wrappedSQLError("commit persona rollback", err)
	}
	return next, nil
}

// Reset appends a revision copied from the initial seed (version one). The
// prior history remains untouched and is still available through ListHistory.
func (r *PersonaRepository) Reset(ctx context.Context, id domain.ID, args ...any) (domain.MutablePersona, error) {
	expected, reason, at, err := parsePersonaResetArgs(args...)
	if err != nil {
		return domain.MutablePersona{}, err
	}
	if id.Empty() {
		return domain.MutablePersona{}, fmt.Errorf("%w: persona id is required", domain.ErrInvalidArgument)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.MutablePersona{}, wrappedSQLError("begin persona reset", err)
	}
	defer tx.Rollback()
	current, err := getPersonaVersionTx(ctx, tx, id, 0)
	if err != nil {
		return domain.MutablePersona{}, err
	}
	if expected == 0 {
		expected = current.Version
	}
	if expected != current.Version {
		return domain.MutablePersona{}, domain.ErrConflict
	}
	seed, err := getPersonaVersionTx(ctx, tx, id, 1)
	if err != nil {
		return domain.MutablePersona{}, err
	}
	next := clonePersona(seed)
	next.Version = current.Version + 1
	next.ParentVersion = current.Version
	next.ParentID = current.RevisionID
	next.RevisionID = ""
	next.Operation = domain.PersonaOperationReset
	next.Reason = strings.TrimSpace(reason)
	next.UpdatedAt = revisionTime(at, current.UpdatedAt)
	next.CreatedAt = current.CreatedAt
	next.Diff = seed.DeltaFrom(current)
	if err := r.appendPersonaTx(ctx, tx, next, current.Version, nil, true); err != nil {
		return domain.MutablePersona{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.MutablePersona{}, wrappedSQLError("commit persona reset", err)
	}
	return next, nil
}

// PinTrait appends a revision for explicit user curation. A pinned trait is
// immutable for ordinary autonomous appends until this method unpins it.
func (r *PersonaRepository) PinTrait(ctx context.Context, id domain.ID, expectedVersion uint64, trait string, pinned bool, at time.Time, reason string) (domain.MutablePersona, error) {
	if strings.TrimSpace(trait) == "" {
		return domain.MutablePersona{}, fmt.Errorf("%w: persona trait is required", domain.ErrInvalidArgument)
	}
	current, err := r.Get(ctx, id)
	if err != nil {
		return domain.MutablePersona{}, err
	}
	if current.Version != expectedVersion {
		return domain.MutablePersona{}, domain.ErrConflict
	}
	if _, ok := current.Traits[trait]; !ok {
		return domain.MutablePersona{}, fmt.Errorf("%w: persona trait %q is not present", domain.ErrInvalidArgument, trait)
	}
	next := clonePersona(current)
	next.Version++
	next.ParentVersion = current.Version
	next.ParentID = current.RevisionID
	next.RevisionID = ""
	next.Operation = domain.PersonaOperationPin
	next.UpdatedAt = revisionTime(at, current.UpdatedAt)
	next.Reason = strings.TrimSpace(reason)
	next.Diff = map[string]float64{}
	next.PinnedTraits = setPinnedTrait(next.PinnedTraits, trait, pinned)
	return r.appendVersionWithMetadata(ctx, next, current.Version, &PersonaVersionMetadata{Operation: domain.PersonaOperationPin, Reason: next.Reason})
}

func (r *PersonaRepository) SetPinnedTraits(ctx context.Context, id domain.ID, expectedVersion uint64, pinned []string, at time.Time, reason string) (domain.MutablePersona, error) {
	current, err := r.Get(ctx, id)
	if err != nil {
		return domain.MutablePersona{}, err
	}
	if current.Version != expectedVersion {
		return domain.MutablePersona{}, domain.ErrConflict
	}
	next := clonePersona(current)
	next.Version++
	next.ParentVersion = current.Version
	next.ParentID = current.RevisionID
	next.RevisionID = ""
	next.Operation = domain.PersonaOperationPin
	next.UpdatedAt = revisionTime(at, current.UpdatedAt)
	next.Reason = strings.TrimSpace(reason)
	next.PinnedTraits = append([]string(nil), pinned...)
	next.Diff = map[string]float64{}
	if err := next.ValidateWithLimits(r.limits); err != nil {
		return domain.MutablePersona{}, err
	}
	return r.appendVersionWithMetadata(ctx, next, current.Version, &PersonaVersionMetadata{Operation: domain.PersonaOperationPin, Reason: next.Reason})
}

func (r *PersonaRepository) appendPersonaTx(ctx context.Context, tx *sql.Tx, persona domain.MutablePersona, previousVersion uint64, metadata *PersonaVersionMetadata, copyParent bool) error {
	if metadata == nil {
		metadata = &PersonaVersionMetadata{}
	}
	if previousVersion == 0 && (metadata.ParentVersion != 0 || !metadata.ParentID.Empty()) {
		return fmt.Errorf("%w: initial persona revision cannot have a parent", domain.ErrInvalidArgument)
	}
	if previousVersion > 0 && metadata.ParentVersion != 0 && metadata.ParentVersion != previousVersion {
		return domain.ErrConflict
	}
	if persona.RevisionID.Empty() {
		persona.RevisionID = domain.ID(fmt.Sprintf("%s:v%d", persona.ID, persona.Version))
	}
	if metadata.RevisionID.Empty() {
		metadata.RevisionID = persona.RevisionID
	}
	if metadata.ParentVersion == 0 && previousVersion > 0 {
		metadata.ParentVersion = previousVersion
	}
	if metadata.ParentID.Empty() && previousVersion > 0 {
		var parentRevision string
		if err := tx.QueryRowContext(ctx, `SELECT revision_id FROM persona_versions WHERE persona_id = ? AND version = ?`, string(persona.ID), previousVersion).Scan(&parentRevision); err != nil {
			return wrappedSQLError("get persona parent", err)
		}
		metadata.ParentID = domain.ID(parentRevision)
	}
	if metadata.Operation == "" {
		metadata.Operation = persona.Operation
	}
	if metadata.Operation == "" {
		metadata.Operation = domain.PersonaOperationUpdate
	}
	if metadata.Reason == "" {
		metadata.Reason = persona.Reason
	}
	if metadata.Diff == nil {
		metadata.Diff = cloneFloatMap(persona.Diff)
	}
	if metadata.Evidence == nil {
		metadata.Evidence = append([]domain.EvidenceLink(nil), persona.Evidence...)
	}
	if metadata.AuthorRunID.Empty() {
		metadata.AuthorRunID = persona.AuthorRunID
	}
	traitsJSON, err := marshalJSON(persona.Traits, "{}")
	if err != nil {
		return err
	}
	diffJSON, err := marshalJSON(metadata.Diff, "{}")
	if err != nil {
		return err
	}
	pinnedJSON, err := marshalJSON(domain.SortedPersonaTraitNames(persona.PinnedTraits), "[]")
	if err != nil {
		return err
	}
	evidenceJSON, err := marshalJSON(metadata.Evidence, "[]")
	if err != nil {
		return err
	}
	prompt := persona.Prompt()
	createdAt, err := timeValue(persona.CreatedAt)
	if err != nil {
		return err
	}
	updatedAt, err := timeValue(persona.UpdatedAt)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO persona_versions(
			persona_id, version, revision_id, parent_id, parent_version, operation,
			traits_json, diff_json, pinned_traits_json, prompt_text, reason, evidence_json,
			author_run_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(persona.ID), persona.Version, string(metadata.RevisionID), nullableID(metadata.ParentID), metadata.ParentVersion,
		string(metadata.Operation), traitsJSON, diffJSON, pinnedJSON, prompt, metadata.Reason, evidenceJSON,
		nullableID(metadata.AuthorRunID), createdAt, updatedAt)
	if err != nil {
		return wrappedSQLError("insert persona version", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO persona_heads(persona_id, version, revision_id, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(persona_id) DO UPDATE SET version = excluded.version, revision_id = excluded.revision_id, updated_at = excluded.updated_at`,
		string(persona.ID), persona.Version, string(metadata.RevisionID), updatedAt)
	if err != nil {
		return wrappedSQLError("update persona head", err)
	}
	_ = copyParent // kept in the signature for readability at call sites
	return nil
}

func getPersonaVersion(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id domain.ID, version uint64) (domain.MutablePersona, error) {
	if version == 0 {
		row := queryer.QueryRowContext(ctx, personaSelect+` FROM persona_heads AS ph JOIN persona_versions AS pv
			ON pv.persona_id = ph.persona_id AND pv.version = ph.version WHERE ph.persona_id = ?`, string(id))
		return scanPersona(row)
	}
	row := queryer.QueryRowContext(ctx, personaSelect+` FROM persona_versions AS pv WHERE pv.persona_id = ? AND pv.version = ?`, string(id), version)
	return scanPersona(row)
}

func getPersonaVersionTx(ctx context.Context, tx *sql.Tx, id domain.ID, version uint64) (domain.MutablePersona, error) {
	return getPersonaVersion(ctx, tx, id, version)
}

const personaSelect = `SELECT pv.persona_id, pv.version, pv.revision_id, pv.parent_id, pv.parent_version,
	pv.operation, pv.traits_json, pv.diff_json, pv.pinned_traits_json, pv.prompt_text, pv.reason,
	pv.evidence_json, pv.author_run_id, pv.created_at, pv.updated_at`

func scanPersona(row interface{ Scan(...any) error }) (domain.MutablePersona, error) {
	var (
		persona                                    domain.MutablePersona
		idValue, revisionID, operation, traitsJSON string
		diffJSON, pinnedJSON, prompt, reason       string
		evidenceJSON, createdAt, updatedAt         string
		parentID, authorRunID                      sql.NullString
	)
	if err := row.Scan(&idValue, &persona.Version, &revisionID, &parentID, &persona.ParentVersion,
		&operation, &traitsJSON, &diffJSON, &pinnedJSON, &prompt, &reason, &evidenceJSON,
		&authorRunID, &createdAt, &updatedAt); err != nil {
		return domain.MutablePersona{}, wrappedSQLError("scan persona", err)
	}
	persona.ID = domain.ID(idValue)
	persona.RevisionID = domain.ID(revisionID)
	persona.Operation = domain.PersonaOperation(operation)
	persona.IdentityPrompt = prompt
	persona.PromptText = prompt
	persona.Reason = reason
	if parentID.Valid {
		persona.ParentID = domain.ID(parentID.String)
	}
	if authorRunID.Valid {
		persona.AuthorRunID = domain.ID(authorRunID.String)
	}
	if err := json.Unmarshal([]byte(traitsJSON), &persona.Traits); err != nil {
		return domain.MutablePersona{}, fmt.Errorf("decode persona traits: %w", err)
	}
	if err := json.Unmarshal([]byte(diffJSON), &persona.Diff); err != nil {
		return domain.MutablePersona{}, fmt.Errorf("decode persona diff: %w", err)
	}
	if err := json.Unmarshal([]byte(pinnedJSON), &persona.PinnedTraits); err != nil {
		return domain.MutablePersona{}, fmt.Errorf("decode persona pinned traits: %w", err)
	}
	if err := json.Unmarshal([]byte(evidenceJSON), &persona.Evidence); err != nil {
		return domain.MutablePersona{}, fmt.Errorf("decode persona evidence: %w", err)
	}
	var err error
	if persona.CreatedAt, err = scanTime(createdAt); err != nil {
		return domain.MutablePersona{}, err
	}
	if persona.UpdatedAt, err = scanTime(updatedAt); err != nil {
		return domain.MutablePersona{}, err
	}
	return persona, nil
}

func normalizePersonaForCreate(persona domain.MutablePersona) domain.MutablePersona {
	if persona.Version == 0 {
		persona.Version = 1
	}
	if persona.Operation == "" {
		persona.Operation = domain.PersonaOperationCreate
	}
	if persona.CreatedAt.IsZero() && !persona.UpdatedAt.IsZero() {
		persona.CreatedAt = persona.UpdatedAt
	}
	if persona.UpdatedAt.IsZero() && !persona.CreatedAt.IsZero() {
		persona.UpdatedAt = persona.CreatedAt
	}
	persona.Traits = cloneFloatMap(persona.Traits)
	persona.PinnedTraits = domain.SortedPersonaTraitNames(persona.PinnedTraits)
	if persona.IdentityPrompt == "" {
		persona.IdentityPrompt = persona.PromptText
	}
	if persona.PromptText == "" {
		persona.PromptText = persona.IdentityPrompt
	}
	return persona
}

func normalizePersonaForAppend(persona, current domain.MutablePersona) domain.MutablePersona {
	persona.CreatedAt = current.CreatedAt
	if persona.UpdatedAt.IsZero() {
		persona.UpdatedAt = time.Now().UTC()
	}
	if persona.IdentityPrompt == "" {
		persona.IdentityPrompt = persona.PromptText
	}
	if persona.PromptText == "" {
		persona.PromptText = persona.IdentityPrompt
	}
	if persona.Operation == "" {
		persona.Operation = domain.PersonaOperationUpdate
	}
	persona.PinnedTraits = domain.SortedPersonaTraitNames(persona.PinnedTraits)
	persona.Diff = persona.DeltaFrom(current)
	return persona
}

func clonePersona(persona domain.MutablePersona) domain.MutablePersona {
	persona.Traits = cloneFloatMap(persona.Traits)
	persona.Diff = cloneFloatMap(persona.Diff)
	persona.PinnedTraits = append([]string(nil), persona.PinnedTraits...)
	persona.Evidence = append([]domain.EvidenceLink(nil), persona.Evidence...)
	return persona
}

func personaVersionRecord(persona domain.MutablePersona) PersonaVersionRecord {
	return PersonaVersionRecord{Persona: persona, RevisionID: persona.RevisionID, ParentID: persona.ParentID,
		ParentVersion: persona.ParentVersion, Operation: persona.Operation, Diff: cloneFloatMap(persona.Diff),
		Reason: persona.Reason, Evidence: append([]domain.EvidenceLink(nil), persona.Evidence...), AuthorRunID: persona.AuthorRunID}
}

func personaMetadata(value any) (*PersonaVersionMetadata, error) {
	switch metadata := value.(type) {
	case PersonaVersionMetadata:
		return &metadata, nil
	case *PersonaVersionMetadata:
		if metadata == nil {
			return nil, fmt.Errorf("%w: nil persona metadata", domain.ErrInvalidArgument)
		}
		copy := *metadata
		return &copy, nil
	default:
		return nil, fmt.Errorf("%w: unsupported persona metadata %T", domain.ErrInvalidArgument, value)
	}
}

func parsePersonaRevisionArgs(args ...any) (expected, target uint64, reason string, at time.Time, err error) {
	versions := make([]uint64, 0, 2)
	for _, value := range args {
		switch item := value.(type) {
		case uint64:
			versions = append(versions, item)
		case uint:
			versions = append(versions, uint64(item))
		case int:
			if item < 0 {
				err = fmt.Errorf("%w: persona version cannot be negative", domain.ErrInvalidArgument)
			} else {
				versions = append(versions, uint64(item))
			}
		case string:
			if reason == "" {
				reason = item
			} else {
				err = fmt.Errorf("%w: duplicate persona rollback reason", domain.ErrInvalidArgument)
			}
		case time.Time:
			at = item
		default:
			err = fmt.Errorf("%w: unsupported persona rollback argument %T", domain.ErrInvalidArgument, value)
		}
		if err != nil {
			return
		}
	}
	if err == nil {
		switch len(versions) {
		case 0:
		case 1:
			target = versions[0]
		case 2:
			expected, target = versions[0], versions[1]
		default:
			err = fmt.Errorf("%w: too many persona versions", domain.ErrInvalidArgument)
		}
	}
	return
}

func parsePersonaResetArgs(args ...any) (expected uint64, reason string, at time.Time, err error) {
	for _, value := range args {
		switch item := value.(type) {
		case uint64:
			expected = item
		case uint:
			expected = uint64(item)
		case int:
			if item < 0 {
				err = fmt.Errorf("%w: persona version cannot be negative", domain.ErrInvalidArgument)
			} else {
				expected = uint64(item)
			}
		case string:
			reason = item
		case time.Time:
			at = item
		default:
			err = fmt.Errorf("%w: unsupported persona reset argument %T", domain.ErrInvalidArgument, value)
		}
		if err != nil {
			return
		}
	}
	return
}

func revisionTime(value, fallback time.Time) time.Time {
	if value.IsZero() {
		if fallback.IsZero() {
			return time.Now().UTC()
		}
		return fallback.Add(time.Nanosecond)
	}
	return value.UTC()
}

func setPinnedTrait(current []string, trait string, pinned bool) []string {
	set := make(map[string]struct{}, len(current)+1)
	for _, item := range current {
		set[item] = struct{}{}
	}
	if pinned {
		set[trait] = struct{}{}
	} else {
		delete(set, trait)
	}
	result := make([]string, 0, len(set))
	for item := range set {
		result = append(result, item)
	}
	return domain.SortedPersonaTraitNames(result)
}

func cloneFloatMap(input map[string]float64) map[string]float64 {
	if input == nil {
		return nil
	}
	result := make(map[string]float64, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
