package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func (r *AffectiveRepository) appendAffectStateTx(ctx context.Context, tx *sql.Tx, state domain.AffectiveState, previousVersion uint64, metadata *AffectiveVersionMetadata) error {
	if err := state.Validate(); err != nil {
		return err
	}
	if metadata == nil {
		metadata = &AffectiveVersionMetadata{}
	}
	if previousVersion == 0 && (metadata.ParentVersion != 0 || !metadata.ParentID.Empty()) {
		return fmt.Errorf("%w: initial affect revision cannot have a parent", domain.ErrInvalidArgument)
	}
	if previousVersion > 0 && metadata.ParentVersion != 0 && metadata.ParentVersion != previousVersion {
		return domain.ErrConflict
	}
	if state.RevisionID.Empty() {
		state.RevisionID = domain.ID(fmt.Sprintf("%s:v%d", state.ID, state.Version))
	}
	if metadata.RevisionID.Empty() {
		metadata.RevisionID = state.RevisionID
	}
	if metadata.ParentVersion == 0 && previousVersion > 0 {
		metadata.ParentVersion = previousVersion
	}
	if metadata.ParentID.Empty() && previousVersion > 0 {
		var parentRevision string
		if err := tx.QueryRowContext(ctx, `SELECT revision_id FROM affective_states WHERE affect_id = ? AND version = ?`, string(state.ID), previousVersion).Scan(&parentRevision); err != nil {
			return wrappedSQLError("get affect parent", err)
		}
		metadata.ParentID = domain.ID(parentRevision)
	}
	if metadata.Operation == "" {
		metadata.Operation = state.Operation
	}
	if metadata.Operation == "" {
		metadata.Operation = domain.AffectOperationUpdate
	}
	if metadata.Reason == "" {
		metadata.Reason = state.Reason
	}
	if metadata.AuthorRunID.Empty() {
		metadata.AuthorRunID = state.AuthorRunID
	}
	emotions := state.Emotions
	if emotions == nil {
		if state.Dimensions != nil {
			emotions = state.Dimensions
		} else {
			emotions = state.Values
		}
	}
	emotionsJSON, err := marshalJSON(emotions, "{}")
	if err != nil {
		return err
	}
	createdAt, err := timeValue(state.CreatedAt)
	if err != nil {
		return err
	}
	updatedAt, err := timeValue(state.UpdatedAt)
	if err != nil {
		return err
	}
	var asOf any
	if !state.AsOf.IsZero() {
		asOf = formatTime(state.AsOf)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO affective_states(
			affect_id, version, revision_id, parent_id, parent_version, operation,
			emotions_json, summary, reason, author_run_id, as_of, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(state.ID), state.Version, string(metadata.RevisionID), nullableID(metadata.ParentID), metadata.ParentVersion,
		string(metadata.Operation), emotionsJSON, strings.TrimSpace(state.Summary), metadata.Reason,
		nullableID(metadata.AuthorRunID), asOf, createdAt, updatedAt)
	if err != nil {
		return wrappedSQLError("insert affect state", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO affective_heads(affect_id, version, revision_id, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(affect_id) DO UPDATE SET version = excluded.version, revision_id = excluded.revision_id, updated_at = excluded.updated_at`,
		string(state.ID), state.Version, string(metadata.RevisionID), updatedAt)
	if err != nil {
		return wrappedSQLError("update affect head", err)
	}
	return nil
}

const affectSelect = `SELECT av.affect_id, av.version, av.revision_id, av.parent_id, av.parent_version,
	av.operation, av.emotions_json, av.summary, av.reason, av.author_run_id, av.as_of,
	av.created_at, av.updated_at`

func getAffectVersion(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id domain.ID, version uint64) (domain.AffectiveState, error) {
	if version == 0 {
		return scanAffect(queryer.QueryRowContext(ctx, affectSelect+` FROM affective_heads AS ah
			JOIN affective_states AS av ON av.affect_id = ah.affect_id AND av.version = ah.version
			WHERE ah.affect_id = ?`, string(id)))
	}
	return scanAffect(queryer.QueryRowContext(ctx, affectSelect+` FROM affective_states AS av WHERE av.affect_id = ? AND av.version = ?`, string(id), version))
}

func getAffectVersionTx(ctx context.Context, tx *sql.Tx, id domain.ID, version uint64) (domain.AffectiveState, error) {
	return getAffectVersion(ctx, tx, id, version)
}

func scanAffect(row rowScanner) (domain.AffectiveState, error) {
	var (
		state                          domain.AffectiveState
		idValue, revisionID, operation string
		emotionsJSON, summary, reason  string
		createdAt, updatedAt           string
		asOf                           sql.NullString
		parentID, authorRunID          sql.NullString
	)
	if err := row.Scan(&idValue, &state.Version, &revisionID, &parentID, &state.ParentVersion,
		&operation, &emotionsJSON, &summary, &reason, &authorRunID, &asOf, &createdAt, &updatedAt); err != nil {
		return domain.AffectiveState{}, wrappedSQLError("scan affect state", err)
	}
	state.ID = domain.ID(idValue)
	state.RevisionID = domain.ID(revisionID)
	state.Operation = domain.AffectOperation(operation)
	state.Summary = summary
	state.Reason = reason
	if parentID.Valid {
		state.ParentID = domain.ID(parentID.String)
	}
	if authorRunID.Valid {
		state.AuthorRunID = domain.ID(authorRunID.String)
	}
	if err := json.Unmarshal([]byte(emotionsJSON), &state.Emotions); err != nil {
		return domain.AffectiveState{}, fmt.Errorf("decode affect emotions: %w", err)
	}
	var err error
	if asOf.Valid {
		if state.AsOf, err = scanTime(asOf.String); err != nil {
			return domain.AffectiveState{}, err
		}
	}
	if state.CreatedAt, err = scanTime(createdAt); err != nil {
		return domain.AffectiveState{}, err
	}
	if state.UpdatedAt, err = scanTime(updatedAt); err != nil {
		return domain.AffectiveState{}, err
	}
	return state, nil
}

const affectEventSelect = `SELECT id, affect_id, state_version, source_id, source_type, run_id,
	conversation_id, emotion, intensity, valence, decay_policy, decay_rate, half_life_seconds,
	decays_at, provenance, evidence_json, metadata_json, created_at`

func insertAffectiveEvent(ctx context.Context, tx *sql.Tx, event domain.AffectiveEvent) error {
	evidenceJSON, err := marshalJSON(event.Evidence, "[]")
	if err != nil {
		return err
	}
	createdAt, err := timeValue(event.CreatedAt)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO affective_events(
			id, affect_id, state_version, source_id, source_type, run_id, conversation_id,
			emotion, intensity, valence, decay_policy, decay_rate, half_life_seconds,
			decays_at, provenance, evidence_json, metadata_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(event.ID), string(event.AffectID), event.StateVersion, nullableID(event.SourceID), event.SourceType,
		nullableID(event.RunID), nullableID(event.ConversationID), event.Emotion, event.Intensity, event.Valence,
		string(event.DecayPolicy), event.DecayRate, event.HalfLifeSeconds, nullableTimeValue(event.DecaysAt),
		event.Provenance, evidenceJSON, event.MetadataJSON, createdAt)
	return wrappedSQLError("insert affective event", err)
}

func scanAffectiveEvent(row rowScanner) (domain.AffectiveEvent, error) {
	var (
		event                                               domain.AffectiveEvent
		idValue, affectID, sourceType, emotion, decayPolicy string
		provenance, evidenceJSON, metadataJSON, createdAt   string
		stateVersion, halfLife                              uint64
		intensity, valence, decayRate                       float64
		decaysAt                                            sql.NullString
		sourceID, runID, conversationID                     sql.NullString
	)
	if err := row.Scan(&idValue, &affectID, &stateVersion, &sourceID, &sourceType, &runID,
		&conversationID, &emotion, &intensity, &valence, &decayPolicy, &decayRate, &halfLife,
		&decaysAt, &provenance, &evidenceJSON, &metadataJSON, &createdAt); err != nil {
		return domain.AffectiveEvent{}, wrappedSQLError("scan affect event", err)
	}
	event.ID = domain.ID(idValue)
	event.AffectID = domain.ID(affectID)
	event.StateVersion = stateVersion
	event.SourceType = sourceType
	event.Emotion = emotion
	event.Intensity = intensity
	event.Valence = valence
	event.DecayPolicy = domain.AffectiveDecayPolicy(decayPolicy)
	event.DecayRate = decayRate
	event.HalfLifeSeconds = int64(halfLife)
	event.Provenance = provenance
	event.MetadataJSON = metadataJSON
	if sourceID.Valid {
		event.SourceID = domain.ID(sourceID.String)
	}
	if runID.Valid {
		event.RunID = domain.ID(runID.String)
	}
	if conversationID.Valid {
		event.ConversationID = domain.ID(conversationID.String)
	}
	if decaysAt.Valid {
		var err error
		if event.DecaysAt, err = scanTime(decaysAt.String); err != nil {
			return domain.AffectiveEvent{}, err
		}
	}
	if err := json.Unmarshal([]byte(evidenceJSON), &event.Evidence); err != nil {
		return domain.AffectiveEvent{}, fmt.Errorf("decode affect event evidence: %w", err)
	}
	var err error
	if event.CreatedAt, err = scanTime(createdAt); err != nil {
		return domain.AffectiveEvent{}, err
	}
	return event, nil
}
