package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

type PeerSocialRepository struct{ db *sql.DB }

type PeerSocialReflectionRecord struct {
	DialogueID          domain.ID
	ObserverAgentID     domain.ID
	SubjectAgentID      domain.ID
	Outcome             string
	Reason              string
	RelationshipVersion uint64
	AffectVersion       uint64
	CreatedAt           time.Time
}

type PeerSocialMutation struct {
	Record               PeerSocialReflectionRecord
	Relationship         *domain.RelationshipState
	ExpectedRelationship uint64
	Affect               *domain.AffectiveState
	ExpectedAffect       uint64
	AffectEvents         []domain.AffectiveEvent
}

func NewPeerSocialRepository(database *sql.DB) *PeerSocialRepository {
	return &PeerSocialRepository{db: database}
}

func peerRelationshipID(observer, subject domain.ID) domain.ID {
	digest := sha256.Sum256([]byte(observer.String() + "\x00" + subject.String()))
	return domain.ID("relationship_peer_" + hex.EncodeToString(digest[:12]))
}

func (r *PeerSocialRepository) GetOrCreateRelationship(ctx context.Context, observer, subject domain.ID, at time.Time) (domain.RelationshipState, error) {
	if r == nil || r.db == nil || observer.Empty() || subject.Empty() || observer == subject || at.IsZero() {
		return domain.RelationshipState{}, fmt.Errorf("%w: peer relationship identity and timestamp are required", domain.ErrInvalidArgument)
	}
	if err := contextErr(ctx); err != nil {
		return domain.RelationshipState{}, err
	}
	var relationshipID string
	err := r.db.QueryRowContext(ctx, `SELECT relationship_id FROM agent_peer_relationships WHERE observer_agent_id = ? AND subject_agent_id = ?`, observer.String(), subject.String()).Scan(&relationshipID)
	if err == nil {
		return getRelationshipVersion(ctx, r.db, domain.ID(relationshipID), 0)
	}
	if !isNoRows(err) {
		return domain.RelationshipState{}, wrappedSQLError("get peer relationship", err)
	}
	state, err := domain.NewRelationshipState(peerRelationshipID(observer, subject), map[string]float64{
		domain.RelationshipDimensionTrust: .5, domain.RelationshipDimensionRespect: .5,
		domain.RelationshipDimensionCloseness: .2, domain.RelationshipDimensionIrritation: 0,
		domain.RelationshipDimensionJealousy: 0, domain.RelationshipDimensionResentment: 0,
		domain.RelationshipDimensionGratitude: 0, domain.RelationshipDimensionReliability: .5,
	}, "Нейтральное исходное мнение о другом агенте", at.UTC())
	if err != nil {
		return domain.RelationshipState{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RelationshipState{}, wrappedSQLError("begin peer relationship", err)
	}
	defer tx.Rollback()
	if err := (&RelationshipRepository{db: r.db, maxDelta: .25}).appendRelationshipTx(ctx, tx, state, 0, nil); err != nil {
		if !errors.Is(err, domain.ErrConflict) && !strings.Contains(strings.ToLower(err.Error()), "unique") {
			return domain.RelationshipState{}, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_peer_relationships(observer_agent_id, subject_agent_id, relationship_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, observer.String(), subject.String(), state.ID.String(), formatTime(at), formatTime(at))
	if err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "unique") {
			return domain.RelationshipState{}, wrappedSQLError("link peer relationship", err)
		}
		_ = tx.Rollback()
		return r.GetOrCreateRelationship(ctx, observer, subject, at)
	}
	if err := tx.Commit(); err != nil {
		return domain.RelationshipState{}, wrappedSQLError("commit peer relationship", err)
	}
	return state, nil
}

func (r *PeerSocialRepository) GetRelationship(ctx context.Context, observer, subject domain.ID) (domain.RelationshipState, error) {
	if r == nil || r.db == nil || observer.Empty() || subject.Empty() || observer == subject {
		return domain.RelationshipState{}, domain.ErrInvalidArgument
	}
	if err := contextErr(ctx); err != nil {
		return domain.RelationshipState{}, err
	}
	var id string
	if err := r.db.QueryRowContext(ctx, `SELECT relationship_id FROM agent_peer_relationships WHERE observer_agent_id = ? AND subject_agent_id = ?`, observer.String(), subject.String()).Scan(&id); err != nil {
		return domain.RelationshipState{}, wrappedSQLError("get peer relationship link", err)
	}
	return getRelationshipVersion(ctx, r.db, domain.ID(id), 0)
}

func (r *PeerSocialRepository) GetReflection(ctx context.Context, dialogueID, observer domain.ID) (PeerSocialReflectionRecord, error) {
	if r == nil || r.db == nil || dialogueID.Empty() || observer.Empty() {
		return PeerSocialReflectionRecord{}, domain.ErrInvalidArgument
	}
	if err := contextErr(ctx); err != nil {
		return PeerSocialReflectionRecord{}, err
	}
	var item PeerSocialReflectionRecord
	var created string
	err := r.db.QueryRowContext(ctx, `SELECT dialogue_id, observer_agent_id, subject_agent_id, outcome, reason, relationship_version, affect_version, created_at FROM peer_social_reflections WHERE dialogue_id = ? AND observer_agent_id = ?`, dialogueID.String(), observer.String()).Scan(&item.DialogueID, &item.ObserverAgentID, &item.SubjectAgentID, &item.Outcome, &item.Reason, &item.RelationshipVersion, &item.AffectVersion, &created)
	if err != nil {
		return item, wrappedSQLError("get peer social reflection", err)
	}
	item.CreatedAt, err = scanTime(created)
	return item, err
}

func (r *PeerSocialRepository) Apply(ctx context.Context, mutation PeerSocialMutation) error {
	record := mutation.Record
	if r == nil || r.db == nil || record.DialogueID.Empty() || record.ObserverAgentID.Empty() || record.SubjectAgentID.Empty() || record.ObserverAgentID == record.SubjectAgentID || (record.Outcome != "no_change" && record.Outcome != "changed") || record.CreatedAt.IsZero() {
		return fmt.Errorf("%w: invalid peer social reflection", domain.ErrInvalidArgument)
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	// A no-change model decision may still carry the engine's deterministic
	// affect decay projection. It may never mutate the peer relationship.
	if record.Outcome == "no_change" && mutation.Relationship != nil {
		return domain.ErrInvalidArgument
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin peer social reflection", err)
	}
	defer tx.Rollback()
	if mutation.Relationship != nil {
		current, loadErr := getRelationshipVersionTx(ctx, tx, mutation.Relationship.ID, 0)
		if loadErr != nil {
			return loadErr
		}
		if current.Version != mutation.ExpectedRelationship || mutation.Relationship.Version != current.Version+1 {
			return domain.ErrConflict
		}
		next := normalizeRelationshipForAppend(*mutation.Relationship, current)
		if err := domain.ValidateRelationshipEvolution(current, next, .10); err != nil {
			return err
		}
		if err := (&RelationshipRepository{db: r.db, maxDelta: .10}).appendRelationshipTx(ctx, tx, next, current.Version, &RelationshipVersionMetadata{Operation: domain.RelationshipOperationUpdate, Reason: next.Reason, Evidence: next.Evidence, AuthorRunID: next.AuthorRunID}); err != nil {
			return err
		}
		record.RelationshipVersion = next.Version
	}
	if mutation.Affect != nil {
		current, loadErr := getAffectVersionTx(ctx, tx, mutation.Affect.ID, 0)
		if loadErr != nil {
			return loadErr
		}
		if current.Version != mutation.ExpectedAffect || mutation.Affect.Version != current.Version+1 {
			return domain.ErrConflict
		}
		next := normalizeAffectForAppend(*mutation.Affect, current)
		if err := next.Validate(); err != nil {
			return err
		}
		if err := (&AffectiveRepository{db: r.db}).appendAffectStateTx(ctx, tx, next, current.Version, &AffectiveVersionMetadata{Operation: domain.AffectOperationUpdate, Reason: next.Reason, AuthorRunID: next.AuthorRunID}); err != nil {
			return err
		}
		for _, event := range mutation.AffectEvents {
			if err := event.Validate(); err != nil {
				return err
			}
			event.AffectID = next.ID
			event.StateVersion = next.Version
			if err := insertAffectiveEvent(ctx, tx, event); err != nil {
				return err
			}
		}
		record.AffectVersion = next.Version
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO peer_social_reflections(dialogue_id, observer_agent_id, subject_agent_id, outcome, reason, relationship_version, affect_version, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, record.DialogueID.String(), record.ObserverAgentID.String(), record.SubjectAgentID.String(), record.Outcome, strings.TrimSpace(record.Reason), record.RelationshipVersion, record.AffectVersion, formatTime(record.CreatedAt))
	if err != nil {
		return wrappedSQLError("record peer social reflection", err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE agent_peer_relationships SET updated_at = ? WHERE observer_agent_id = ? AND subject_agent_id = ?`, formatTime(record.CreatedAt), record.ObserverAgentID.String(), record.SubjectAgentID.String())
	if err != nil {
		return wrappedSQLError("touch peer relationship", err)
	}
	if err := tx.Commit(); err != nil {
		return wrappedSQLError("commit peer social reflection", err)
	}
	return nil
}
